package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type fireworks412Executor struct {
	mu       sync.Mutex
	provider string
	calls    []string
	errors   map[string]error
}

func (e *fireworks412Executor) Identifier() string {
	if e.provider != "" {
		return e.provider
	}
	return "fireworks"
}

func (e *fireworks412Executor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	_ = ctx
	_ = opts
	e.mu.Lock()
	e.calls = append(e.calls, auth.ID)
	err := e.errors[auth.ID]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *fireworks412Executor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "ExecuteStream not implemented"}
}

func (e *fireworks412Executor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *fireworks412Executor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *fireworks412Executor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *fireworks412Executor) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.calls))
	copy(out, e.calls)
	return out
}

func TestManagerExecute_Fireworks412SwapsToNextKey(t *testing.T) {
	ctx := context.Background()
	model := "kimi-code"

	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)

	executor := &fireworks412Executor{
		errors: map[string]error{
			"fw-1": &Error{HTTPStatus: http.StatusPreconditionFailed, Message: "Account suspended"},
		},
	}
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("fw-1", "fireworks", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient("fw-2", "fireworks", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient("fw-1")
		reg.UnregisterClient("fw-2")
	})

	auth1 := &Auth{ID: "fw-1", Provider: "fireworks", Status: StatusActive}
	auth2 := &Auth{ID: "fw-2", Provider: "fireworks", Status: StatusActive}
	if _, err := m.Register(ctx, auth1); err != nil {
		t.Fatalf("register fw-1: %v", err)
	}
	if _, err := m.Register(ctx, auth2); err != nil {
		t.Fatalf("register fw-2: %v", err)
	}

	resp, err := m.Execute(ctx, []string{"fireworks"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("payload = %q, want ok", string(resp.Payload))
	}

	calls := executor.Calls()
	want := []string{"fw-1", "fw-2"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, calls[i], want[i])
		}
	}

	updated1, ok := m.GetByID("fw-1")
	if !ok || updated1 == nil {
		t.Fatalf("fw-1 not found after execute")
	}
	if !updated1.Unavailable {
		t.Fatalf("fw-1.Unavailable = false, want true")
	}
	if updated1.StatusMessage != "account_suspended" {
		t.Fatalf("fw-1.StatusMessage = %q, want account_suspended", updated1.StatusMessage)
	}
	if updated1.NextRetryAfter.IsZero() {
		t.Fatalf("fw-1.NextRetryAfter is zero, want future cooldown")
	}

	updated2, ok := m.GetByID("fw-2")
	if !ok || updated2 == nil {
		t.Fatalf("fw-2 not found after execute")
	}
	if updated2.Unavailable {
		t.Fatalf("fw-2.Unavailable = true, want false")
	}
}

func TestManagerExecute_NonFireworks412DoesNotSuspend(t *testing.T) {
	ctx := context.Background()
	model := "claude-model"
	provider := "claude"

	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)

	executor := &fireworks412Executor{
		provider: provider,
		errors: map[string]error{
			"claude-1": &Error{HTTPStatus: http.StatusPreconditionFailed, Message: "some precondition failed"},
		},
	}
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("claude-1", provider, []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient("claude-2", provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient("claude-1")
		reg.UnregisterClient("claude-2")
	})

	auth1 := &Auth{ID: "claude-1", Provider: provider, Status: StatusActive}
	auth2 := &Auth{ID: "claude-2", Provider: provider, Status: StatusActive}
	if _, err := m.Register(ctx, auth1); err != nil {
		t.Fatalf("register claude-1: %v", err)
	}
	if _, err := m.Register(ctx, auth2); err != nil {
		t.Fatalf("register claude-2: %v", err)
	}

	resp, err := m.Execute(ctx, []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("payload = %q, want ok", string(resp.Payload))
	}

	updated1, ok := m.GetByID("claude-1")
	if !ok || updated1 == nil {
		t.Fatalf("claude-1 not found after execute")
	}
	if updated1.Unavailable {
		t.Fatalf("claude-1.Unavailable = true, want false for non-fireworks 412")
	}
	if !updated1.NextRetryAfter.IsZero() {
		t.Fatalf("claude-1.NextRetryAfter = %v, want zero", updated1.NextRetryAfter)
	}
}
