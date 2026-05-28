package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countingStore struct {
	saveCount atomic.Int32
}

func (s *countingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *countingStore) Save(context.Context, *Auth) (string, error) {
	s.saveCount.Add(1)
	return "", nil
}

func (s *countingStore) Delete(context.Context, string) error { return nil }

func TestWithSkipPersist_DisablesUpdatePersistence(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Update(context.Background(), auth); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected 1 Save call, got %d", got)
	}

	ctxSkip := WithSkipPersist(context.Background())
	if _, err := mgr.Update(ctxSkip, auth); err != nil {
		t.Fatalf("Update(skipPersist) returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected Save call count to remain 1, got %d", got)
	}
}

func TestWithSkipPersist_DisablesRegisterPersistence(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("expected 0 Save calls, got %d", got)
	}
}

type blockingMarkResultStore struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

func newBlockingMarkResultStore() *blockingMarkResultStore {
	return &blockingMarkResultStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *blockingMarkResultStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *blockingMarkResultStore) Save(ctx context.Context, _ *Auth) (string, error) {
	s.startedOnce.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return "", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *blockingMarkResultStore) Delete(context.Context, string) error { return nil }

func (s *blockingMarkResultStore) releaseSave() {
	s.releaseOnce.Do(func() { close(s.release) })
}

func TestManagerMarkResultDoesNotWaitForPersistence(t *testing.T) {
	store := newBlockingMarkResultStore()
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "gemini",
		Metadata: map[string]any{
			"type": "gemini",
		},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	markDone := make(chan struct{})
	go func() {
		defer close(markDone)
		mgr.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: auth.Provider,
			Model:    "test-model",
			Success:  true,
		})
	}()

	select {
	case <-markDone:
	case <-time.After(200 * time.Millisecond):
		store.releaseSave()
		<-markDone
		t.Fatal("MarkResult blocked while persisting auth state")
	}

	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async auth persistence to start")
	}
	store.releaseSave()
}

type contextCheckingStore struct {
	ctxErr chan error
}

func (s *contextCheckingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *contextCheckingStore) Save(ctx context.Context, _ *Auth) (string, error) {
	s.ctxErr <- ctx.Err()
	return "", nil
}

func (s *contextCheckingStore) Delete(context.Context, string) error { return nil }

func TestManagerMarkResultPersistenceIgnoresRequestCancellation(t *testing.T) {
	store := &contextCheckingStore{ctxErr: make(chan error, 1)}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "gemini",
		Metadata: map[string]any{
			"type": "gemini",
		},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mgr.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "test-model",
		Success:  true,
	})

	select {
	case err := <-store.ctxErr:
		if err != nil {
			t.Fatalf("expected async persistence context to ignore request cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async auth persistence")
	}
}

func TestManagerMarkResultDoesNotHoldManagerLockWhilePersisting(t *testing.T) {
	store := newBlockingMarkResultStore()
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "gemini",
		Metadata: map[string]any{
			"type": "gemini",
		},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	markDone := make(chan struct{})
	go func() {
		defer close(markDone)
		mgr.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: auth.Provider,
			Model:    "test-model",
			Success:  false,
			Error:    &Error{HTTPStatus: 429, Message: "quota"},
		})
	}()

	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for MarkResult persistence to start")
	}

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		got, ok := mgr.GetByID(auth.ID)
		if !ok || got == nil {
			t.Errorf("GetByID returned ok=%v auth=%v", ok, got)
			return
		}
		if got.ModelStates["test-model"] == nil {
			t.Error("expected MarkResult state to be visible while persistence is still blocked")
		}
	}()

	select {
	case <-readDone:
	case <-time.After(500 * time.Millisecond):
		store.releaseSave()
		<-markDone
		t.Fatal("GetByID blocked while MarkResult was persisting")
	}

	store.releaseSave()
	select {
	case <-markDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for MarkResult to finish")
	}
}

type coalescingMarkResultStore struct {
	firstStarted chan struct{}
	firstRelease chan struct{}
	saved        chan *Auth
	saveCount    atomic.Int32
	startedOnce  sync.Once
	releaseOnce  sync.Once
}

func newCoalescingMarkResultStore() *coalescingMarkResultStore {
	return &coalescingMarkResultStore{
		firstStarted: make(chan struct{}),
		firstRelease: make(chan struct{}),
		saved:        make(chan *Auth, 8),
	}
}

