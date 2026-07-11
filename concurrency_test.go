package dbsession

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestRaceCondition is a regression test for concurrent Manager.Save and
// Session.Set calls. Save persists a snapshot while Set can continue safely.
func TestRaceCondition(t *testing.T) {
	// We need a store that doesn't just do nothing, but simulates saving
	// to trigger the Manager.Save logic that encodes data.
	// Actually Manager.Save encodes data IF maxSessionBytes > 0.
	store := &MockStore{}
	mgr := MustNewManager(Config{
		Store:           store,
		TTL:             time.Hour,
		MaxSessionBytes: 1024, // Enable size check which triggers encoding in Manager.Save
	})
	defer mgr.Close()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	session := mgr.New()

	var wg sync.WaitGroup
	start := make(chan struct{})
	duration := 500 * time.Millisecond

	// Goroutine 1: Modifies the session constantly
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		end := time.Now().Add(duration)
		i := 0
		for time.Now().Before(end) {
			session.Set("key", i)
			i++
		}
	}()

	// Goroutine 2: Saves the session constantly
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		end := time.Now().Add(duration)
		for time.Now().Before(end) {
			// Save snapshots and encodes the session values.
			_ = mgr.Save(w, req, session)
		}
	}()

	close(start)
	wg.Wait()
}
