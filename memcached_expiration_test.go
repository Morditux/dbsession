package dbsession

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"testing"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
)

func TestMemcachedExpiration(t *testing.T) {
	now := time.Unix(1_700_000_000, 123_456_789)

	tests := []struct {
		name      string
		expiresAt time.Time
		want      int32
		wantErr   bool
	}{
		{
			name:      "positive fraction rounds to one second",
			expiresAt: now.Add(time.Nanosecond),
			want:      1,
		},
		{
			name:      "one second remains relative",
			expiresAt: now.Add(time.Second),
			want:      1,
		},
		{
			name:      "partial second rounds up",
			expiresAt: now.Add(time.Second + time.Nanosecond),
			want:      2,
		},
		{
			name:      "exactly thirty days remains relative",
			expiresAt: now.Add(memcachedMaxRelativeExpiration),
			want:      int32(memcachedMaxRelativeExpiration / time.Second),
		},
		{
			name:      "thirty days plus one second becomes absolute",
			expiresAt: now.Add(memcachedMaxRelativeExpiration + time.Second),
			want:      int32(now.Add(memcachedMaxRelativeExpiration+time.Second).Unix() + 1),
		},
		{
			name:      "absolute partial second rounds up",
			expiresAt: now.Add(memcachedMaxRelativeExpiration + time.Nanosecond),
			want:      int32(now.Add(memcachedMaxRelativeExpiration+time.Nanosecond).Unix() + 1),
		},
		{
			name:      "maximum int32 timestamp",
			expiresAt: time.Unix(1<<31-1, 0),
			want:      1<<31 - 1,
		},
		{
			name:      "zero deadline is invalid",
			expiresAt: time.Time{},
			wantErr:   true,
		},
		{
			name:      "equal deadline is invalid",
			expiresAt: now,
			wantErr:   true,
		},
		{
			name:      "negative ttl is invalid",
			expiresAt: now.Add(-time.Second),
			wantErr:   true,
		},
		{
			name:      "timestamp beyond int32 is invalid",
			expiresAt: time.Unix(1<<31, 0),
			wantErr:   true,
		},
		{
			name:      "rounding beyond int32 is invalid",
			expiresAt: time.Unix(1<<31-1, 1),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := memcachedExpiration(tt.expiresAt, now)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidMemcachedExpiration) {
					t.Fatalf("expected ErrInvalidMemcachedExpiration, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expiration = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMemcachedStoreSaveRejectsInvalidFallbackTTL(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Second, time.Duration(1<<63 - 1)} {
		store := NewMemcachedStore(ttl, "127.0.0.1:11211")
		session := RestoreSession(SessionSnapshot{ID: "test-invalid-expiration"})

		err := store.Save(context.Background(), session)
		if !errors.Is(err, ErrInvalidMemcachedExpiration) {
			t.Fatalf("TTL %v: expected ErrInvalidMemcachedExpiration, got %v", ttl, err)
		}
	}
}

func TestMemcachedStoreGetRejectsAndDeletesExpiredEnvelope(t *testing.T) {
	store := NewMemcachedStore(time.Minute, "127.0.0.1:11211")
	env := sessionEnvelope{
		Values:    map[string]any{"secret": "expired"},
		CreatedAt: time.Now().Add(-2 * time.Hour),
		ExpiresAt: time.Now().Add(-time.Hour),
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(env); err != nil {
		t.Fatalf("encode expired envelope: %v", err)
	}
	const id = "test-expired-envelope"
	if err := store.client.Set(&memcache.Item{Key: id, Value: buf.Bytes()}); err != nil {
		t.Skipf("memcached is unavailable: %v", err)
	}
	t.Cleanup(func() { _ = store.client.Delete(id) })

	session, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get expired envelope: %v", err)
	}
	if session != nil {
		t.Fatal("expected an expired envelope to be rejected")
	}
	if _, err := store.client.Get(id); err != memcache.ErrCacheMiss {
		t.Fatalf("expected expired envelope to be deleted, got %v", err)
	}
}
