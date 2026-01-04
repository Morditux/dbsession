package dbsession

import (
	"context"
	"time"
)

// Session represents a user session.
type Session struct {
	ID        string
	Values    map[string]any
	CreatedAt time.Time
	ExpiresAt time.Time
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
