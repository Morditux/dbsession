package dbsession

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

var (
	// ErrSessionTooLarge is returned when the session data exceeds the configured MaxSessionBytes.
	ErrSessionTooLarge = errors.New("session data too large")

	// ErrInvalidSessionID is returned when the session ID format is invalid.
	ErrInvalidSessionID = errors.New("invalid session id")

	// ErrInvalidConfig is returned when manager configuration is unsafe or
	// cannot be represented correctly by HTTP cookies.
	ErrInvalidConfig = errors.New("invalid manager config")
)

type Manager struct {
	store           Store
	ttl             time.Duration
	cookie          string
	cookiePath      string
	cookieDomain    string
	cleanup         time.Duration
	disableCleanup  bool
	stopChan        chan struct{}
	workerDone      chan struct{}
	stopOnce        sync.Once
	storeCloseOnce  sync.Once
	storeCloseErr   error
	leaveStoreOpen  bool
	httpOnly        bool
	secure          *bool
	sameSite        http.SameSite
	maxSessionBytes int
}

type Config struct {
	Store           Store
	TTL             time.Duration
	CookieName      string
	CookiePath      string
	CookieDomain    string
	CleanupInterval time.Duration
	// DisableCleanup prevents the manager from starting its cleanup goroutine.
	// Use this for stores with native expiration or externally managed cleanup.
	DisableCleanup  bool
	HttpOnly        *bool
	Secure          *bool
	SameSite        http.SameSite
	MaxSessionBytes int // Maximum size in bytes of the serialized session data. 0 means unlimited.
	// LeaveStoreOpen transfers store ownership to the caller. By default, the
	// Manager owns the Store and closes it after its cleanup worker stops.
	// Set this to true when multiple managers share the same Store.
	LeaveStoreOpen bool
}

// NewManager validates cfg before constructing a Manager or starting its
// cleanup goroutine. A zero TTL selects the 24-hour default; explicit TTLs must
// be at least one second and fit in http.Cookie.MaxAge on the target platform.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.CookieName == "" {
		cfg.CookieName = "session_id"
	}
	if cfg.CookiePath == "" {
		cfg.CookiePath = "/"
	}
	if cfg.TTL == 0 {
		cfg.TTL = 24 * time.Hour
	}
	if cfg.CleanupInterval == 0 {
		cfg.CleanupInterval = 10 * time.Minute
	}
	if err := validateManagerConfig(cfg); err != nil {
		return nil, err
	}

	m := &Manager{
		store:           cfg.Store,
		ttl:             cfg.TTL,
		cookie:          cfg.CookieName,
		cookiePath:      cfg.CookiePath,
		cookieDomain:    cfg.CookieDomain,
		cleanup:         cfg.CleanupInterval,
		disableCleanup:  cfg.DisableCleanup,
		stopChan:        make(chan struct{}),
		workerDone:      make(chan struct{}),
		leaveStoreOpen:  cfg.LeaveStoreOpen,
		httpOnly:        true, // Default
		secure:          cfg.Secure,
		sameSite:        http.SameSiteLaxMode, // Default
		maxSessionBytes: cfg.MaxSessionBytes,
	}

	if cfg.HttpOnly != nil {
		m.httpOnly = *cfg.HttpOnly
	}

	if cfg.SameSite != 0 {
		m.sameSite = cfg.SameSite
	}

	// Security: SameSite=None requires Secure=true.
	// Browsers reject SameSite=None cookies if the Secure attribute is missing.
	// We enforce this even if the user didn't explicitly set Secure=true.
	if m.sameSite == http.SameSiteNoneMode {
		secure := true
		m.secure = &secure
	}

	if m.disableCleanup {
		close(m.workerDone)
	} else {
		go m.cleanupWorker()
	}

	return m, nil
}

// MustNewManager is a convenience wrapper for applications that treat invalid
// startup configuration as unrecoverable. It panics if NewManager fails.
func MustNewManager(cfg Config) *Manager {
	m, err := NewManager(cfg)
	if err != nil {
		panic(err)
	}
	return m
}

