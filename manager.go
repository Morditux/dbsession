package dbsession

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"sync"
	"time"
)

var (
	// ErrSessionTooLarge is returned when the session data exceeds the configured MaxSessionBytes.
	ErrSessionTooLarge = errors.New("session data too large")

	// ErrInvalidSessionID is returned when the session ID format is invalid.
	ErrInvalidSessionID = errors.New("invalid session id")
)

type Manager struct {
	store           Store
	ttl             time.Duration
	cookie          string
	cookiePath      string
	cookieDomain    string
	cleanup         time.Duration
	stopChan        chan struct{}
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
	HttpOnly        *bool
	Secure          *bool
	SameSite        http.SameSite
	MaxSessionBytes int // Maximum size in bytes of the serialized session data. 0 means unlimited.
}

func NewManager(cfg Config) *Manager {
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

	m := &Manager{
		store:           cfg.Store,
		ttl:             cfg.TTL,
		cookie:          cfg.CookieName,
		cookiePath:      cfg.CookiePath,
		cookieDomain:    cfg.CookieDomain,
		cleanup:         cfg.CleanupInterval,
		stopChan:        make(chan struct{}),
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

	go m.cleanupWorker()

	return m
}

func (m *Manager) cleanupWorker() {
	ticker := time.NewTicker(m.cleanup)
	defer ticker.Stop()

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

func (m *Manager) Close() error {
	close(m.stopChan)
	return m.store.Close()
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
	if session.ExpiresAt.Before(time.Now()) {
		return m.New(), nil
	}

	return session, nil
}

func (m *Manager) Save(w http.ResponseWriter, r *http.Request, s *Session) error {
	// Acquire lock to prevent race conditions with concurrent Session.Set/Delete calls.
	// This ensures that s.Values and s.encoded are accessed consistently.
	s.mu.Lock()
	defer s.mu.Unlock()

	if !isValidID(s.ID) {
		return ErrInvalidSessionID
	}

	s.ExpiresAt = time.Now().Add(m.ttl)

	// Check session size if limit is configured
	// Optimization: Skip encoding if the session is empty.
	// This saves allocations and CPU cycles for new/empty sessions.
	if m.maxSessionBytes > 0 && len(s.Values) > 0 {
		buf := bufferPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer PutBuffer(buf)

		if err := gob.NewEncoder(buf).Encode(s.Values); err != nil {
			return err
		}

		if buf.Len() > m.maxSessionBytes {
			return ErrSessionTooLarge
		}

		// Optimization: Store the encoded data in the session so the store doesn't have to re-encode it.
		// Note: We use the buffer's bytes directly. The Store must consume it before we return from Save.
		// Since store.Save is synchronous, this is safe, provided we clear s.encoded before returning.
		s.encoded = buf.Bytes()
	}

	err := m.store.Save(r.Context(), s)
	s.encoded = nil // Clear the cache to prevent use-after-free if buffer is reused
	if err != nil {
		return err
	}

	secure := r.TLS != nil
	if m.secure != nil {
		secure = *m.secure
	}

	http.SetCookie(w, &http.Cookie{
		Name:     m.cookie,
		Value:    s.ID,
		Path:     m.cookiePath,
		Domain:   m.cookieDomain,
		Expires:  s.ExpiresAt,
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
	oldID := s.ID
	newID, err := generateID()
	if err != nil {
		return err
	}
	s.ID = newID

	if err := m.Save(w, r, s); err != nil {
		s.ID = oldID // Restore old ID on failure
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

	return nil
}

func (m *Manager) Destroy(w http.ResponseWriter, r *http.Request, s *Session) error {
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

	if err := m.store.Delete(r.Context(), s.ID); err != nil {
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
	return &Session{
		ID:        id,
		Values:    make(map[string]any),
		CreatedAt: now,
		ExpiresAt: now.Add(m.ttl),
	}
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
