package dbsession

import (
	"crypto/rand"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

// FaultyReader simulates a reader that always fails.
type FaultyReader struct{}

func (f *FaultyReader) Read(p []byte) (n int, err error) {
	return 0, errors.New("simulated entropy failure")
}

func TestRegenerate_RandFailure(t *testing.T) {
	// NOTE: This test modifies global rand.Reader. Do NOT run this test in parallel (t.Parallel()).
	// It is not thread-safe with other tests that use crypto/rand.

	// Setup manager with mock store
	store := &MockStore{}
	mgr := NewManager(Config{Store: store})
	defer mgr.Close()

	// Create a session MANUALLY to avoid calling generateID (which uses rand)
	// or create it before swapping the reader.
	s := &Session{
		ID:        "valid-initial-id",
		Values:    make(map[string]any),
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)

	// Save original reader and defer restoration
	origReader := rand.Reader
	defer func() { rand.Reader = origReader }()

	// Inject faulty reader
	rand.Reader = &FaultyReader{}

	// This should NOT panic, but return an error
	err := mgr.Regenerate(w, r, s)
	if err == nil {
		t.Fatal("Expected error on random failure, got nil")
	}
	if err.Error() != "simulated entropy failure" {
		t.Errorf("Expected 'simulated entropy failure', got: %v", err)
	}
}
