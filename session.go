package dbsession

import (
	"context"
	"sync"
	"time"
)

// Session represents a user session.
type Session struct {
	ID        string
	Values    map[string]any
	CreatedAt time.Time
	ExpiresAt time.Time
	encoded   []byte // Cache for encoded values
	mu        sync.RWMutex
}

// Get retrieves a value from the session in a thread-safe manner.
func (s *Session) Get(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.Values[key]
	return val, ok
}

// Set stores a value in the session in a thread-safe manner.
func (s *Session) Set(key string, val any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Values == nil {
		s.Values = make(map[string]any)
	}
	s.Values[key] = val
	s.encoded = nil
}

// Delete removes a value from the session in a thread-safe manner.
func (s *Session) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Values, key)
	s.encoded = nil
}

// Store defines the interface for session persistence.
type Store interface {
	// Get retrieves a session by its ID.
	Get(ctx context.Context, id string) (*Session, error)
	// Save saves a session to the store.
	Save(ctx context.Context, s *Session) error
	// Delete removes a session from the store.
	Delete(ctx context.Context, id string) error
	// Cleanup removes expired sessions from the store.
	Cleanup(ctx context.Context) error
	// Close closes the store.
	Close() error
}
