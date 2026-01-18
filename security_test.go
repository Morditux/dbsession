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
		if c.Path != "/" {
			t.Errorf("Path should be / by default, got %s", c.Path)
		}
		if c.MaxAge <= 0 {
			t.Errorf("MaxAge should be set to positive integer (TTL), got %d", c.MaxAge)
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

	t.Run("Cookie Scope", func(t *testing.T) {
		mgr := NewManager(Config{
			Store:        store,
			CookiePath:   "/app",
			CookieDomain: "example.com",
		})
		defer mgr.Close()

		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/app/dashboard", nil)
		s := mgr.New()

		if err := mgr.Save(w, r, s); err != nil {
			t.Fatalf("Save failed: %v", err)
		}

		cookies := w.Result().Cookies()
		c := cookies[0]

		if c.Path != "/app" {
			t.Errorf("Path should be /app, got %s", c.Path)
		}
		if c.Domain != "example.com" {
			t.Errorf("Domain should be example.com, got %s", c.Domain)
		}

		// Test Destroy uses correct scope
		w2 := httptest.NewRecorder()
		if err := mgr.Destroy(w2, r, s); err != nil {
			t.Fatalf("Destroy failed: %v", err)
		}
		c2 := w2.Result().Cookies()[0]
		if c2.Path != "/app" {
			t.Errorf("Destroy Path should be /app, got %s", c2.Path)
		}
		if c2.Domain != "example.com" {
			t.Errorf("Destroy Domain should be example.com, got %s", c2.Domain)
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

	t.Run("SameSite=None Enforces Secure", func(t *testing.T) {
		// Case: SameSite=None, Secure=nil (default, auto-detect)
		// Since request is HTTP, auto-detect would usually result in Secure=false.
		// But because SameSite=None, we MUST enforce Secure=true.
		mgr := NewManager(Config{
			Store:    store,
			SameSite: http.SameSiteNoneMode,
		})
		defer mgr.Close()

		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/", nil) // Non-TLS
		s := mgr.New()

		if err := mgr.Save(w, r, s); err != nil {
			t.Fatalf("Save failed: %v", err)
		}

		cookies := w.Result().Cookies()
		c := cookies[0]

		if c.SameSite != http.SameSiteNoneMode {
			t.Errorf("Expected SameSite=None, got %v", c.SameSite)
		}
		if !c.Secure {
			t.Error("Expected Secure=true when SameSite=None, but got false")
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

	// Security: Verify that we fail closed by clearing the cookie.
	// If we leave the cookie set to the NEW ID, the user is effectively logged in
	// even though the security rotation failed.
	cookies := w.Result().Cookies()
	foundClear := false
	for _, c := range cookies {
		if c.Name == "session_id" && c.MaxAge < 0 {
			foundClear = true
		}
	}

	if !foundClear {
		t.Error("Expected session cookie to be cleared (MaxAge < 0) when Regenerate fails, but it remained valid")
	}
}

func TestSave_ValidatesSessionID(t *testing.T) {
	// Mock store
	store := &MockStore{}
	mgr := NewManager(Config{Store: store})
	defer mgr.Close()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)

	t.Run("Invalid ID rejected", func(t *testing.T) {
		s := mgr.New()
		s.ID = "bad-id" // Not 32 hex chars

		err := mgr.Save(w, r, s)
		if err == nil {
			t.Fatal("Expected error when saving session with invalid ID, got nil")
		}
		if err != ErrInvalidSessionID {
			t.Errorf("Expected ErrInvalidSessionID, got %v", err)
		}
	})

	t.Run("Valid ID accepted", func(t *testing.T) {
		s := mgr.New() // Generates valid ID

		err := mgr.Save(w, r, s)
		if err != nil {
			t.Fatalf("Expected no error for valid ID, got %v", err)
		}
	})
}

func TestDestroy_ClearsMemory(t *testing.T) {
	store := &MockStore{}
	mgr := NewManager(Config{Store: store})
	defer mgr.Close()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	s := mgr.New()

	s.Set("secret", "sensitive-data")

	if err := mgr.Destroy(w, r, s); err != nil {
		t.Fatalf("Destroy failed: %v", err)
	}

	// Verify memory is cleared
	val, ok := s.Get("secret")
	if ok || val != nil {
		t.Error("Expected session values to be cleared after Destroy, but 'secret' is still present")
	}

	// Verify internal map is nil or empty
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.Values) > 0 {
		t.Errorf("Expected Values map to be empty/nil, got len %d", len(s.Values))
	}
}