func (s *coalescingMarkResultStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *coalescingMarkResultStore) Save(ctx context.Context, auth *Auth) (string, error) {
	count := s.saveCount.Add(1)
	s.saved <- auth.Clone()
	if count == 1 {
		s.startedOnce.Do(func() { close(s.firstStarted) })
		select {
		case <-s.firstRelease:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "", nil
}

func (s *coalescingMarkResultStore) Delete(context.Context, string) error { return nil }

func (s *coalescingMarkResultStore) releaseFirstSave() {
	s.releaseOnce.Do(func() { close(s.firstRelease) })
}

func TestManagerMarkResultCoalescesPendingPersistenceByAuthID(t *testing.T) {
	store := newCoalescingMarkResultStore()
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "gemini",
		Metadata: map[string]any{
			"type": "gemini",
		},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		mgr.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: auth.Provider,
			Model:    "test-model",
			Success:  true,
		})
	}()
	select {
	case <-firstDone:
	case <-time.After(200 * time.Millisecond):
		store.releaseFirstSave()
		<-firstDone
		t.Fatal("MarkResult blocked on the first auth persistence")
	}

	select {
	case <-store.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first async persistence to start")
	}

	for i := 0; i < 2; i++ {
		done := make(chan struct{})
		go func() {
			defer close(done)
			mgr.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    "test-model",
				Success:  true,
			})
		}()
		select {
		case <-done:
		case <-time.After(200 * time.Millisecond):
			store.releaseFirstSave()
			<-done
			t.Fatal("MarkResult blocked while a previous auth persistence was in flight")
		}
	}

	store.releaseFirstSave()

	var latest *Auth
	for deadline := time.After(time.Second); latest == nil || latest.Success < 3; {
		select {
		case latest = <-store.saved:
		case <-deadline:
			t.Fatalf("timed out waiting for coalesced latest snapshot, latest=%v", latest)
		}
	}

	if got := store.saveCount.Load(); got != 2 {
		t.Fatalf("expected 2 Save calls after coalescing, got %d", got)
	}
}

type staleOverwriteStore struct {
	blockID      string
	blockStarted chan struct{}
	blockRelease chan struct{}
	saved        chan *Auth
	blockOnce    sync.Once
	releaseOnce  sync.Once
}

func newStaleOverwriteStore(blockID string) *staleOverwriteStore {
	return &staleOverwriteStore{
		blockID:      blockID,
		blockStarted: make(chan struct{}),
		blockRelease: make(chan struct{}),
		saved:        make(chan *Auth, 16),
	}
}

func (s *staleOverwriteStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *staleOverwriteStore) Save(ctx context.Context, auth *Auth) (string, error) {
	s.saved <- auth.Clone()
	if auth.ID == s.blockID {
		s.blockOnce.Do(func() { close(s.blockStarted) })
		select {
		case <-s.blockRelease:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "", nil
}

func (s *staleOverwriteStore) Delete(context.Context, string) error { return nil }

func (s *staleOverwriteStore) releaseBlock() {
	s.releaseOnce.Do(func() { close(s.blockRelease) })
}

func TestManagerUpdateDropsOlderPendingMarkResultPersistence(t *testing.T) {
	const (
		blockerID = "auth-blocker"
		targetID  = "auth-target"
	)
	store := newStaleOverwriteStore(blockerID)
	mgr := NewManager(store, nil, nil)
	blocker := &Auth{
		ID:       blockerID,
		Provider: "gemini",
		Metadata: map[string]any{"type": "gemini"},
	}
	target := &Auth{
		ID:       targetID,
		Provider: "gemini",
		Metadata: map[string]any{"type": "gemini"},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), blocker); err != nil {
		t.Fatalf("Register blocker returned error: %v", err)
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), target); err != nil {
		t.Fatalf("Register target returned error: %v", err)
	}

	mgr.MarkResult(context.Background(), Result{
		AuthID:   blocker.ID,
		Provider: blocker.Provider,
		Model:    "test-model",
		Success:  true,
	})
	select {
	case <-store.blockStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blocker persistence to start")
	}

	mgr.MarkResult(context.Background(), Result{
		AuthID:   target.ID,
		Provider: target.Provider,
		Model:    "test-model",
		Success:  true,
	})

	disabledTarget := target.Clone()
	disabledTarget.Disabled = true
	disabledTarget.Status = StatusDisabled
	if _, err := mgr.Update(context.Background(), disabledTarget); err != nil {
		store.releaseBlock()
		t.Fatalf("Update target returned error: %v", err)
	}

	store.releaseBlock()

	var unexpected *Auth
	timeout := time.After(300 * time.Millisecond)
	for unexpected == nil {
		select {
		case saved := <-store.saved:
			if saved.ID == targetID && !saved.Disabled {
				unexpected = saved
			}
		case <-timeout:
			return
		}
	}
	t.Fatalf("older pending MarkResult persistence overwrote disabled target: %+v", unexpected)
}

