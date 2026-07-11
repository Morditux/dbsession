package dbsession

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestSessionValuesSnapshotIsIndependent(t *testing.T) {
	s := RestoreSession(SessionSnapshot{
		ID:     "0123456789abcdef0123456789abcdef",
		Values: map[string]any{"role": "user"},
	})

	values := s.ValuesSnapshot()
	values["role"] = "admin"
	values["new"] = true

	role, _ := s.Get("role")
	if role != "user" {
		t.Fatalf("snapshot mutation changed session role to %v", role)
	}
	if _, ok := s.Get("new"); ok {
		t.Fatal("snapshot mutation added a value to the session")
	}
}

type snapshotBlockingStore struct {
	MockStore
	started   chan struct{}
	release   chan struct{}
	mu        sync.Mutex
	persisted SessionSnapshot
}

func (s *snapshotBlockingStore) Save(_ context.Context, session *Session) error {
	snapshot := session.Snapshot()
	close(s.started)
	<-s.release
	s.mu.Lock()
	s.persisted = snapshot
	s.mu.Unlock()
	return nil
}

func TestManagerSavePersistsImmutableMapSnapshot(t *testing.T) {
	store := &snapshotBlockingStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	mgr := MustNewManager(Config{Store: store, DisableCleanup: true})
	defer mgr.Close()
	s := mgr.New()
	s.Set("before", true)

	done := make(chan error, 1)
	go func() {
		done <- mgr.Save(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), s)
	}()
	<-store.started
	s.Set("after", true)
	close(store.release)
	if err := <-done; err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	store.mu.Lock()
	persisted := store.persisted
	store.mu.Unlock()
	if persisted.Values["before"] != true {
		t.Fatal("snapshot did not persist the original value")
	}
	if _, ok := persisted.Values["after"]; ok {
		t.Fatal("snapshot included a value written after persistence started")
	}
	if after, _ := s.Get("after"); after != true {
		t.Fatal("concurrent Set was lost from the live session")
	}
}

func TestSessionConcurrentRegenerateSaveAndSet(t *testing.T) {
	mgr := MustNewManager(Config{Store: &MockStore{}, DisableCleanup: true})
	defer mgr.Close()
	s := mgr.New()

	const iterations = 100
	var wg sync.WaitGroup
	errs := make(chan error, iterations*2)
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			err := mgr.Regenerate(
				httptest.NewRecorder(),
				httptest.NewRequest("POST", "/rotate", nil),
				s,
			)
			if err != nil {
				errs <- err
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if err := mgr.Save(
				httptest.NewRecorder(),
				httptest.NewRequest("POST", "/save", nil),
				s,
			); err != nil {
				errs <- err
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			s.Set("counter", i)
			_, _ = s.Get("counter")
			_ = s.ID()
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent session operation failed: %v", err)
	}
}

func TestSessionConcurrentDestroyGetAndSet(t *testing.T) {
	mgr := MustNewManager(Config{Store: &MockStore{}, DisableCleanup: true})
	defer mgr.Close()
	s := mgr.New()

	const iterations = 100
	var wg sync.WaitGroup
	errs := make(chan error, iterations)
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if err := mgr.Destroy(
				httptest.NewRecorder(),
				httptest.NewRequest("POST", "/destroy", nil),
				s,
			); err != nil {
				errs <- err
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			s.Set("value", i)
			s.Delete("other")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, _ = s.Get("value")
			_ = s.ValuesSnapshot()
			_ = s.ExpiresAt()
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Destroy failed: %v", err)
	}
}
