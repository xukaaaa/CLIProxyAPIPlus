// Package usage provides durable usage statistics collection for management APIs.
package usage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

var statisticsEnabled atomic.Bool

func init() {
	statisticsEnabled.Store(true)
	coreusage.RegisterPlugin(NewLoggerPlugin())
}

var (
	statisticsBackendMu sync.RWMutex
	statisticsBackend   StatisticsBackend = NewInMemoryStatisticsBackend(NewRequestStatistics())
)

// StatisticsBackend records usage events and exposes management snapshots.
type StatisticsBackend interface {
	Record(context.Context, coreusage.Record) error
	Snapshot(context.Context) (StatisticsSnapshot, error)
}

// LoggerPlugin persists usage records through the configured statistics backend.
type LoggerPlugin struct{}

// NewLoggerPlugin constructs a usage logging plugin.
func NewLoggerPlugin() *LoggerPlugin { return &LoggerPlugin{} }

// HandleUsage implements coreusage.Plugin.
func (p *LoggerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || !statisticsEnabled.Load() {
		return
	}
	backend := GetStatisticsBackend()
	if backend == nil {
		return
	}
	if err := backend.Record(ctx, record); err != nil {
		log.WithError(err).Warn("usage: failed to persist usage record")
	}
}

// SetStatisticsEnabled toggles durable usage recording.
func SetStatisticsEnabled(enabled bool) { statisticsEnabled.Store(enabled) }

// StatisticsEnabled reports whether durable usage recording is enabled.
func StatisticsEnabled() bool { return statisticsEnabled.Load() }

// GetStatisticsBackend returns the configured usage backend.
func GetStatisticsBackend() StatisticsBackend {
	statisticsBackendMu.RLock()
	defer statisticsBackendMu.RUnlock()
	return statisticsBackend
}

// SetStatisticsBackend configures the shared usage backend.
func SetStatisticsBackend(backend StatisticsBackend) {
	statisticsBackendMu.Lock()
	defer statisticsBackendMu.Unlock()
	if backend == nil {
		statisticsBackend = NewInMemoryStatisticsBackend(NewRequestStatistics())
		return
	}
	statisticsBackend = backend
}

// TokenStats captures token usage for a request.
type TokenStats struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

// RequestDetail stores one usage event in snapshot form.
type RequestDetail struct {
	Timestamp       time.Time  `json:"timestamp"`
	LatencyMs       int64      `json:"latency_ms"`
	Source          string     `json:"source"`
	AuthIndex       string     `json:"auth_index"`
	MachineID       string     `json:"machine_id,omitempty"`
	ReasoningEffort string     `json:"reasoning_effort,omitempty"`
	Tokens          TokenStats `json:"tokens"`
	Failed          bool       `json:"failed"`
}

