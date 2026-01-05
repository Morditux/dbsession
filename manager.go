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

func (m *Manager) Destroy(w http.ResponseWriter, r *http.Request, s *Session) error {
	if err := m.store.Delete(r.Context(), s.ID); err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     m.cookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: m.httpOnly,
		SameSite: m.sameSite,
	})

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
