package usage

import (
	"context"
	"testing"
	"time"
)

type testContextKey struct{}

type capturePlugin struct {
	ctxs chan context.Context
}

func (p *capturePlugin) HandleUsage(ctx context.Context, _ Record) {
	p.ctxs <- ctx
}

func TestManagerPublishDetachesRequestCancellation(t *testing.T) {
	mgr := NewManager(1)
	plugin := &capturePlugin{ctxs: make(chan context.Context, 1)}
	mgr.Register(plugin)
	defer mgr.Stop()

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), testContextKey{}, "request-id"))
	cancel()

	mgr.Publish(ctx, Record{Provider: "codex", Model: "gpt-test"})

	select {
	case gotCtx := <-plugin.ctxs:
		if err := gotCtx.Err(); err != nil {
			t.Fatalf("published context is canceled: %v", err)
		}
		if got := gotCtx.Value(testContextKey{}); got != "request-id" {
			t.Fatalf("context value = %v, want request-id", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage dispatch")
	}
}
