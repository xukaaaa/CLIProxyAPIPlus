package usage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const defaultUsageEventsTable = "usage_request_events"

// PostgresUsageStoreConfig configures durable usage persistence.
type PostgresUsageStoreConfig struct {
	DSN       string
	Schema    string
	TableName string
}

// PostgresUsageStore stores usage events in PostgreSQL.
type PostgresUsageStore struct {
	db        *sql.DB
	cfg       PostgresUsageStoreConfig
	machineID string
}

type persistedUsageRecord struct {
	requestedAt     time.Time
	apiKey          string
	model           string
	source          string
	authIndex       string
	machineID       string
	reasoningEffort string
	failed          bool
	latencyMs       int64
	tokens          TokenStats
	dedupKey        string
}

// NewPostgresUsageStore opens a PostgreSQL-backed usage store.
func NewPostgresUsageStore(ctx context.Context, cfg PostgresUsageStoreConfig) (*PostgresUsageStore, error) {
	trimmedDSN := strings.TrimSpace(cfg.DSN)
	if trimmedDSN == "" {
		return nil, fmt.Errorf("postgres usage store: DSN is required")
	}
	cfg.DSN = trimmedDSN
	if strings.TrimSpace(cfg.TableName) == "" {
		cfg.TableName = defaultUsageEventsTable
	}

	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres usage store: open database connection: %w", err)
	}
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres usage store: ping database: %w", err)
	}

	return &PostgresUsageStore{db: db, cfg: cfg, machineID: uuid.New().String()}, nil
}

// Close closes the database connection.
func (s *PostgresUsageStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// EnsureSchema creates or upgrades the usage table.
func (s *PostgresUsageStore) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres usage store: not initialized")
	}
	if schema := strings.TrimSpace(s.cfg.Schema); schema != "" {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema))); err != nil {
			return fmt.Errorf("postgres usage store: create schema: %w", err)
		}
	}

	tableName := s.fullTableName(s.cfg.TableName)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGSERIAL PRIMARY KEY,
			requested_at TIMESTAMPTZ NOT NULL,
			api_key TEXT NOT NULL,
			model TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			auth_index TEXT NOT NULL DEFAULT '',
			machine_id TEXT NOT NULL DEFAULT '',
			reasoning_effort TEXT NOT NULL DEFAULT '',
			failed BOOLEAN NOT NULL DEFAULT FALSE,
			latency_ms BIGINT NOT NULL DEFAULT 0,
			input_tokens BIGINT NOT NULL DEFAULT 0,
			output_tokens BIGINT NOT NULL DEFAULT 0,
			reasoning_tokens BIGINT NOT NULL DEFAULT 0,
			cached_tokens BIGINT NOT NULL DEFAULT 0,
			total_tokens BIGINT NOT NULL DEFAULT 0,
			dedup_key TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, tableName)); err != nil {
		return fmt.Errorf("postgres usage store: create table: %w", err)
	}

	for _, stmt := range []string{
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT ''`, tableName),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS auth_index TEXT NOT NULL DEFAULT ''`, tableName),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS machine_id TEXT NOT NULL DEFAULT ''`, tableName),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS reasoning_effort TEXT NOT NULL DEFAULT ''`, tableName),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS failed BOOLEAN NOT NULL DEFAULT FALSE`, tableName),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS latency_ms BIGINT NOT NULL DEFAULT 0`, tableName),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS input_tokens BIGINT NOT NULL DEFAULT 0`, tableName),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS output_tokens BIGINT NOT NULL DEFAULT 0`, tableName),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS reasoning_tokens BIGINT NOT NULL DEFAULT 0`, tableName),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS cached_tokens BIGINT NOT NULL DEFAULT 0`, tableName),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS total_tokens BIGINT NOT NULL DEFAULT 0`, tableName),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS dedup_key TEXT NOT NULL DEFAULT ''`, tableName),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`, tableName),
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("postgres usage store: alter table: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (requested_at)`, quoteIdentifier(s.cfg.TableName+"_requested_at_idx"), tableName)); err != nil {
		return fmt.Errorf("postgres usage store: create requested_at index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (api_key, model, requested_at)`, quoteIdentifier(s.cfg.TableName+"_api_model_requested_at_idx"), tableName)); err != nil {
		return fmt.Errorf("postgres usage store: create api/model/requested_at index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET dedup_key = CONCAT(id::text, '-legacy') WHERE dedup_key = ''`, tableName)); err != nil {
		return fmt.Errorf("postgres usage store: backfill dedup_key: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (dedup_key)`, quoteIdentifier(s.cfg.TableName+"_dedup_key_idx"), tableName)); err != nil {
		return fmt.Errorf("postgres usage store: create dedup_key index: %w", err)
	}
	return nil
}

// Record persists one usage record.
func (s *PostgresUsageStore) Record(ctx context.Context, record coreusage.Record) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres usage store: not initialized")
	}
	persisted := normalisePersistedUsageRecord(ctx, record, s.machineID)
	return s.insertPersistedRecord(ctx, persisted)
}

