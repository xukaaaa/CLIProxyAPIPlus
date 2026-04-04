package usage

import (
	"context"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestLoggerPluginRespectsStatisticsEnabled(t *testing.T) {
	prevBackend := GetStatisticsBackend()
	prevEnabled := StatisticsEnabled()
	t.Cleanup(func() {
		SetStatisticsBackend(prevBackend)
		SetStatisticsEnabled(prevEnabled)
	})

	backend := &recordingStatisticsBackend{}
	SetStatisticsBackend(backend)

	plugin := NewLoggerPlugin()
	SetStatisticsEnabled(false)
	plugin.HandleUsage(context.Background(), coreusage.Record{APIKey: "test-key", Model: "gpt-5.5"})
	if backend.records != 0 {
		t.Fatalf("records while disabled = %d, want 0", backend.records)
	}

	SetStatisticsEnabled(true)
	plugin.HandleUsage(context.Background(), coreusage.Record{APIKey: "test-key", Model: "gpt-5.5"})
	if backend.records != 1 {
		t.Fatalf("records while enabled = %d, want 1", backend.records)
	}
}

type recordingStatisticsBackend struct {
	records int
}

func (b *recordingStatisticsBackend) Record(context.Context, coreusage.Record) error {
	b.records++
	return nil
}

func (b *recordingStatisticsBackend) Snapshot(context.Context) (StatisticsSnapshot, error) {
	return StatisticsSnapshot{APIs: map[string]APISnapshot{}}, nil
}