func validateManagerConfig(cfg Config) error {
	if isNilStore(cfg.Store) {
		return fmt.Errorf("%w: Store must not be nil", ErrInvalidConfig)
	}
	if cfg.TTL < time.Second {
		return fmt.Errorf("%w: TTL must be at least one second", ErrInvalidConfig)
	}
	maxCookieAgeSeconds := int64(int(^uint(0) >> 1))
	if int64(cfg.TTL/time.Second) > maxCookieAgeSeconds {
		return fmt.Errorf("%w: TTL exceeds Cookie.MaxAge on this platform", ErrInvalidConfig)
	}
	if cfg.CleanupInterval < 0 {
		return fmt.Errorf("%w: CleanupInterval must not be negative", ErrInvalidConfig)
	}
	if !cfg.DisableCleanup && cfg.CleanupInterval <= 0 {
		return fmt.Errorf("%w: CleanupInterval must be positive when cleanup is enabled", ErrInvalidConfig)
	}
	if cfg.MaxSessionBytes < 0 {
		return fmt.Errorf("%w: MaxSessionBytes must not be negative", ErrInvalidConfig)
	}
	if cfg.SameSite < 0 || cfg.SameSite > http.SameSiteNoneMode {
		return fmt.Errorf("%w: unsupported SameSite value %d", ErrInvalidConfig, cfg.SameSite)
	}
	if !strings.HasPrefix(cfg.CookiePath, "/") {
		return fmt.Errorf("%w: CookiePath must start with '/'", ErrInvalidConfig)
	}
	if err := (&http.Cookie{
		Name:     cfg.CookieName,
		Path:     cfg.CookiePath,
		Domain:   cfg.CookieDomain,
		SameSite: cfg.SameSite,
	}).Valid(); err != nil {
		return fmt.Errorf("%w: invalid cookie scope: %v", ErrInvalidConfig, err)
	}
	secureEnabled := cfg.Secure != nil && *cfg.Secure
	if strings.HasPrefix(cfg.CookieName, "__Secure-") && !secureEnabled {
		return fmt.Errorf("%w: __Secure- cookies require Secure=true", ErrInvalidConfig)
	}
	if strings.HasPrefix(cfg.CookieName, "__Host-") &&
		(!secureEnabled || cfg.CookiePath != "/" || cfg.CookieDomain != "") {
		return fmt.Errorf("%w: __Host- cookies require Secure=true, Path=/, and no Domain", ErrInvalidConfig)
	}
	return nil
}

func isNilStore(store Store) bool {
	if store == nil {
		return true
	}
	v := reflect.ValueOf(store)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func (m *Manager) cleanupWorker() {
	ticker := time.NewTicker(m.cleanup)
	defer ticker.Stop()
	defer close(m.workerDone)

	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = m.store.Cleanup(ctx)
			cancel()
		case <-m.stopChan:
			return
		}
	}
}

// Close stops the cleanup worker, waits for it to finish, and closes the store
// when the manager owns it. Close is safe to call multiple times and from
// concurrent goroutines.
func (m *Manager) Close() error {
	return m.CloseContext(context.Background())
}

// CloseContext stops the cleanup worker and waits for it to finish or for ctx
// to be canceled. A timeout does not close the store while cleanup may still be
// using it; a later call to Close or CloseContext can complete the shutdown.
// When LeaveStoreOpen is true, the caller remains responsible for closing the
// store after all managers that share it have stopped.
func (m *Manager) CloseContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	m.stopOnce.Do(func() {
		close(m.stopChan)
	})

	select {
	case <-m.workerDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	if m.leaveStoreOpen {
		return nil
	}

	m.storeCloseOnce.Do(func() {
		m.storeCloseErr = m.store.Close()
	})
	return m.storeCloseErr
}

