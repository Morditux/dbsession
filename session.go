package dbsession

import (
	"context"
	"sync"
	"time"
)

// SessionSnapshot is a point-in-time, shallow copy of a Session. The Values
// map itself is independent, but mutable values stored inside it are shared.
// Callers must copy mutable maps, slices, pointers, or structs before mutating
// them concurrently.
type SessionSnapshot struct {
	ID        string
	Values    map[string]any
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Session represents a thread-safe user session. Its fields are private so all
// access to the value map and metadata is synchronized. A Session must not be
// copied after first use.
type Session struct {
	mu        sync.RWMutex
	opMu      sync.Mutex
	id        string
	values    map[string]any
	createdAt time.Time
	expiresAt time.Time
	encoded   []byte
}

type sessionState struct {
	id        string
	values    map[string]any
	createdAt time.Time
	expiresAt time.Time
	encoded   []byte
}

// RestoreSession creates a Session from persisted data. It is intended for
// Store implementations. The Values map is copied before it is retained.
func RestoreSession(snapshot SessionSnapshot) *Session {
	return &Session{
		id:        snapshot.ID,
		values:    cloneValues(snapshot.Values),
		createdAt: snapshot.CreatedAt,
		expiresAt: snapshot.ExpiresAt,
	}
}

// ID returns the current session identifier.
func (s *Session) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

// CreatedAt returns the session creation time.
func (s *Session) CreatedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.createdAt
}

// ExpiresAt returns the current session expiration time.
func (s *Session) ExpiresAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.expiresAt
}

// Get retrieves a value from the session. Mutable values are returned by
// reference and must not be mutated concurrently unless callers synchronize
// or copy them first.
func (s *Session) Get(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.values[key]
	return val, ok
}

// Set stores a value in the session.
func (s *Session) Set(key string, val any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = make(map[string]any)
	}
	s.values[key] = val
	s.encoded = nil
}

// Delete removes a value from the session.
func (s *Session) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	s.encoded = nil
}

// ValuesSnapshot returns a shallow copy of all session values. Mutating the
// returned map is safe, but mutable values inside it remain shared.
func (s *Session) ValuesSnapshot() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneValues(s.values)
}

// Snapshot returns a point-in-time shallow copy of the session for inspection
// or persistence by custom Store implementations.
func (s *Session) Snapshot() SessionSnapshot {
	state := s.stateSnapshot()
	return SessionSnapshot{
		ID:        state.id,
		Values:    state.values,
		CreatedAt: state.createdAt,
		ExpiresAt: state.expiresAt,
	}
}

// Clear removes all values from the session and drops cached serialization.
func (s *Session) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.values)
	s.values = nil
	s.encoded = nil
}

func (s *Session) stateSnapshot() sessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sessionState{
		id:        s.id,
		values:    cloneValues(s.values),
		createdAt: s.createdAt,
		expiresAt: s.expiresAt,
		encoded:   s.encoded,
	}
}

func (s *Session) setIdentityAndExpiry(id string, expiresAt time.Time) {
	s.mu.Lock()
	s.id = id
	s.expiresAt = expiresAt
	s.mu.Unlock()
}

func sessionFromState(state sessionState) *Session {
	return &Session{
		id:        state.id,
		values:    state.values,
		createdAt: state.createdAt,
		expiresAt: state.expiresAt,
		encoded:   state.encoded,
	}
}

func cloneValues(values map[string]any) map[string]any {
	if len(values) == 0 {
		return make(map[string]any)
	}
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

// Store defines the interface for session persistence.
type Store interface {
	// Get retrieves a session by its ID.
	Get(ctx context.Context, id string) (*Session, error)
	// Save saves a point-in-time snapshot of a session.
	Save(ctx context.Context, s *Session) error
	// Delete removes a session by its ID.
	Delete(ctx context.Context, id string) error
	// Cleanup removes expired sessions from the store.
	Cleanup(ctx context.Context) error
	// Close closes the store.
	Close() error
}
