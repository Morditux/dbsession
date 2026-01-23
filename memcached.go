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
}

// NewMemcachedStore creates a new MemcachedStore.
func NewMemcachedStore(ttl time.Duration, servers ...string) *MemcachedStore {
	return NewMemcachedStoreWithConfig(MemcachedConfig{
		Servers: servers,
		TTL:     ttl,
	})
}

// NewMemcachedStoreWithConfig creates a new MemcachedStore with custom configuration.
func NewMemcachedStoreWithConfig(cfg MemcachedConfig) *MemcachedStore {
	return &MemcachedStore{
		client:          memcache.New(cfg.Servers...),
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
	var expiration int32
	if !session.ExpiresAt.IsZero() {
		diff := time.Until(session.ExpiresAt)
		if diff <= 0 {
			return nil // Already expired
		}
		expiration = int32(diff.Seconds())
	} else {
		expiration = int32(s.ttl.Seconds())
	}

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
