package dbsession

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestStore_MaxSessionBytes(t *testing.T) {
	dbPath := "test_limit.db"
	defer os.Remove(dbPath)

	// 1. Create a store WITHOUT limit to save a large session
	unlimitedStore, err := NewSQLiteStoreWithConfig(SQLiteConfig{
		DSN: dbPath,
	})
	if err != nil {
		t.Fatalf("failed to create unlimited store: %v", err)
	}
	// We keep unlimitedStore open or close it?
	// SQLite supports multiple connections.
	// But let's close it to be clean, although defer handles it.

	ctx := context.Background()
	largeData := make([]byte, 1024) // 1KB of data
	// Fill with some data
	for i := range largeData {
		largeData[i] = 'A'
	}

	session := &Session{
		ID:        "large-session",
		Values:    map[string]any{"data": string(largeData)},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	if err := unlimitedStore.Save(ctx, session); err != nil {
		t.Fatalf("failed to save large session: %v", err)
	}
	unlimitedStore.Close()

	// 2. Create a store WITH limit (smaller than session)
	// The encoded size of map{"data": 1KB} will be > 1024 bytes (overhead).
	// Let's set limit to 500 bytes.
	limitedStore, err := NewSQLiteStoreWithConfig(SQLiteConfig{
		DSN:             dbPath,
		MaxSessionBytes: 500,
	})
	if err != nil {
		t.Fatalf("failed to create limited store: %v", err)
	}
	defer limitedStore.Close()

	// 3. Attempt to Get the session
	_, err = limitedStore.Get(ctx, session.ID)
	if err == nil {
		t.Fatal("expected error when getting too large session, got nil")
	}

	if !errors.Is(err, ErrSessionTooLarge) {
		t.Errorf("expected ErrSessionTooLarge, got: %v", err)
	}

	// 4. Attempt to Save a large session directly using limited store
	// Ensure session.encoded is nil (it should be, as we haven't used Manager)
	session.encoded = nil
	if err := limitedStore.Save(ctx, session); err == nil {
		t.Fatal("expected error when saving too large session, got nil")
	} else if !errors.Is(err, ErrSessionTooLarge) {
		t.Errorf("expected ErrSessionTooLarge on Save, got: %v", err)
	}
}
