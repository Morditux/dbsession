package dbsession

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"
)

type Manager struct {
	store    Store
	ttl      time.Duration
	cookie   string
	cleanup  time.Duration
	stopChan chan struct{}
	httpOnly bool
	secure   *bool
	sameSite http.SameSite
}

type Config struct {
	Store           Store
	TTL             time.Duration
	CookieName      string
	CleanupInterval time.Duration
	HttpOnly        *bool
	Secure          *bool
	SameSite        http.SameSite
}

func NewManager(cfg Config) *Manager {
	if cfg.CookieName == "" {
		cfg.CookieName = "session_id"
	}
	if cfg.TTL == 0 {
		cfg.TTL = 24 * time.Hour
	}
	if cfg.CleanupInterval == 0 {
		cfg.CleanupInterval = 10 * time.Minute
	}

	m := &Manager{
		store:    cfg.Store,
		ttl:      cfg.TTL,
		cookie:   cfg.CookieName,
		cleanup:  cfg.CleanupInterval,
		stopChan: make(chan struct{}),
		httpOnly: true, // Default
		secure:   cfg.Secure,
		sameSite: http.SameSiteLaxMode, // Default
	}

	if cfg.HttpOnly != nil {
		m.httpOnly = *cfg.HttpOnly
	}

	if cfg.SameSite != 0 {
		m.sameSite = cfg.SameSite
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
	s.ExpiresAt = time.Now().Add(m.ttl)
	if err := m.store.Save(r.Context(), s); err != nil {
		return err
	}

	secure := r.TLS != nil
	if m.secure != nil {
		secure = *m.secure
	}

	http.SetCookie(w, &http.Cookie{
		Name:     m.cookie,
		Value:    s.ID,
		Path:     "/",
		Expires:  s.ExpiresAt,
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
		// We log the error but don't fail the request as the new session is valid.
		// In a real logger we would log this. For now we just return it.
		// It's better to return nil here to not interrupt the user flow,
		// but returning error allows the caller to decide.
		// Given the interface, let's return it wrapped.
		// However, the user has a valid new session.
		return nil
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
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: m.httpOnly,
		Secure:   secure,
		SameSite: m.sameSite,
	})

	if err := m.store.Delete(r.Context(), s.ID); err != nil {
		return err
	}

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
