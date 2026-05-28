package cliproxy

import (
	"context"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type shutdownBlockingAuthStore struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

func newShutdownBlockingAuthStore() *shutdownBlockingAuthStore {
	return &shutdownBlockingAuthStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *shutdownBlockingAuthStore) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }

func (s *shutdownBlockingAuthStore) Save(ctx context.Context, _ *coreauth.Auth) (string, error) {
	s.startedOnce.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return "", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *shutdownBlockingAuthStore) Delete(context.Context, string) error { return nil }

func (s *shutdownBlockingAuthStore) releaseSave() {
	s.releaseOnce.Do(func() { close(s.release) })
}

func TestServiceShutdownFlushesAuthPersistence(t *testing.T) {
	store := newShutdownBlockingAuthStore()
	manager := coreauth.NewManager(store, nil, nil)
	auth := &coreauth.Auth{
		ID:       "auth-1",
		Provider: "gemini",
		Metadata: map[string]any{
			"type": "gemini",
		},
	}
	if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "test-model",
		Success:  true,
	})

	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for auth persistence to start")
	}

	service := &Service{coreManager: manager}
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- service.Shutdown(context.Background())
	}()

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown returned error before persistence was released: %v", err)
		}
		t.Fatal("Shutdown returned before flushing auth persistence")
	case <-time.After(100 * time.Millisecond):
	}

	store.releaseSave()
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Shutdown after releasing auth persistence")
	}
}