// Snapshot reads all usage events and returns an aggregated management snapshot.
func (s *PostgresUsageStore) Snapshot(ctx context.Context) (StatisticsSnapshot, error) {
	if s == nil || s.db == nil {
		return StatisticsSnapshot{}, fmt.Errorf("postgres usage store: not initialized")
	}

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT requested_at, api_key, model, source, auth_index, machine_id, reasoning_effort, failed, latency_ms,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens
		FROM %s
		ORDER BY requested_at, id
	`, s.fullTableName(s.cfg.TableName)))
	if err != nil {
		return StatisticsSnapshot{}, fmt.Errorf("postgres usage store: query rows: %w", err)
	}
	defer rows.Close()

	stats := NewRequestStatistics()
	for rows.Next() {
		var detail RequestDetail
		var apiKey, model string
		if err = rows.Scan(
			&detail.Timestamp,
			&apiKey,
			&model,
			&detail.Source,
			&detail.AuthIndex,
			&detail.MachineID,
			&detail.ReasoningEffort,
			&detail.Failed,
			&detail.LatencyMs,
			&detail.Tokens.InputTokens,
			&detail.Tokens.OutputTokens,
			&detail.Tokens.ReasoningTokens,
			&detail.Tokens.CachedTokens,
			&detail.Tokens.TotalTokens,
		); err != nil {
			return StatisticsSnapshot{}, fmt.Errorf("postgres usage store: scan row: %w", err)
		}
		detail.Timestamp = detail.Timestamp.UTC()
		detail.Tokens = normaliseTokenStats(detail.Tokens)
		stats.mu.Lock()
		stats.recordImportedLocked(apiKey, normalizeModelName(model), ensureAPIStats(stats, apiKey), detail)
		stats.mu.Unlock()
	}
	if err = rows.Err(); err != nil {
		return StatisticsSnapshot{}, fmt.Errorf("postgres usage store: iterate rows: %w", err)
	}
	return stats.Snapshot(), nil
}

func (s *PostgresUsageStore) insertPersistedRecord(ctx context.Context, record persistedUsageRecord) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			requested_at, api_key, model, source, auth_index, machine_id, reasoning_effort, failed, latency_ms,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, dedup_key
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14, $15
		)
		ON CONFLICT (dedup_key) DO NOTHING
	`, s.fullTableName(s.cfg.TableName)),
		record.requestedAt,
		record.apiKey,
		record.model,
		record.source,
		record.authIndex,
		record.machineID,
		record.reasoningEffort,
		record.failed,
		record.latencyMs,
		record.tokens.InputTokens,
		record.tokens.OutputTokens,
		record.tokens.ReasoningTokens,
		record.tokens.CachedTokens,
		record.tokens.TotalTokens,
		record.dedupKey,
	)
	if err != nil {
		return fmt.Errorf("postgres usage store: insert row: %w", err)
	}
	return nil
}

func normalisePersistedUsageRecord(ctx context.Context, record coreusage.Record, machineID string) persistedUsageRecord {
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	tokens := normaliseDetail(record.Detail)
	apiKey := resolveAPIIdentifier(ctx, record)
	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	model := normalizeModelName(record.Model)
	detail := RequestDetail{
		Timestamp:       timestamp,
		LatencyMs:       normaliseLatency(record.Latency),
		Source:          strings.TrimSpace(record.Source),
		AuthIndex:       strings.TrimSpace(record.AuthIndex),
		MachineID:       strings.TrimSpace(machineID),
		ReasoningEffort: strings.TrimSpace(record.ReasoningEffort),
		Tokens:          tokens,
		Failed:          failed,
	}
	return persistedUsageRecord{
		requestedAt:     timestamp,
		apiKey:          apiKey,
		model:           model,
		source:          detail.Source,
		authIndex:       detail.AuthIndex,
		machineID:       detail.MachineID,
		reasoningEffort: detail.ReasoningEffort,
		failed:          failed,
		latencyMs:       detail.LatencyMs,
		tokens:          tokens,
		dedupKey:        dedupKey(apiKey, model, detail),
	}
}

func (s *PostgresUsageStore) fullTableName(name string) string {
	quotedName := quoteIdentifier(strings.TrimSpace(name))
	if schema := strings.TrimSpace(s.cfg.Schema); schema != "" {
		return quoteIdentifier(schema) + "." + quotedName
	}
	return quotedName
}

func quoteIdentifier(identifier string) string {
	escaped := strings.ReplaceAll(strings.TrimSpace(identifier), `"`, `""`)
	return `"` + escaped + `"`
}
