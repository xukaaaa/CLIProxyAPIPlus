package usage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

const defaultUsageEventsTable = "usage_request_events"

type PostgresUsageStoreConfig struct {
	DSN       string
	Schema    string
	TableName string
}

type StatisticsBackend interface {
	Record(context.Context, coreusage.Record) error
	Snapshot(context.Context) (StatisticsSnapshot, error)
	MergeSnapshot(context.Context, StatisticsSnapshot) (MergeResult, error)
}

type PostgresUsageStore struct {
	db  *sql.DB
	cfg PostgresUsageStoreConfig
}

type persistedUsageRecord struct {
	requestedAt time.Time
	apiKey      string
	model       string
	source      string
	authIndex   string
	failed      bool
	latencyMs   int64
	tokens      TokenStats
	dedupKey    string
}

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

	return &PostgresUsageStore{db: db, cfg: cfg}, nil
}

func (s *PostgresUsageStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *PostgresUsageStore) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres usage store: not initialized")
	}
	if schema := strings.TrimSpace(s.cfg.Schema); schema != "" {
		query := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema))
		if _, err := s.db.ExecContext(ctx, query); err != nil {
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

func (s *PostgresUsageStore) Record(ctx context.Context, record coreusage.Record) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres usage store: not initialized")
	}
	persisted := normalisePersistedUsageRecord(ctx, record)
	return s.insertPersistedRecord(ctx, persisted)
}

func (s *PostgresUsageStore) Snapshot(ctx context.Context) (StatisticsSnapshot, error) {
	if s == nil || s.db == nil {
		return StatisticsSnapshot{}, fmt.Errorf("postgres usage store: not initialized")
	}

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT requested_at, api_key, model, source, auth_index, failed, latency_ms,
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
		if err := rows.Scan(
			&detail.Timestamp,
			&apiKey,
			&model,
			&detail.Source,
			&detail.AuthIndex,
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
		stats.recordImported(apiKey, model, ensureAPIStats(stats, apiKey), detail)
	}
	if err := rows.Err(); err != nil {
		return StatisticsSnapshot{}, fmt.Errorf("postgres usage store: iterate rows: %w", err)
	}
	return stats.Snapshot(), nil
}

func (s *PostgresUsageStore) MergeSnapshot(ctx context.Context, snapshot StatisticsSnapshot) (MergeResult, error) {
	if s == nil || s.db == nil {
		return MergeResult{}, fmt.Errorf("postgres usage store: not initialized")
	}

	result := MergeResult{}
	for apiName, apiSnapshot := range snapshot.APIs {
		apiName = strings.TrimSpace(apiName)
		if apiName == "" {
			continue
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = strings.TrimSpace(modelName)
			if modelName == "" {
				modelName = "unknown"
			}
			for _, detail := range modelSnapshot.Details {
				detail.Tokens = normaliseTokenStats(detail.Tokens)
				if detail.LatencyMs < 0 {
					detail.LatencyMs = 0
				}
				if detail.Timestamp.IsZero() {
					detail.Timestamp = time.Now()
				}
				record := persistedUsageRecord{
					requestedAt: detail.Timestamp,
					apiKey:      apiName,
					model:       modelName,
					source:      detail.Source,
					authIndex:   detail.AuthIndex,
					failed:      detail.Failed,
					latencyMs:   detail.LatencyMs,
					tokens:      detail.Tokens,
					dedupKey:    dedupKey(apiName, modelName, detail),
				}
				inserted, err := s.insertPersistedRecordWithResult(ctx, record)
				if err != nil {
					return MergeResult{}, err
				}
				if inserted {
					result.Added++
				} else {
					result.Skipped++
				}
			}
		}
	}
	return result, nil
}

func (s *PostgresUsageStore) insertPersistedRecord(ctx context.Context, record persistedUsageRecord) error {
	_, err := s.insertPersistedRecordWithResult(ctx, record)
	return err
}

func (s *PostgresUsageStore) insertPersistedRecordWithResult(ctx context.Context, record persistedUsageRecord) (bool, error) {
	result, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			requested_at, api_key, model, source, auth_index, failed, latency_ms,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, dedup_key
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12, $13
		)
		ON CONFLICT (dedup_key) DO NOTHING
	`, s.fullTableName(s.cfg.TableName)),
		record.requestedAt,
		record.apiKey,
		record.model,
		record.source,
		record.authIndex,
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
		return false, fmt.Errorf("postgres usage store: insert row: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("postgres usage store: rows affected: %w", err)
	}
	return affected > 0, nil
}

func normalisePersistedUsageRecord(ctx context.Context, record coreusage.Record) persistedUsageRecord {
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	tokens := normaliseDetail(record.Detail)
	apiKey := record.APIKey
	if apiKey == "" {
		apiKey = resolveAPIIdentifier(ctx, record)
	}
	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	model := record.Model
	if model == "" {
		model = "unknown"
	}
	detail := RequestDetail{
		Timestamp: timestamp,
		LatencyMs: normaliseLatency(record.Latency),
		Source:    record.Source,
		AuthIndex: record.AuthIndex,
		Tokens:    tokens,
		Failed:    failed,
	}
	return persistedUsageRecord{
		requestedAt: timestamp,
		apiKey:      apiKey,
		model:       model,
		source:      record.Source,
		authIndex:   record.AuthIndex,
		failed:      failed,
		latencyMs:   detail.LatencyMs,
		tokens:      tokens,
		dedupKey:    dedupKey(apiKey, model, detail),
	}
}

func ensureAPIStats(stats *RequestStatistics, apiName string) *apiStats {
	if stats.apis == nil {
		stats.apis = make(map[string]*apiStats)
	}
	apiValue, ok := stats.apis[apiName]
	if !ok || apiValue == nil {
		apiValue = &apiStats{Models: make(map[string]*modelStats)}
		stats.apis[apiName] = apiValue
	} else if apiValue.Models == nil {
		apiValue.Models = make(map[string]*modelStats)
	}
	return apiValue
}

func (s *PostgresUsageStore) fullTableName(name string) string {
	quotedName := quoteIdentifier(strings.TrimSpace(name))
	if schema := strings.TrimSpace(s.cfg.Schema); schema != "" {
		return quoteIdentifier(schema) + "." + quotedName
	}
	return quotedName
}

func (s *PostgresUsageStore) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

func (s *PostgresUsageStore) FullTableName(name string) string {
	if s == nil {
		return ""
	}
	return s.fullTableName(name)
}

func quoteIdentifier(identifier string) string {
	escaped := strings.ReplaceAll(strings.TrimSpace(identifier), `"`, `""`)
	return `"` + escaped + `"`
}
