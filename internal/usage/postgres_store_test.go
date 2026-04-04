package usage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestPostgresUsageStoreSnapshotReadsExistingRows(t *testing.T) {
	dsn := os.Getenv("PGSTORE_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tableName := fmt.Sprintf("test_usage_events_%d", time.Now().UnixNano())
	db := openUsageTestDB(t, ctx, dsn)
	defer db.Close()
	defer dropUsageTestTable(t, db, tableName)

	store, err := NewPostgresUsageStore(ctx, PostgresUsageStoreConfig{
		DSN:       dsn,
		TableName: tableName,
	})
	if err != nil {
		t.Fatalf("new postgres usage store: %v", err)
	}
	defer store.Close()
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	insertUsageTestEvent(t, ctx, db, tableName, usageTestEvent{
		RequestedAt:     time.Date(2026, 5, 26, 16, 2, 52, 553653000, time.UTC),
		APIKey:          "test-api-key",
		Model:           "gpt-5.5",
		Source:          "user@example.test",
		AuthIndex:       "auth-index-1",
		MachineID:       "machine-a",
		InputTokens:     123033,
		OutputTokens:    928,
		ReasoningTokens: 516,
		CachedTokens:    116608,
		TotalTokens:     123961,
		DedupKey:        "usage-test-1",
	})
	insertUsageTestEvent(t, ctx, db, tableName, usageTestEvent{
		RequestedAt: time.Date(2026, 5, 26, 16, 3, 0, 0, time.UTC),
		APIKey:      "test-api-key",
		Model:       "gpt-5.5",
		Source:      "user@example.test",
		AuthIndex:   "auth-index-1",
		MachineID:   "machine-a",
		Failed:      true,
		LatencyMs:   1000,
		TotalTokens: 10,
		DedupKey:    "usage-test-2",
	})

	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	if snapshot.TotalRequests != 2 {
		t.Fatalf("total requests = %d, want 2", snapshot.TotalRequests)
	}
	if snapshot.SuccessCount != 1 || snapshot.FailureCount != 1 {
		t.Fatalf("success/failure = %d/%d, want 1/1", snapshot.SuccessCount, snapshot.FailureCount)
	}
	if snapshot.TotalTokens != 123971 {
		t.Fatalf("total tokens = %d, want 123971", snapshot.TotalTokens)
	}

	apiSnapshot, ok := snapshot.APIs["test-api-key"]
	if !ok {
		t.Fatalf("snapshot missing api key bucket: %#v", snapshot.APIs)
	}
	modelSnapshot, ok := apiSnapshot.Models["gpt-5.5"]
	if !ok {
		t.Fatalf("snapshot missing model bucket: %#v", apiSnapshot.Models)
	}
	if len(modelSnapshot.Details) != 2 {
		t.Fatalf("model details = %d, want 2", len(modelSnapshot.Details))
	}
	first := modelSnapshot.Details[0]
	if first.Source != "user@example.test" || first.AuthIndex != "auth-index-1" || first.MachineID != "machine-a" {
		t.Fatalf("first detail identifiers = source:%q auth:%q machine:%q", first.Source, first.AuthIndex, first.MachineID)
	}
	if first.Tokens.InputTokens != 123033 || first.Tokens.CachedTokens != 116608 || first.Tokens.TotalTokens != 123961 {
		t.Fatalf("first detail tokens = %#v", first.Tokens)
	}
}

func TestPostgresUsageStoreRecordWritesMachineAwareDedupedRows(t *testing.T) {
	dsn := os.Getenv("PGSTORE_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tableName := fmt.Sprintf("test_usage_record_%d", time.Now().UnixNano())
	db := openUsageTestDB(t, ctx, dsn)
	defer db.Close()
	defer dropUsageTestTable(t, db, tableName)

	store, err := NewPostgresUsageStore(ctx, PostgresUsageStoreConfig{
		DSN:       dsn,
		TableName: tableName,
	})
	if err != nil {
		t.Fatalf("new postgres usage store: %v", err)
	}
	defer store.Close()
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	record := coreusage.Record{
		APIKey:          "test-management-key",
		Model:           "gpt-5.5",
		Source:          "user@example.com",
		AuthIndex:       "auth-index-1",
		ReasoningEffort: "medium",
		RequestedAt:     time.Date(2026, 5, 26, 16, 2, 52, 553653000, time.UTC),
		Latency:         1500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:     100,
			OutputTokens:    20,
			ReasoningTokens: 5,
			CachedTokens:    80,
			TotalTokens:     125,
		},
	}

	if err = store.Record(ctx, record); err != nil {
		t.Fatalf("record usage: %v", err)
	}
	if err = store.Record(ctx, record); err != nil {
		t.Fatalf("record duplicate usage: %v", err)
	}

	row := db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COUNT(*), COALESCE(MAX(machine_id), ''), COALESCE(MAX(reasoning_effort), '')
		FROM %s
	`, tableName))
	var count int
	var machineID string
	var reasoningEffort string
	if err = row.Scan(&count, &machineID, &reasoningEffort); err != nil {
		t.Fatalf("query persisted usage: %v", err)
	}
	if count != 1 {
		t.Fatalf("persisted rows = %d, want 1", count)
	}
	if machineID == "" {
		t.Fatalf("machine_id is empty")
	}
	if reasoningEffort != "medium" {
		t.Fatalf("reasoning_effort = %q, want medium", reasoningEffort)
	}
}

func TestPostgresUsageStoreEnsureSchemaUpgradesExistingUsageTable(t *testing.T) {
	dsn := os.Getenv("PGSTORE_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tableName := fmt.Sprintf("test_usage_upgrade_%d", time.Now().UnixNano())
	db := openUsageTestDB(t, ctx, dsn)
	defer db.Close()
	defer dropUsageTestTable(t, db, tableName)
	createLegacyUsageTestTable(t, ctx, db, tableName)

	store, err := NewPostgresUsageStore(ctx, PostgresUsageStoreConfig{
		DSN:       dsn,
		TableName: tableName,
	})
	if err != nil {
		t.Fatalf("new postgres usage store: %v", err)
	}
	defer store.Close()
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	var hasReasoningEffort bool
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = current_schema()
				AND table_name = $1
				AND column_name = 'reasoning_effort'
		)
	`, tableName).Scan(&hasReasoningEffort)
	if err != nil {
		t.Fatalf("query reasoning_effort column: %v", err)
	}
	if !hasReasoningEffort {
		t.Fatalf("reasoning_effort column was not added")
	}

	if err = store.Record(ctx, coreusage.Record{
		APIKey:          "test-management-key",
		Model:           "gpt-5.5",
		ReasoningEffort: "high",
		RequestedAt:     time.Date(2026, 5, 26, 17, 0, 0, 0, time.UTC),
		Detail:          coreusage.Detail{TotalTokens: 42},
	}); err != nil {
		t.Fatalf("record usage after schema upgrade: %v", err)
	}

	row := db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COUNT(*), COALESCE(MAX(reasoning_effort), '')
		FROM %s
	`, tableName))
	var count int
	var reasoningEffort string
	if err = row.Scan(&count, &reasoningEffort); err != nil {
		t.Fatalf("query upgraded usage: %v", err)
	}
	if count != 1 {
		t.Fatalf("persisted rows = %d, want 1", count)
	}
	if reasoningEffort != "high" {
		t.Fatalf("reasoning_effort = %q, want high", reasoningEffort)
	}
}

type usageTestEvent struct {
	RequestedAt     time.Time
	APIKey          string
	Model           string
	Source          string
	AuthIndex       string
	MachineID       string
	Failed          bool
	LatencyMs       int64
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
	TotalTokens     int64
	DedupKey        string
}

func openUsageTestDB(t *testing.T, ctx context.Context, dsn string) *sql.DB {
	t.Helper()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		t.Fatalf("ping db: %v", err)
	}
	return db
}

func dropUsageTestTable(t *testing.T, db *sql.DB, tableName string) {
	t.Helper()

	if _, err := db.ExecContext(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName)); err != nil {
		t.Fatalf("drop table %s: %v", tableName, err)
	}
}

func createLegacyUsageTestTable(t *testing.T, ctx context.Context, db *sql.DB, tableName string) {
	t.Helper()

	_, err := db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id BIGSERIAL PRIMARY KEY,
			requested_at TIMESTAMPTZ NOT NULL,
			api_key TEXT NOT NULL,
			model TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			auth_index TEXT NOT NULL DEFAULT '',
			failed BOOLEAN NOT NULL DEFAULT FALSE,
			latency_ms BIGINT NOT NULL DEFAULT 0,
			input_tokens BIGINT NOT NULL DEFAULT 0,
			output_tokens BIGINT NOT NULL DEFAULT 0,
			reasoning_tokens BIGINT NOT NULL DEFAULT 0,
			cached_tokens BIGINT NOT NULL DEFAULT 0,
			total_tokens BIGINT NOT NULL DEFAULT 0,
			dedup_key TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			machine_id TEXT NOT NULL DEFAULT ''
		)
	`, tableName))
	if err != nil {
		t.Fatalf("create legacy usage table: %v", err)
	}
}

func insertUsageTestEvent(t *testing.T, ctx context.Context, db *sql.DB, tableName string, event usageTestEvent) {
	t.Helper()

	_, err := db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			requested_at, api_key, model, source, auth_index, machine_id, failed, latency_ms,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, dedup_key
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14
		)
	`, tableName),
		event.RequestedAt,
		event.APIKey,
		event.Model,
		event.Source,
		event.AuthIndex,
		event.MachineID,
		event.Failed,
		event.LatencyMs,
		event.InputTokens,
		event.OutputTokens,
		event.ReasoningTokens,
		event.CachedTokens,
		event.TotalTokens,
		event.DedupKey,
	)
	if err != nil {
		t.Fatalf("insert usage event: %v", err)
	}
}