// StatisticsSnapshot is the management API shape consumed by the Web UI.
type StatisticsSnapshot struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`

	APIs map[string]APISnapshot `json:"apis"`
}

// APISnapshot groups usage by API key.
type APISnapshot struct {
	TotalRequests int64                    `json:"total_requests"`
	TotalTokens   int64                    `json:"total_tokens"`
	Models        map[string]ModelSnapshot `json:"models"`
}

// ModelSnapshot groups usage by model under an API key.
type ModelSnapshot struct {
	TotalRequests int64           `json:"total_requests"`
	TotalTokens   int64           `json:"total_tokens"`
	Details       []RequestDetail `json:"details"`
}

type apiStats struct {
	TotalRequests int64
	TotalTokens   int64
	Models        map[string]*modelStats
}

type modelStats struct {
	TotalRequests int64
	TotalTokens   int64
	Details       []RequestDetail
}

// RequestStatistics aggregates usage in memory.
type RequestStatistics struct {
	mu sync.RWMutex

	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64
	apis          map[string]*apiStats
}

// NewRequestStatistics constructs an empty in-memory statistics store.
func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{apis: make(map[string]*apiStats)}
}

// Record ingests one usage record.
func (s *RequestStatistics) Record(ctx context.Context, record coreusage.Record) {
	if s == nil {
		return
	}
	persisted := normalisePersistedUsageRecord(ctx, record, "")
	detail := RequestDetail{
		Timestamp:       persisted.requestedAt,
		LatencyMs:       persisted.latencyMs,
		Source:          persisted.source,
		AuthIndex:       persisted.authIndex,
		MachineID:       persisted.machineID,
		ReasoningEffort: persisted.reasoningEffort,
		Tokens:          persisted.tokens,
		Failed:          persisted.failed,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stats := ensureAPIStats(s, persisted.apiKey)
	s.recordImportedLocked(persisted.apiKey, persisted.model, stats, detail)
}

// Snapshot returns a copy of current in-memory statistics.
func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	result := StatisticsSnapshot{APIs: make(map[string]APISnapshot)}
	if s == nil {
		return result
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result.TotalRequests = s.totalRequests
	result.SuccessCount = s.successCount
	result.FailureCount = s.failureCount
	result.TotalTokens = s.totalTokens
	result.APIs = make(map[string]APISnapshot, len(s.apis))
	for apiName, stats := range s.apis {
		if stats == nil {
			continue
		}
		apiSnapshot := APISnapshot{
			TotalRequests: stats.TotalRequests,
			TotalTokens:   stats.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(stats.Models)),
		}
		for modelName, modelValue := range stats.Models {
			if modelValue == nil {
				continue
			}
			details := make([]RequestDetail, len(modelValue.Details))
			copy(details, modelValue.Details)
			apiSnapshot.Models[modelName] = ModelSnapshot{
				TotalRequests: modelValue.TotalRequests,
				TotalTokens:   modelValue.TotalTokens,
				Details:       details,
			}
		}
		result.APIs[apiName] = apiSnapshot
	}
	return result
}

func (s *RequestStatistics) recordImportedLocked(apiName, modelName string, stats *apiStats, detail RequestDetail) {
	totalTokens := detail.Tokens.TotalTokens
	if totalTokens < 0 {
		totalTokens = 0
	}
	s.totalRequests++
	if detail.Failed {
		s.failureCount++
	} else {
		s.successCount++
	}
	s.totalTokens += totalTokens

	stats.TotalRequests++
	stats.TotalTokens += totalTokens
	if stats.Models == nil {
		stats.Models = make(map[string]*modelStats)
	}
	modelValue := stats.Models[modelName]
	if modelValue == nil {
		modelValue = &modelStats{}
		stats.Models[modelName] = modelValue
	}
	modelValue.TotalRequests++
	modelValue.TotalTokens += totalTokens
	modelValue.Details = append(modelValue.Details, detail)
}

type inMemoryStatisticsBackend struct {
	stats *RequestStatistics
}

// NewInMemoryStatisticsBackend wraps RequestStatistics in StatisticsBackend.
func NewInMemoryStatisticsBackend(stats *RequestStatistics) StatisticsBackend {
	if stats == nil {
		stats = NewRequestStatistics()
	}
	return &inMemoryStatisticsBackend{stats: stats}
}

func (b *inMemoryStatisticsBackend) Record(ctx context.Context, record coreusage.Record) error {
	if b == nil || b.stats == nil {
		return nil
	}
	b.stats.Record(ctx, record)
	return nil
}

func (b *inMemoryStatisticsBackend) Snapshot(context.Context) (StatisticsSnapshot, error) {
	if b == nil || b.stats == nil {
		return StatisticsSnapshot{APIs: map[string]APISnapshot{}}, nil
	}
	return b.stats.Snapshot(), nil
}

func ensureAPIStats(stats *RequestStatistics, apiName string) *apiStats {
	if stats.apis == nil {
		stats.apis = make(map[string]*apiStats)
	}
	apiValue := stats.apis[apiName]
	if apiValue == nil {
		apiValue = &apiStats{Models: make(map[string]*modelStats)}
		stats.apis[apiName] = apiValue
	} else if apiValue.Models == nil {
		apiValue.Models = make(map[string]*modelStats)
	}
	return apiValue
}

func normaliseDetail(detail coreusage.Detail) TokenStats {
	tokens := TokenStats{
		InputTokens:     detail.InputTokens,
		OutputTokens:    detail.OutputTokens,
		ReasoningTokens: detail.ReasoningTokens,
		CachedTokens:    detail.CachedTokens,
		TotalTokens:     detail.TotalTokens,
	}
	return normaliseTokenStats(tokens)
}

func normaliseTokenStats(tokens TokenStats) TokenStats {
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}
	return tokens
}

func normaliseLatency(latency time.Duration) int64 {
	if latency <= 0 {
		return 0
	}
	return latency.Milliseconds()
}

func resolveAPIIdentifier(_ context.Context, record coreusage.Record) string {
	if apiKey := strings.TrimSpace(record.APIKey); apiKey != "" {
		return apiKey
	}
	if provider := strings.TrimSpace(record.Provider); provider != "" {
		return provider
	}
	return "unknown"
}

func resolveSuccess(_ context.Context) bool {
	return true
}

func normalizeModelName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown"
	}
	return model
}

func dedupKey(apiName, modelName string, detail RequestDetail) string {
	timestamp := detail.Timestamp.UTC().Format(time.RFC3339Nano)
	tokens := normaliseTokenStats(detail.Tokens)
	return fmt.Sprintf(
		"%s|%s|%s|%s|%s|%s|%t|%d|%d|%d|%d|%d",
		apiName,
		modelName,
		timestamp,
		detail.Source,
		detail.AuthIndex,
		detail.MachineID,
		detail.Failed,
		tokens.InputTokens,
		tokens.OutputTokens,
		tokens.ReasoningTokens,
		tokens.CachedTokens,
		tokens.TotalTokens,
	)
}
