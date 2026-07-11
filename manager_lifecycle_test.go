package dbsession

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type lifecycleStore struct {
	cleanupStarted chan struct{}
	cleanupRelease chan struct{}
	startOnce      sync.Once
	cleanupCalls   atomic.Int32
	closeCalls     atomic.Int32
	closeErr       error
}

func (s *lifecycleStore) Get(context.Context, string) (*Session, error) { return nil, nil }
func (s *lifecycleStore) Save(context.Context, *Session) error          { return nil }
func (s *lifecycleStore) Delete(context.Context, string) error          { return nil }

func (s *lifecycleStore) Cleanup(context.Context) error {
	s.cleanupCalls.Add(1)
	if s.cleanupStarted != nil {
		s.startOnce.Do(func() { close(s.cleanupStarted) })
	}
	if s.cleanupRelease != nil {
		<-s.cleanupRelease
	}
	return nil
}

func (s *lifecycleStore) Close() error {
	s.closeCalls.Add(1)
	return s.closeErr
}

func newLifecycleManager(store Store, leaveStoreOpen bool) *Manager {
	return NewManager(Config{
		Store:           store,
		CleanupInterval: time.Millisecond,
		LeaveStoreOpen:  leaveStoreOpen,
	})
}

func TestManagerCloseIsIdempotentAndConcurrentSafe(t *testing.T) {
	storeErr := errors.New("store close failure")
	store := &lifecycleStore{closeErr: storeErr}
	mgr := newLifecycleManager(store, false)

	const callers = 32
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- mgr.Close()
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if !errors.Is(err, storeErr) {
			t.Fatalf("Close returned %v, want %v", err, storeErr)
		}
	}
	if got := store.closeCalls.Load(); got != 1 {
		t.Fatalf("store Close called %d times, want 1", got)
	}
	if err := mgr.Close(); !errors.Is(err, storeErr) {
		t.Fatalf("repeated Close returned %v, want %v", err, storeErr)
	}
}

func TestManagerCloseWaitsForCleanupBeforeClosingStore(t *testing.T) {
	store := &lifecycleStore{
		cleanupStarted: make(chan struct{}),
		cleanupRelease: make(chan struct{}),
	}
	mgr := newLifecycleManager(store, false)

	select {
	case <-store.cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}

	closed := make(chan error, 1)
	go func() { closed <- mgr.Close() }()

	select {
	case err := <-closed:
		t.Fatalf("Close returned before cleanup completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if got := store.closeCalls.Load(); got != 0 {
		t.Fatalf("store closed %d times while cleanup was running", got)
	}

	close(store.cleanupRelease)
	if err := <-closed; err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if got := store.closeCalls.Load(); got != 1 {
		t.Fatalf("store Close called %d times, want 1", got)
	}
}

func TestManagerCloseContextCanTimeOutAndResume(t *testing.T) {
	store := &lifecycleStore{
		cleanupStarted: make(chan struct{}),
		cleanupRelease: make(chan struct{}),
	}
	mgr := newLifecycleManager(store, false)

	select {
	case <-store.cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := mgr.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext returned %v, want deadline exceeded", err)
	}
	if got := store.closeCalls.Load(); got != 0 {
		t.Fatalf("store closed %d times after shutdown timeout", got)
	}

	close(store.cleanupRelease)
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close after releasing cleanup failed: %v", err)
	}
	if got := store.closeCalls.Load(); got != 1 {
		t.Fatalf("store Close called %d times, want 1", got)
	}
}

func TestManagersCanShareCallerOwnedStore(t *testing.T) {
	store := &lifecycleStore{}
	first := newLifecycleManager(store, true)
	second := newLifecycleManager(store, true)

	if err := first.Close(); err != nil {
		t.Fatalf("first manager Close failed: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second manager Close failed: %v", err)
	}
	if got := store.closeCalls.Load(); got != 0 {
		t.Fatalf("shared store closed by managers %d times, want 0", got)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("caller-owned store Close failed: %v", err)
	}
	if got := store.closeCalls.Load(); got != 1 {
		t.Fatalf("caller closed shared store %d times, want 1", got)
	}
}
