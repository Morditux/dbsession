package dbsession

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityConfig(t *testing.T) {
	// Mock store
	store := &MockStore{}

	t.Run("Default Security Settings", func(t *testing.T) {
		mgr := NewManager(Config{Store: store})
		defer mgr.Close()

		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/", nil)
		s := mgr.New()

		if err := mgr.Save(w, r, s); err != nil {
			t.Fatalf("Save failed: %v", err)
		}

		cookies := w.Result().Cookies()
		if len(cookies) == 0 {
			t.Fatal("No cookie set")
		}
		c := cookies[0]

		if !c.HttpOnly {
			t.Error("HttpOnly should be true by default")
		}
		if c.SameSite != http.SameSiteLaxMode {
			t.Errorf("SameSite should be Lax by default, got %v", c.SameSite)
		}
		if c.Secure {
			t.Error("Secure should be false for non-TLS request by default")
		}
	})

	t.Run("Custom Security Settings", func(t *testing.T) {
		httpOnly := false
		secure := true
		mgr := NewManager(Config{
			Store:    store,
			HttpOnly: &httpOnly,
			Secure:   &secure,
			SameSite: http.SameSiteStrictMode,
		})
		defer mgr.Close()

		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/", nil) // Non-TLS request
		s := mgr.New()

		if err := mgr.Save(w, r, s); err != nil {
			t.Fatalf("Save failed: %v", err)
		}

		cookies := w.Result().Cookies()
		c := cookies[0]

		if c.HttpOnly {
			t.Error("HttpOnly should be false")
		}
		if c.SameSite != http.SameSiteStrictMode {
			t.Errorf("SameSite should be Strict, got %v", c.SameSite)
		}
		if !c.Secure {
			t.Error("Secure should be forced to true")
		}
	})

	t.Run("Destroy Respects Secure Setting", func(t *testing.T) {
		secure := true
		mgr := NewManager(Config{
			Store:  store,
			Secure: &secure,
		})
		defer mgr.Close()

		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/", nil)
		s := mgr.New()

		// Save first to verify Secure is set on creation
		if err := mgr.Save(w, r, s); err != nil {
			t.Fatalf("Save failed: %v", err)
		}
		c := w.Result().Cookies()[0]
		if !c.Secure {
			t.Fatal("Expected Secure cookie on Save")
		}

		// Now Destroy
		w2 := httptest.NewRecorder()
		if err := mgr.Destroy(w2, r, s); err != nil {
			t.Fatalf("Destroy failed: %v", err)
		}

		cookies := w2.Result().Cookies()
		if len(cookies) == 0 {
			t.Fatal("No cookie set on Destroy")
		}
		c2 := cookies[0]
		if !c2.Secure {
			t.Error("Secure should be true on deletion cookie")
		}
	})

	t.Run("Max Session Size Limit", func(t *testing.T) {
		limit := 10 // Very small limit (bytes)
		mgr := NewManager(Config{
			Store:           store,
			MaxSessionBytes: limit,
		})
		defer mgr.Close()

		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/", nil)
		s := mgr.New()

		// Add data that exceeds the limit
		// "key" + "val" + map overhead > 10 bytes
		s.Set("key", strings.Repeat("a", 20))

		err := mgr.Save(w, r, s)
		if err == nil {
			t.Error("Expected error for large session data, got nil")
		} else if err != ErrSessionTooLarge {
			t.Errorf("Expected ErrSessionTooLarge, got: %v", err)
		}
	})
}

// MockStore to avoid needing DB/Memcached for this test
type MockStore struct{}

func (m *MockStore) Get(ctx context.Context, id string) (*Session, error) { return nil, nil }
func (m *MockStore) Save(ctx context.Context, s *Session) error           { return nil }
func (m *MockStore) Delete(ctx context.Context, id string) error          { return nil }
func (m *MockStore) Cleanup(ctx context.Context) error                    { return nil }
func (m *MockStore) Close() error                                         { return nil }

type MockStoreFailDelete struct {
	MockStore
}

func (m *MockStoreFailDelete) Delete(ctx context.Context, id string) error {
	return context.DeadlineExceeded // Simulate a failure
}

func TestRegenerate_FailSecure(t *testing.T) {
	store := &MockStoreFailDelete{}
	mgr := NewManager(Config{Store: store})
	defer mgr.Close()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	s := mgr.New()
	s.ID = "old-id"

	// Regenerate should fail if Delete fails
	err := mgr.Regenerate(w, r, s)
	if err == nil {
		t.Error("Expected error when backend Delete fails, got nil (Fail Open)")
	}
}
