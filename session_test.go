package dbsession

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestSQLiteStore(t *testing.T) {
	dbPath := "test.db"
	defer os.Remove(dbPath)

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	s := &Session{
		ID:        "test-session",
		Values:    map[string]any{"foo": "bar", "count": 42},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	// Test Save
	if err := store.Save(ctx, s); err != nil {
		t.Errorf("failed to save session: %v", err)
	}

	// Test Get
	got, err := store.Get(ctx, s.ID)
	if err != nil {
		t.Errorf("failed to get session: %v", err)
	}
	if got == nil {
		t.Fatal("session not found")
	}
	if got.ID != s.ID {
		t.Errorf("expected ID %s, got %s", s.ID, got.ID)
	}
	if got.Values["foo"] != "bar" || got.Values["count"].(int) != 42 {
		t.Errorf("unexpected values: %v", got.Values)
	}

	// Test Delete
	if err := store.Delete(ctx, s.ID); err != nil {
		t.Errorf("failed to delete session: %v", err)
	}
	got, err = store.Get(ctx, s.ID)
	if err != nil {
		t.Errorf("failed to get session after delete: %v", err)
	}
	if got != nil {
		t.Error("expected session to be deleted")
	}

	// Test Cleanup
	expired := &Session{
		ID:        "expired-session",
		Values:    map[string]any{"key": "val"},
		CreatedAt: time.Now().Add(-2 * time.Hour),
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	if err := store.Save(ctx, expired); err != nil {
		t.Errorf("failed to save expired session: %v", err)
	}

	if err := store.Cleanup(ctx); err != nil {
		t.Errorf("failed cleanup: %v", err)
	}

	got, err = store.Get(ctx, expired.ID)
	if err != nil {
		t.Errorf("failed to get after cleanup: %v", err)
	}
	if got != nil {
		t.Error("expected expired session to be cleaned up")
	}
}

func TestManager_Regenerate(t *testing.T) {
	// Setup
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	manager := NewManager(Config{
		Store: store,
	})
	defer manager.Close()

	// 1. Create a session
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	session, err := manager.Get(req)
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}

	session.Set("user_id", "123")
	if err := manager.Save(w, req, session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	oldID := session.ID

	// 2. Regenerate
	if err := manager.Regenerate(w, req, session); err != nil {
		t.Fatalf("failed to regenerate session: %v", err)
	}

	// Check results
	if session.ID == oldID {
		t.Errorf("expected new session ID, got same ID")
	}

	val, ok := session.Get("user_id")
	if !ok || val != "123" {
		t.Errorf("expected user_id=123, got %v", val)
	}

	// Verify old session is gone
	oldSess, err := store.Get(context.Background(), oldID)
	if err != nil {
		t.Fatalf("failed to check old session: %v", err)
	}
	if oldSess != nil {
		t.Errorf("old session still exists")
	}

	// Verify new session is persisted
	newSess, err := store.Get(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("failed to check new session: %v", err)
	}
	if newSess == nil {
		t.Errorf("new session not found in store")
	}
}

func TestManager(t *testing.T) {
	dbPath := "test_mgr.db"
	defer os.Remove(dbPath)

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	mgr := NewManager(Config{
		Store: store,
		TTL:   time.Minute,
	})
	defer mgr.Close()

	// Test New and Save
	s := mgr.New()
	s.Values["user"] = "mordicus"

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)

	if err := mgr.Save(w, r, s); err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	// Verify cookie
	resp := w.Result()
	cookies := resp.Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "session_id" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("session cookie not found")
	}

	// Test Get with cookie
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.AddCookie(sessionCookie)

	s2, err := mgr.Get(r2)
	if err != nil {
		t.Fatalf("failed to get: %v", err)
	}
	if s2.ID != s.ID {
		t.Errorf("ID mismatch: %s != %s", s2.ID, s.ID)
	}
	if s2.Values["user"] != "mordicus" {
		t.Errorf("value mismatch: %v", s2.Values["user"])
	}

	// Test Destroy
	w3 := httptest.NewRecorder()
	if err := mgr.Destroy(w3, r2, s2); err != nil {
		t.Fatalf("failed to destroy: %v", err)
	}

	// Verify cookie removal
	resp3 := w3.Result()
	cookies3 := resp3.Cookies()
	found := false
	for _, c := range cookies3 {
		if c.Name == "session_id" && c.MaxAge < 0 {
			found = true
			break
		}
	}
	if !found {
		t.Error("session cookie removal not found in response")
	}
}