func (m *Manager) Get(r *http.Request) (*Session, error) {
	cookie, err := r.Cookie(m.cookie)
	if err != nil {
		return m.New(), nil
	}

	// Input validation: Ensure the session ID matches our expected format (32 hex characters).
	// This prevents invalid or malicious keys from reaching the backend store.
	if !isValidID(cookie.Value) {
		return m.New(), nil
	}

	session, err := m.store.Get(r.Context(), cookie.Value)
	if err != nil {
		return nil, err
	}

	if session == nil {
		return m.New(), nil
	}

	// Security: Enforce expiration check at the Manager level.
	// Some stores (like Memcached) might rely on lazy expiration or external TTLs,
	// which can be unreliable or bypassed. We must ensure we never return an expired session.
	if session.ExpiresAt().Before(time.Now()) {
		return m.New(), nil
	}

	return session, nil
}

func (m *Manager) Save(w http.ResponseWriter, r *http.Request, s *Session) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	state := s.stateSnapshot()
	if !isValidID(state.id) {
		return ErrInvalidSessionID
	}
	state.expiresAt = time.Now().Add(m.ttl)

	if err := m.saveState(w, r, state); err != nil {
		return err
	}
	s.setIdentityAndExpiry(state.id, state.expiresAt)
	return nil
}

func (m *Manager) saveState(w http.ResponseWriter, r *http.Request, state sessionState) error {
	// Check session size if limit is configured
	// Optimization: Skip encoding if the session is empty.
	// This saves allocations and CPU cycles for new/empty sessions.
	if m.maxSessionBytes > 0 && len(state.values) > 0 {
		buf := bufferPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer PutBuffer(buf)

		if err := gob.NewEncoder(buf).Encode(state.values); err != nil {
			return err
		}

		if buf.Len() > m.maxSessionBytes {
			return ErrSessionTooLarge
		}

		// Optimization: Store the encoded data in an immutable persistence
		// snapshot so SQL stores do not have to re-encode it.
		// Note: We use the buffer's bytes directly. The Store must consume it before we return from Save.
		// Since store.Save is synchronous, this is safe until PutBuffer runs.
		state.encoded = buf.Bytes()
	}

	if err := m.store.Save(r.Context(), sessionFromState(state)); err != nil {
		return err
	}

	secure := r.TLS != nil
	if m.secure != nil {
		secure = *m.secure
	}

	http.SetCookie(w, &http.Cookie{
		Name:     m.cookie,
		Value:    state.id,
		Path:     m.cookiePath,
		Domain:   m.cookieDomain,
		Expires:  state.expiresAt,
		MaxAge:   int(m.ttl.Seconds()),
		HttpOnly: m.httpOnly,
		Secure:   secure,
		SameSite: m.sameSite,
	})

	return nil
}

// Regenerate regenerates the session ID to prevent session fixation attacks.
// It creates a new session ID, saves the session with the new ID,
// and removes the old session from the store.
func (m *Manager) Regenerate(w http.ResponseWriter, r *http.Request, s *Session) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	state := s.stateSnapshot()
	oldID := state.id
	newID, err := generateID()
	if err != nil {
		return err
	}
	state.id = newID
	state.expiresAt = time.Now().Add(m.ttl)
	if err := m.saveState(w, r, state); err != nil {
		return err
	}

	if err := m.store.Delete(r.Context(), oldID); err != nil {
		// Security: If we fail to delete the old session, we must return an error.
		// Failing to do so leaves the old session ID valid, which could be used
		// in a session fixation attack. We must "fail closed" here.

		// Attempt to cleanup the new session we just created
		_ = m.store.Delete(r.Context(), newID)

		// Force logout by clearing the cookie.
		// This ensures the client is not left with a valid session (newID)
		// while the old session (oldID) might still be valid in the store.
		secure := r.TLS != nil
		if m.secure != nil {
			secure = *m.secure
		}

		http.SetCookie(w, &http.Cookie{
			Name:     m.cookie,
			Value:    "",
			Path:     m.cookiePath,
			Domain:   m.cookieDomain,
			MaxAge:   -1,
			HttpOnly: m.httpOnly,
			Secure:   secure,
			SameSite: m.sameSite,
		})

		return err
	}

	s.setIdentityAndExpiry(newID, state.expiresAt)
	return nil
}