type inFlightUpdateStore struct {
	firstStarted chan struct{}
	firstRelease chan struct{}
	saved        chan *Auth
	saveCount    atomic.Int32
	startedOnce  sync.Once
	releaseOnce  sync.Once
}

func newInFlightUpdateStore() *inFlightUpdateStore {
	return &inFlightUpdateStore{
		firstStarted: make(chan struct{}),
		firstRelease: make(chan struct{}),
		saved:        make(chan *Auth, 8),
	}
}

func (s *inFlightUpdateStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *inFlightUpdateStore) Save(ctx context.Context, auth *Auth) (string, error) {
	count := s.saveCount.Add(1)
	s.saved <- auth.Clone()
	if count == 1 {
		s.startedOnce.Do(func() { close(s.firstStarted) })
		select {
		case <-s.firstRelease:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "", nil
}

func (s *inFlightUpdateStore) Delete(context.Context, string) error { return nil }

func (s *inFlightUpdateStore) releaseFirstSave() {
	s.releaseOnce.Do(func() { close(s.firstRelease) })
}

func TestManagerUpdateWaitsForInFlightMarkResultPersistenceBeforeSaving(t *testing.T) {
	store := newInFlightUpdateStore()
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "gemini",
		Metadata: map[string]any{"type": "gemini"},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	mgr.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "test-model",
		Success:  true,
	})
	select {
	case <-store.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for in-flight persistence to start")
	}

	disabledAuth := auth.Clone()
	disabledAuth.Disabled = true
	disabledAuth.Status = StatusDisabled
	updateDone := make(chan error, 1)
	go func() {
		_, err := mgr.Update(context.Background(), disabledAuth)
		updateDone <- err
	}()

	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("Update returned error before in-flight persistence was released: %v", err)
		}
		t.Fatal("Update persisted while older MarkResult persistence for the same auth was still in flight")
	case <-time.After(100 * time.Millisecond):
	}

	store.releaseFirstSave()
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("Update returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Update after releasing in-flight persistence")
	}

	var latest *Auth
	for deadline := time.After(time.Second); latest == nil || !latest.Disabled; {
		select {
		case latest = <-store.saved:
		case <-deadline:
			t.Fatalf("timed out waiting for disabled auth snapshot, latest=%v", latest)
		}
	}
}

func TestManagerFlushPersistenceWaitsForPendingMarkResult(t *testing.T) {
	store := newBlockingMarkResultStore()
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "gemini",
		Metadata: map[string]any{
			"type": "gemini",
		},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	mgr.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "test-model",
		Success:  true,
	})
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async persistence to start")
	}

	flushDone := make(chan error, 1)
	go func() {
		flushDone <- mgr.FlushPersistence(context.Background())
	}()

	select {
	case err := <-flushDone:
		if err != nil {
			t.Fatalf("FlushPersistence returned error before release: %v", err)
		}
		t.Fatal("FlushPersistence returned while auth persistence was still blocked")
	case <-time.After(100 * time.Millisecond):
	}

	store.releaseSave()
	select {
	case err := <-flushDone:
		if err != nil {
			t.Fatalf("FlushPersistence returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for FlushPersistence")
	}
}

func TestManagerFlushPersistenceReturnsContextErrorWhenSaveIsBlocked(t *testing.T) {
	store := newBlockingMarkResultStore()
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "gemini",
		Metadata: map[string]any{
			"type": "gemini",
		},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	mgr.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "test-model",
		Success:  true,
	})
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async persistence to start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := mgr.FlushPersistence(ctx)
	if err == nil {
		store.releaseSave()
		t.Fatal("expected FlushPersistence to return context error")
	}
	if err != context.DeadlineExceeded {
		store.releaseSave()
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
	store.releaseSave()
}
