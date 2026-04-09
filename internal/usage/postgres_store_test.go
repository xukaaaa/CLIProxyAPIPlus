package usage

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestPostgresUsageStoreEnsureSchemaAndRecordAndSnapshot(t *testing.T) {
	dsn := os.Getenv("PGSTORE_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tableName := "test_usage_events_" + time.Now().UTC().Format("20060102150405.000000000")
	store, err := NewPostgresUsageStore(ctx, PostgresUsageStoreConfig{
		DSN:       dsn,
		TableName: tableName,
	})
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	defer func() {
		store.db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+store.fullTableName(tableName))
		store.Close()
	}()

	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	if err := store.Record(ctx, coreusage.Record{
		APIKey:      "test-api-key",
		Model:       "accounts/fireworks/models/qwen3p6-plus",
		RequestedAt: time.Date(2026, 4, 8, 10, 8, 34, 0, time.UTC),
		Latency:     12251 * time.Millisecond,
		Source:      "fw_5SnYmbdUKHuZC5ZePi4g5n",
		AuthIndex:   "c1738e197b189fd3",
		Detail: coreusage.Detail{
			InputTokens:  158985,
			OutputTokens: 31,
			TotalTokens:  159016,
		},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	if snapshot.TotalRequests != 1 {
		t.Fatalf("expected total_requests 1, got %d", snapshot.TotalRequests)
	}
	if snapshot.TotalTokens != 159016 {
		t.Fatalf("expected total_tokens 159016, got %d", snapshot.TotalTokens)
	}
	details := snapshot.APIs["test-api-key"].Models["accounts/fireworks/models/qwen3p6-plus"].Details
	if len(details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(details))
	}
	if details[0].LatencyMs != 12251 {
		t.Fatalf("expected latency_ms 12251, got %d", details[0].LatencyMs)
	}
	if details[0].MachineID != store.MachineID() {
		t.Fatalf("expected machine_id %q, got %q", store.MachineID(), details[0].MachineID)
	}
}

func TestPostgresUsageStoreRecordPreservesCallerMachineID(t *testing.T) {
	dsn := os.Getenv("PGSTORE_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tableName := "test_usage_machine_record_" + time.Now().UTC().Format("20060102150405.000000000")
	store, err := NewPostgresUsageStore(ctx, PostgresUsageStoreConfig{
		DSN:       dsn,
		TableName: tableName,
	})
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	defer func() {
		store.db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+store.fullTableName(tableName))
		store.Close()
	}()

	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	if err := store.Record(ctx, coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		MachineID:   "machine-explicit",
		RequestedAt: time.Date(2026, 4, 8, 10, 8, 34, 0, time.UTC),
		Latency:     1500 * time.Millisecond,
		Source:      "source-a",
		AuthIndex:   "auth-a",
		Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(details))
	}
	if details[0].MachineID != "machine-explicit" {
		t.Fatalf("expected machine_id %q, got %q", "machine-explicit", details[0].MachineID)
	}
}

func TestPostgresUsageStoreMergeSnapshotDedupIgnoresLatency(t *testing.T) {
	dsn := os.Getenv("PGSTORE_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tableName := "test_usage_merge_" + time.Now().UTC().Format("20060102150405.000000000")
	store, err := NewPostgresUsageStore(ctx, PostgresUsageStoreConfig{
		DSN:       dsn,
		TableName: tableName,
	})
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	defer func() {
		store.db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+store.fullTableName(tableName))
		store.Close()
	}()

	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	timestamp := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	first := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 0,
							Source:    "user@example.com",
							AuthIndex: "0",
							MachineID: "machine-a",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}
	second := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 2500,
							Source:    "user@example.com",
							AuthIndex: "0",
							MachineID: "machine-a",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}

	result, err := store.MergeSnapshot(ctx, first)
	if err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("first merge = %+v, want added=1 skipped=0", result)
	}

	result, err = store.MergeSnapshot(ctx, second)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("second merge = %+v, want added=0 skipped=1", result)
	}

	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(details))
	}
}

func TestPostgresUsageStoreMergeSnapshotKeepsDifferentMachineIDs(t *testing.T) {
	dsn := os.Getenv("PGSTORE_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tableName := "test_usage_machine_dedup_" + time.Now().UTC().Format("20060102150405.000000000")
	store, err := NewPostgresUsageStore(ctx, PostgresUsageStoreConfig{
		DSN:       dsn,
		TableName: tableName,
	})
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	defer func() {
		store.db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+store.fullTableName(tableName))
		store.Close()
	}()

	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	timestamp := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	first := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 0,
							Source:    "user@example.com",
							AuthIndex: "0",
							MachineID: "machine-a",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}
	second := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 0,
							Source:    "user@example.com",
							AuthIndex: "0",
							MachineID: "machine-b",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}

	result, err := store.MergeSnapshot(ctx, first)
	if err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("first merge = %+v, want added=1 skipped=0", result)
	}

	result, err = store.MergeSnapshot(ctx, second)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("second merge = %+v, want added=1 skipped=0", result)
	}

	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 2 {
		t.Fatalf("expected 2 details, got %d", len(details))
	}
	machineIDs := map[string]bool{}
	for _, detail := range details {
		machineIDs[detail.MachineID] = true
	}
	if !machineIDs["machine-a"] || !machineIDs["machine-b"] {
		t.Fatalf("expected machine_ids machine-a and machine-b, got %+v", machineIDs)
	}
}

func TestPostgresUsageStoreSnapshotMatchesInMemory(t *testing.T) {
	dsn := os.Getenv("PGSTORE_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tableName := "test_usage_parity_" + time.Now().UTC().Format("20060102150405.000000000")
	store, err := NewPostgresUsageStore(ctx, PostgresUsageStoreConfig{
		DSN:       dsn,
		TableName: tableName,
	})
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	defer func() {
		store.db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+store.fullTableName(tableName))
		store.Close()
	}()

	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	memory := NewRequestStatistics()
	records := []coreusage.Record{
		{
			APIKey:      "key-a",
			Model:       "model-a",
			RequestedAt: time.Date(2026, 4, 8, 10, 8, 34, 0, time.UTC),
			Latency:     1500 * time.Millisecond,
			Source:      "source-a",
			AuthIndex:   "auth-a",
			Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
		},
		{
			APIKey:      "key-a",
			Model:       "model-b",
			RequestedAt: time.Date(2026, 4, 8, 11, 8, 34, 0, time.UTC),
			Latency:     2500 * time.Millisecond,
			Source:      "source-b",
			AuthIndex:   "auth-b",
			Failed:      true,
			Detail: coreusage.Detail{InputTokens: 5, OutputTokens: 15, TotalTokens: 20},
		},
	}

	for _, record := range records {
		memory.Record(ctx, record)
		if err := store.Record(ctx, record); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	got, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	want := memory.Snapshot()

	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("snapshot mismatch\nwant=%s\n got=%s", wantJSON, gotJSON)
	}
}