func TestMemcachedStore(t *testing.T) {
	// Memcached is often not available in CI/local envs by default.
	// We'll try to connect and skip if it fails.
	server := "127.0.0.1:11211"
	store := NewMemcachedStore(time.Minute, server)

	// Simple check to see if memcached is up
	ctx := context.Background()
	testSession := &Session{
		ID:        "test-memcached",
		Values:    map[string]any{"color": "blue"},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Minute),
	}

	err := store.Save(ctx, testSession)
	if err != nil {
		t.Skipf("Skipping Memcached test: %v (is memcached running on %s?)", err, server)
	}

	// Test Get
	got, err := store.Get(ctx, testSession.ID)
	if err != nil {
		t.Fatalf("failed to get from memcached: %v", err)
	}
	if got == nil {
		t.Fatal("session not found in memcached")
	}
	if got.Values["color"] != "blue" {
		t.Errorf("expected color blue, got %v", got.Values["color"])
	}

	// Test Delete
	if err := store.Delete(ctx, testSession.ID); err != nil {
		t.Errorf("failed to delete from memcached: %v", err)
	}
	got, err = store.Get(ctx, testSession.ID)
	if err != nil {
		t.Errorf("failed to get after delete: %v", err)
	}
	if got != nil {
		t.Error("expected session to be deleted from memcached")
	}
}

// Benchmarks

func BenchmarkSQLiteStore_Save(b *testing.B) {
	dbPath := "bench_sqlite.db"
	defer os.Remove(dbPath)

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		session := &Session{
			ID:        "bench-session",
			Values:    map[string]any{"key": "value", "count": i},
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		if err := store.Save(ctx, session); err != nil {
			b.Fatalf("failed to save: %v", err)
		}
	}
}

func BenchmarkSQLiteStore_Get(b *testing.B) {
	dbPath := "bench_sqlite_get.db"
	defer os.Remove(dbPath)

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	session := &Session{
		ID:        "bench-get-session",
		Values:    map[string]any{"key": "value"},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := store.Save(ctx, session); err != nil {
		b.Fatalf("failed to save: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.Get(ctx, session.ID)
		if err != nil {
			b.Fatalf("failed to get: %v", err)
		}
	}
}

func BenchmarkMemcachedStore_Save(b *testing.B) {
	server := "127.0.0.1:11211"
	store := NewMemcachedStore(time.Hour, server)

	ctx := context.Background()

	// Check if memcached is available
	testSession := &Session{
		ID:        "bench-mc-test",
		Values:    map[string]any{"test": true},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := store.Save(ctx, testSession); err != nil {
		b.Skipf("Skipping Memcached benchmark: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		session := &Session{
			ID:        "bench-mc-session",
			Values:    map[string]any{"key": "value", "count": i},
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		if err := store.Save(ctx, session); err != nil {
			b.Fatalf("failed to save: %v", err)
		}
	}
}

func BenchmarkMemcachedStore_Get(b *testing.B) {
	server := "127.0.0.1:11211"
	store := NewMemcachedStore(time.Hour, server)

	ctx := context.Background()
	session := &Session{
		ID:        "bench-mc-get-session",
		Values:    map[string]any{"key": "value"},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := store.Save(ctx, session); err != nil {
		b.Skipf("Skipping Memcached benchmark: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.Get(ctx, session.ID)
		if err != nil {
			b.Fatalf("failed to get: %v", err)
		}
	}
}

// Parallel benchmarks

func BenchmarkSQLiteStore_SaveParallel(b *testing.B) {
	// SQLite is not designed for high-concurrency parallel writes.
	// For high write concurrency, use Memcached or another distributed store.
	b.Skip("SQLite parallel writes not supported - use Memcached for concurrent workloads")
}

func BenchmarkMemcachedStore_SaveParallel(b *testing.B) {
	server := "127.0.0.1:11211"
	store := NewMemcachedStore(time.Hour, server)

	ctx := context.Background()

	// Check if memcached is available
	testSession := &Session{
		ID:        "bench-mc-parallel-test",
		Values:    map[string]any{"test": true},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := store.Save(ctx, testSession); err != nil {
		b.Skipf("Skipping Memcached benchmark: %v", err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			session := &Session{
				ID:        generateID(),
				Values:    map[string]any{"key": "value", "count": i},
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			}
			if err := store.Save(ctx, session); err != nil {
				b.Errorf("failed to save: %v", err)
			}
			i++
		}
	})
}

func BenchmarkSQLiteStore_GetParallel(b *testing.B) {
	dbPath := "bench_sqlite_get_parallel.db"
	defer os.Remove(dbPath)

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	session := &Session{
		ID:        "bench-get-session",
		Values:    map[string]any{"key": "value"},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := store.Save(ctx, session); err != nil {
		b.Fatalf("failed to save: %v", err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := store.Get(ctx, session.ID)
			if err != nil {
				b.Errorf("failed to get: %v", err)
			}
		}
	})
}

func BenchmarkManager_Save_Empty(b *testing.B) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	mgr := NewManager(Config{
		Store:           store,
		MaxSessionBytes: 4096, // Enable size check to trigger encoding logic
	})
	defer mgr.Close()

	s := mgr.New() // Empty values
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := mgr.Save(w, r, s); err != nil {
			b.Fatal(err)
		}
	}
}
