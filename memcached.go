package dbsession

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
)

// MemcachedStore implements the Store interface using Memcached.
type MemcachedStore struct {
	client          *memcache.Client
	ttl             time.Duration
	maxSessionBytes int
}

// MemcachedConfig holds configuration for the Memcached store.
type MemcachedConfig struct {
	Servers         []string
	TTL             time.Duration
	MaxSessionBytes int
	Timeout         time.Duration // Timeout for Memcached operations. Defaults to 0 (no timeout) if not set.
}

// NewMemcachedStore creates a new MemcachedStore.
func NewMemcachedStore(ttl time.Duration, servers ...string) *MemcachedStore {
	return NewMemcachedStoreWithConfig(MemcachedConfig{
		Servers: servers,
		TTL:     ttl,
		// Security: Set a default timeout to prevent indefinite hanging if Memcached is down.
		// 1 second is usually sufficient for local/network cache.
		Timeout: 1 * time.Second,
	})
}

// NewMemcachedStoreWithConfig creates a new MemcachedStore with custom configuration.
func NewMemcachedStoreWithConfig(cfg MemcachedConfig) *MemcachedStore {
	client := memcache.New(cfg.Servers...)
	client.Timeout = cfg.Timeout

	return &MemcachedStore{
		client:          client,
		ttl:             cfg.TTL,
		maxSessionBytes: cfg.MaxSessionBytes,
	}
}

type sessionEnvelope struct {
	Values    map[string]any
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Get retrieves a session from Memcached.
func (s *MemcachedStore) Get(ctx context.Context, id string) (*Session, error) {
	item, err := s.client.Get(id)
	if err == memcache.ErrCacheMiss {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get from memcached: %w", err)
	}

	if s.maxSessionBytes > 0 && len(item.Value) > s.maxSessionBytes {
		return nil, ErrSessionTooLarge
	}

	var env sessionEnvelope

	reader := readerPool.Get().(*bytes.Reader)
	reader.Reset(item.Value)
	defer readerPool.Put(reader)

	if err := gob.NewDecoder(reader).Decode(&env); err != nil {
		return nil, fmt.Errorf("failed to decode session data: %w", err)
	}

	if env.Values == nil {
		env.Values = make(map[string]any)
	}

	return &Session{
		ID:        id,
		Values:    env.Values,
		CreatedAt: env.CreatedAt,
		ExpiresAt: env.ExpiresAt,
	}, nil
}

// Save stores a session in Memcached.
func (s *MemcachedStore) Save(ctx context.Context, session *Session) error {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer PutBuffer(buf)

	env := sessionEnvelope{
		Values:    session.Values,
		CreatedAt: session.CreatedAt,
		ExpiresAt: session.ExpiresAt,
	}
	if err := gob.NewEncoder(buf).Encode(env); err != nil {
		return fmt.Errorf("failed to encode session data: %w", err)
	}

	if s.maxSessionBytes > 0 && buf.Len() > s.maxSessionBytes {
		return ErrSessionTooLarge
	}

	// Use specified TTL or calculate from session.ExpiresAt
	// Also check if we need to skip saving if already expired.
	if !session.ExpiresAt.IsZero() && time.Until(session.ExpiresAt) <= 0 {
		return nil // Already expired
	}

	expiration := calculateMemcachedExpiration(time.Now(), session.ExpiresAt, s.ttl)

	err := s.client.Set(&memcache.Item{
		Key:        session.ID,
		Value:      buf.Bytes(),
		Expiration: expiration,
	})

	if err != nil {
		return fmt.Errorf("failed to save to memcached: %w", err)
	}
	return nil
}

func init() {
	gob.Register(sessionEnvelope{})
}

// Delete removes a session from Memcached.
func (s *MemcachedStore) Delete(ctx context.Context, id string) error {
	err := s.client.Delete(id)
	if err != nil && err != memcache.ErrCacheMiss {
		return fmt.Errorf("failed to delete from memcached: %w", err)
	}
	return nil
}

// Cleanup is a no-op for Memcached as it handles expiration automatically.
func (s *MemcachedStore) Cleanup(ctx context.Context) error {
	return nil
}

// Close is a no-op for Memcached client.
func (s *MemcachedStore) Close() error {
	return nil
}

// calculateMemcachedExpiration calculates the expiration value for Memcached.
// Memcached treats values > 30 days (60*60*24*30 seconds) as absolute Unix timestamps.
// Values <= 30 days are treated as a delta from the current time.
func calculateMemcachedExpiration(now time.Time, expiresAt time.Time, ttl time.Duration) int32 {
	const maxDelta = 30 * 24 * 60 * 60 // 30 days in seconds

	var duration time.Duration
	if !expiresAt.IsZero() {
		duration = expiresAt.Sub(now)
	} else {
		duration = ttl
	}

	// If duration exceeds 30 days, we MUST use absolute Unix timestamp.
	// Otherwise, Memcached will interpret a large delta as a timestamp in 1970 (expired).
	if duration > maxDelta*time.Second {
		if !expiresAt.IsZero() {
			return int32(expiresAt.Unix())
		}
		return int32(now.Add(ttl).Unix())
	}

	// For short durations, use delta seconds.
	// Ensure we don't return negative values.
	if duration < 0 {
		return 0
	}
	return int32(duration.Seconds())
}