func (m *Manager) Destroy(w http.ResponseWriter, r *http.Request, s *Session) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	state := s.stateSnapshot()

	// Always clear the cookie, even if store deletion fails.
	// This ensures the client side is logged out ("fail safe" for the user).
	secure := r.TLS != nil
	if m.secure != nil {
		secure = *m.secure
	}

	http.SetCookie(w, &http.Cookie{
		Name:     m.cookie,
		Value:    "",
		Path:     m.cookiePath,
		Domain:   m.cookieDomain,
		MaxAge:   -1,
		HttpOnly: m.httpOnly,
		Secure:   secure,
		SameSite: m.sameSite,
	})

	// Security: Clear the session values from memory regardless of whether
	// the store deletion succeeds or fails. This ensures sensitive data
	// is wiped from memory (Defense in Depth).
	defer s.Clear()

	if err := m.store.Delete(r.Context(), state.id); err != nil {
		return err
	}

	return nil
}

func (m *Manager) New() *Session {
	id, err := generateID()
	if err != nil {
		panic(err)
	}
	now := time.Now()
	return RestoreSession(SessionSnapshot{
		ID:        id,
		Values:    make(map[string]any),
		CreatedAt: now,
		ExpiresAt: now.Add(m.ttl),
	})
}

// rngPool reuses *math/rand/v2.Rand instances to amortize the cost of
// seeding from crypto/rand. This significantly reduces syscall overhead
// for ID generation.
var rngPool = sync.Pool{}

func generateID() (string, error) {
	ptr := idBufferPool.Get().(*[]byte)
	b := *ptr

	// Use first 16 bytes for entropy
	entropy := b[:16]

	// Retrieve a seeded generator from the pool.
	v := rngPool.Get()
	var rng *mrand.Rand
	if v == nil {
		// First time use or pool is empty: seed a new generator from crypto/rand.
		var seed [32]byte
		if _, err := io.ReadFull(rand.Reader, seed[:]); err != nil {
			clear(b)
			idBufferPool.Put(ptr)
			return "", err
		}
		rng = mrand.New(mrand.NewChaCha8(seed))
	} else {
		rng = v.(*mrand.Rand)
	}

	// Read 16 bytes (128 bits) of randomness.
	// Since math/rand/v2 provides uint64, we fill the buffer 8 bytes at a time.
	// This avoids allocating a new buffer or stream wrapper.
	binary.LittleEndian.PutUint64(entropy[0:8], rng.Uint64())
	binary.LittleEndian.PutUint64(entropy[8:16], rng.Uint64())

	// Return the generator to the pool for reuse.
	rngPool.Put(rng)

	// Optimization: Encode hex directly into the remaining 32 bytes of the buffer.
	// This avoids allocating a new byte slice inside hex.EncodeToString.
	hexDst := b[16:]
	hex.Encode(hexDst, entropy)
	id := string(hexDst)

	clear(b)
	idBufferPool.Put(ptr)
	return id, nil
}

// validIDChars is a lookup table for valid hex characters (0-9, a-f).
var validIDChars = [256]bool{}

func init() {
	for i := 0; i < len(validIDChars); i++ {
		c := byte(i)
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			validIDChars[i] = true
		}
	}
}

func isValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	// Optimization: Iterate exactly 32 times. Since we verified len(id) == 32,
	// the compiler can eliminate bounds checks for id[i] inside the loop.
	for i := 0; i < 32; i++ {
		// Lookup table is faster than multiple comparisons
		if !validIDChars[id[i]] {
			return false
		}
	}
	return true
}
