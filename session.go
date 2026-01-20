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
	val, ok := s.Values[key]
	s.mu.RUnlock()
	return val, ok
}

// Set stores a value in the session in a thread-safe manner.
func (s *Session) Set(key string, val any) {
	s.mu.Lock()
	if s.Values == nil {
		s.Values = make(map[string]any)
	}
	s.Values[key] = val
	s.encoded = nil
	s.mu.Unlock()
}

// Delete removes a value from the session in a thread-safe manner.
func (s *Session) Delete(key string) {
	s.mu.Lock()
	delete(s.Values, key)
	s.encoded = nil
	s.mu.Unlock()
}

// Clear removes all values from the session and clears the encoded cache.
// This is used to wipe sensitive data from memory when destroying a session.
func (s *Session) Clear() {
	s.mu.Lock()
	s.Values = nil
	s.encoded = nil
	s.mu.Unlock()
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
