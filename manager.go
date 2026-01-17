package dbsession

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"net/http"
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
	if m.maxSessionBytes > 0 {
		buf := bufferPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufferPool.Put(buf)

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
	newID := generateID()
	s.ID = newID

	if err := m.Save(w, r, s); err != nil {
		s.ID = oldID // Restore old ID on failure
		return err
	}

	if err := m.store.Delete(r.Context(), oldID); err != nil {
		// Security: If we fail to delete the old session, we must return an error.
		// Failing to do so leaves the old session ID valid, which could be used
		// in a session fixation attack. We must "fail closed" here.
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

	if err := m.store.Delete(r.Context(), s.ID); err != nil {
		return err
	}

	// Security: Clear the session values from memory to reduce the window
	// of exposure for sensitive data.
	s.Clear()

	return nil
}

func (m *Manager) New() *Session {
	return &Session{
		ID:        generateID(),
		Values:    make(map[string]any),
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(m.ttl),
	}
}

func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // Should never happen
	}
	return hex.EncodeToString(b)
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
	for i := 0; i < len(id); i++ {
		// Lookup table is faster than multiple comparisons
		if !validIDChars[id[i]] {
			return false
		}
	}
	return true
}
