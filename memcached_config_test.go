package dbsession

import (
	"testing"
	"time"
)

func TestMemcachedStore_TimeoutConfig(t *testing.T) {
	t.Run("Default Timeout", func(t *testing.T) {
		store := NewMemcachedStore(time.Hour, "localhost:11211")

		// Inspect the unexported client field
		if store.client.Timeout != 1*time.Second {
			t.Errorf("Expected default timeout of 1s, got %v", store.client.Timeout)
		}
	})

	t.Run("Custom Timeout", func(t *testing.T) {
		timeout := 5 * time.Second
		store := NewMemcachedStoreWithConfig(MemcachedConfig{
			Servers: []string{"localhost:11211"},
			TTL:     time.Hour,
			Timeout: timeout,
		})

		if store.client.Timeout != timeout {
			t.Errorf("Expected timeout of %v, got %v", timeout, store.client.Timeout)
		}
	})

	t.Run("No Timeout (Explicit 0)", func(t *testing.T) {
		store := NewMemcachedStoreWithConfig(MemcachedConfig{
			Servers: []string{"localhost:11211"},
			TTL:     time.Hour,
			Timeout: 0,
		})

		if store.client.Timeout != 0 {
			t.Errorf("Expected timeout of 0, got %v", store.client.Timeout)
		}
	})
}
