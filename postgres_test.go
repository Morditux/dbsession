package dbsession

import (
	"context"
	"os"
	"testing"
	"time"
)

// getTestPostgreSQLDSN returns the PostgreSQL DSN for testing.
// It checks the POSTGRES_TEST_DSN environment variable, or uses a default.
func getTestPostgreSQLDSN() string {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/dbsession_test?sslmode=disable"
	}
	return dsn
}

func TestPostgreSQLStore(t *testing.T) {
	dsn := getTestPostgreSQLDSN()

	store, err := NewPostgreSQLStore(dsn)
	if err != nil {
		t.Skipf("Skipping PostgreSQL test: %v (is PostgreSQL running?)", err)
	}
	defer store.Close()

	ctx := context.Background()
	s := &Session{
		ID:        "test-pg-session",
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
		ID:        "expired-pg-session",
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

func TestPostgreSQLStoreEmptySession(t *testing.T) {
	dsn := getTestPostgreSQLDSN()

	store, err := NewPostgreSQLStore(dsn)
	if err != nil {
		t.Skipf("Skipping PostgreSQL test: %v (is PostgreSQL running?)", err)
	}
	defer store.Close()

	ctx := context.Background()
	s := &Session{
		ID:        "test-pg-empty-session",
		Values:    map[string]any{},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	// Test Save empty session
	if err := store.Save(ctx, s); err != nil {
		t.Errorf("failed to save empty session: %v", err)
	}

	// Test Get empty session
	got, err := store.Get(ctx, s.ID)
	if err != nil {
		t.Errorf("failed to get empty session: %v", err)
	}
	if got == nil {
		t.Fatal("empty session not found")
	}
	if got.ID != s.ID {
		t.Errorf("expected ID %s, got %s", s.ID, got.ID)
	}
	if len(got.Values) != 0 {
		t.Errorf("expected empty values, got %v", got.Values)
	}

	// Clean up
	if err := store.Delete(ctx, s.ID); err != nil {
		t.Errorf("failed to delete session: %v", err)
	}
}

// Benchmarks

func BenchmarkPostgreSQLStore_Save(b *testing.B) {
	dsn := getTestPostgreSQLDSN()

	store, err := NewPostgreSQLStore(dsn)
	if err != nil {
		b.Skipf("Skipping PostgreSQL benchmark: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		session := &Session{
			ID:        "bench-pg-session",
			Values:    map[string]any{"key": "value", "count": i},
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		if err := store.Save(ctx, session); err != nil {
			b.Fatalf("failed to save: %v", err)
		}
	}
}

func BenchmarkPostgreSQLStore_Get(b *testing.B) {
	dsn := getTestPostgreSQLDSN()

	store, err := NewPostgreSQLStore(dsn)
	if err != nil {
		b.Skipf("Skipping PostgreSQL benchmark: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	session := &Session{
		ID:        "bench-pg-get-session",
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

func BenchmarkPostgreSQLStore_SaveParallel(b *testing.B) {
	dsn := getTestPostgreSQLDSN()

	store, err := NewPostgreSQLStore(dsn)
	if err != nil {
		b.Skipf("Skipping PostgreSQL benchmark: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

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

func BenchmarkPostgreSQLStore_GetParallel(b *testing.B) {
	dsn := getTestPostgreSQLDSN()

	store, err := NewPostgreSQLStore(dsn)
	if err != nil {
		b.Skipf("Skipping PostgreSQL benchmark: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	session := &Session{
		ID:        "bench-pg-get-parallel-session",
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
