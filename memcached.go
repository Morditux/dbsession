package dbsession

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
)

const memcachedMaxRelativeExpiration = 30 * 24 * time.Hour

// ErrInvalidMemcachedExpiration is returned when a session deadline cannot be
// represented safely using Memcached's expiration field.
var ErrInvalidMemcachedExpiration = errors.New("invalid memcached expiration")

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
	defer PutReader(reader)

	if err := gob.NewDecoder(reader).Decode(&env); err != nil {
		return nil, fmt.Errorf("failed to decode session data: %w", err)
	}

	// Memcached expiration is an eviction mechanism, not the source of truth.
	// In particular, old entries written with an invalid or zero TTL may still
	// exist, so enforce the deadline stored in the envelope as well.
	if !env.ExpiresAt.IsZero() && !env.ExpiresAt.After(time.Now()) {
		_ = s.client.Delete(id)
		return nil, nil
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
	// Use the session deadline when available, otherwise derive one from the
	// store TTL. Passing zero accidentally would make the item never expire.
	now := time.Now()
	expiresAt := session.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = now.Add(s.ttl)
	}
	expiration, err := memcachedExpiration(expiresAt, now)
	if err != nil {
		return err
	}

	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer PutBuffer(buf)

	env := sessionEnvelope{
		Values:    session.Values,
		CreatedAt: session.CreatedAt,
		ExpiresAt: expiresAt,
	}
	if err := gob.NewEncoder(buf).Encode(env); err != nil {
		return fmt.Errorf("failed to encode session data: %w", err)
	}

	if s.maxSessionBytes > 0 && buf.Len() > s.maxSessionBytes {
		return ErrSessionTooLarge
	}

	err = s.client.Set(&memcache.Item{
		Key:        session.ID,
		Value:      buf.Bytes(),
		Expiration: expiration,
	})

	if err != nil {
		return fmt.Errorf("failed to save to memcached: %w", err)
	}
	return nil
}

// memcachedExpiration converts an absolute deadline to the representation
// expected by the Memcached protocol. Values up to 30 days are relative
// seconds; larger values must be absolute Unix timestamps. Positive partial
// seconds are rounded up so they can never become the special value 0, which
// means "never expire" in Memcached.
func memcachedExpiration(expiresAt, now time.Time) (int32, error) {
	if expiresAt.IsZero() || !expiresAt.After(now) {
		return 0, fmt.Errorf("%w: deadline must be in the future", ErrInvalidMemcachedExpiration)
	}

	remaining := expiresAt.Sub(now)
	if remaining <= memcachedMaxRelativeExpiration {
		seconds := (remaining + time.Second - 1) / time.Second
		return int32(seconds), nil
	}

	const maxInt32 = int64(1<<31 - 1)
	unixSeconds := expiresAt.Unix()
	if unixSeconds < 0 || unixSeconds > maxInt32 {
		return 0, fmt.Errorf("%w: absolute deadline is outside int32 Unix range", ErrInvalidMemcachedExpiration)
	}
	if expiresAt.Nanosecond() != 0 {
		if unixSeconds == maxInt32 {
			return 0, fmt.Errorf("%w: rounded absolute deadline is outside int32 Unix range", ErrInvalidMemcachedExpiration)
		}
		unixSeconds++
	}

	// An absolute timestamp at or below this threshold would be interpreted as
	// a relative TTL by Memcached. This matters mainly for synthetic/old clocks.
	if unixSeconds <= int64(memcachedMaxRelativeExpiration/time.Second) {
		return 0, fmt.Errorf("%w: absolute deadline is ambiguous to memcached", ErrInvalidMemcachedExpiration)
	}

	return int32(unixSeconds), nil
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
