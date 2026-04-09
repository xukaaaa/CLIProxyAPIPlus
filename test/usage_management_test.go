package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func newUsageTestHandler(t *testing.T) (*management.Handler, *usage.RequestStatistics) {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8080\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	h := management.NewHandler(&config.Config{}, configPath, nil)
	stats := usage.NewRequestStatistics()
	h.SetUsageStatistics(usage.NewInMemoryStatisticsBackend(stats))
	return h, stats
}

func setupUsageRouter(h *management.Handler) *gin.Engine {
	r := gin.New()
	mgmt := r.Group("/v0/management")
	{
		mgmt.GET("/usage", h.GetUsageStatistics)
		mgmt.GET("/usage/export", h.ExportUsageStatistics)
		mgmt.POST("/usage/import", h.ImportUsageStatistics)
	}
	return r
}

func seedUsageRecord(t *testing.T, stats *usage.RequestStatistics) {
	t.Helper()
	stats.Record(context.Background(), coreusage.Record{
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
	})
}

func TestGetUsageStatisticsReturnsCurrentSchema(t *testing.T) {
	h, stats := newUsageTestHandler(t)
	seedUsageRecord(t, stats)
	r := setupUsageRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp struct {
		Usage struct {
			TotalRequests int64 `json:"total_requests"`
			SuccessCount  int64 `json:"success_count"`
			FailureCount  int64 `json:"failure_count"`
			TotalTokens   int64 `json:"total_tokens"`
			APIs          map[string]struct {
				TotalRequests int64 `json:"total_requests"`
				TotalTokens   int64 `json:"total_tokens"`
				Models        map[string]struct {
					TotalRequests int64 `json:"total_requests"`
					TotalTokens   int64 `json:"total_tokens"`
					Details       []struct {
						Timestamp time.Time `json:"timestamp"`
						LatencyMs int64     `json:"latency_ms"`
						Source    string    `json:"source"`
						AuthIndex string    `json:"auth_index"`
						MachineID string    `json:"machine_id"`
						Tokens    struct {
							InputTokens     int64 `json:"input_tokens"`
							OutputTokens    int64 `json:"output_tokens"`
							ReasoningTokens int64 `json:"reasoning_tokens"`
							CachedTokens    int64 `json:"cached_tokens"`
							TotalTokens     int64 `json:"total_tokens"`
						} `json:"tokens"`
						Failed bool `json:"failed"`
					} `json:"details"`
				} `json:"models"`
			} `json:"apis"`
			RequestsByDay  map[string]int64 `json:"requests_by_day"`
			RequestsByHour map[string]int64 `json:"requests_by_hour"`
			TokensByDay    map[string]int64 `json:"tokens_by_day"`
			TokensByHour   map[string]int64 `json:"tokens_by_hour"`
		} `json:"usage"`
		FailedRequests int64 `json:"failed_requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Usage.TotalRequests != 1 {
		t.Fatalf("expected total_requests 1, got %d", resp.Usage.TotalRequests)
	}
	if resp.Usage.SuccessCount != 1 {
		t.Fatalf("expected success_count 1, got %d", resp.Usage.SuccessCount)
	}
	if resp.Usage.FailureCount != 0 {
		t.Fatalf("expected failure_count 0, got %d", resp.Usage.FailureCount)
	}
	if resp.FailedRequests != resp.Usage.FailureCount {
		t.Fatalf("expected failed_requests mirror failure_count, got %d vs %d", resp.FailedRequests, resp.Usage.FailureCount)
	}

	api, ok := resp.Usage.APIs["test-api-key"]
	if !ok {
		t.Fatal("expected api entry for Thuyanh1110")
	}
	model, ok := api.Models["accounts/fireworks/models/qwen3p6-plus"]
	if !ok {
		t.Fatal("expected model entry for accounts/fireworks/models/qwen3p6-plus")
	}
	if len(model.Details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(model.Details))
	}
	if model.Details[0].LatencyMs != 12251 {
		t.Fatalf("expected latency_ms 12251, got %d", model.Details[0].LatencyMs)
	}
	if model.Details[0].MachineID != "" {
		t.Fatalf("expected empty machine_id for in-memory /usage detail, got %q", model.Details[0].MachineID)
	}
	if model.Details[0].Tokens.TotalTokens != 159016 {
		t.Fatalf("expected total_tokens 159016, got %d", model.Details[0].Tokens.TotalTokens)
	}
}

func TestExportUsageStatisticsReturnsCurrentSchema(t *testing.T) {
	h, stats := newUsageTestHandler(t)
	seedUsageRecord(t, stats)
	r := setupUsageRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/export", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp struct {
		Version    int       `json:"version"`
		ExportedAt time.Time `json:"exported_at"`
		Usage      struct {
			TotalRequests int64 `json:"total_requests"`
			APIs          map[string]struct {
				Models map[string]struct {
					Details []json.RawMessage `json:"details"`
				} `json:"models"`
			} `json:"apis"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Version != 1 {
		t.Fatalf("expected version 1, got %d", resp.Version)
	}
	if resp.ExportedAt.IsZero() {
		t.Fatal("expected exported_at to be set")
	}
	if resp.Usage.TotalRequests != 1 {
		t.Fatalf("expected total_requests 1, got %d", resp.Usage.TotalRequests)
	}
	if got := len(resp.Usage.APIs["test-api-key"].Models["accounts/fireworks/models/qwen3p6-plus"].Details); got != 1 {
		t.Fatalf("expected 1 exported detail, got %d", got)
	}
}

func TestImportUsageStatisticsReturnsAddedSkippedAndTotals(t *testing.T) {
	h, _ := newUsageTestHandler(t)
	r := setupUsageRouter(h)

	body := `{
		"version": 1,
		"usage": {
			"apis": {
				"test-api-key": {
					"models": {
						"accounts/fireworks/models/qwen3p6-plus": {
							"details": [
								{
									"timestamp": "2026-04-08T17:08:34.673916705+07:00",
									"latency_ms": 12251,
									"source": "fw_5SnYmbdUKHuZC5ZePi4g5n",
									"auth_index": "c1738e197b189fd3",
									"tokens": {
										"input_tokens": 158985,
										"output_tokens": 31,
										"reasoning_tokens": 0,
										"cached_tokens": 0,
										"total_tokens": 159016
									},
									"failed": false
								}
							]
						}
					}
				}
			}
		}
	}`

	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp struct {
		Added          int64 `json:"added"`
		Skipped        int64 `json:"skipped"`
		TotalRequests  int64 `json:"total_requests"`
		FailedRequests int64 `json:"failed_requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Added != 1 {
		t.Fatalf("expected added 1, got %d", resp.Added)
	}
	if resp.Skipped != 0 {
		t.Fatalf("expected skipped 0, got %d", resp.Skipped)
	}
	if resp.TotalRequests != 1 {
		t.Fatalf("expected total_requests 1, got %d", resp.TotalRequests)
	}
	if resp.FailedRequests != 0 {
		t.Fatalf("expected failed_requests 0, got %d", resp.FailedRequests)
	}
}

func TestGetUsageStatisticsWithPostgresBackendReturnsCurrentSchema(t *testing.T) {
	dsn := os.Getenv("PGSTORE_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_DSN not set")
	}

	h, _ := newUsageTestHandler(t)
	tableName := "test_usage_handler_" + time.Now().UTC().Format("20060102150405.000000000")
	store, err := usage.NewPostgresUsageStore(context.Background(), usage.PostgresUsageStoreConfig{
		DSN:       dsn,
		TableName: tableName,
	})
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	defer func() {
		store.DB().ExecContext(context.Background(), "DROP TABLE IF EXISTS "+store.FullTableName(tableName))
		store.Close()
	}()
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if err := store.Record(context.Background(), coreusage.Record{
		APIKey:      "test-api-key",
		Model:       "accounts/fireworks/models/qwen3p6-plus",
		RequestedAt: time.Date(2026, 4, 8, 10, 8, 34, 0, time.UTC),
		Latency:     12251 * time.Millisecond,
		Source:      "fw_5SnYmbdUKHuZC5ZePi4g5n",
		AuthIndex:   "c1738e197b189fd3",
		Detail: coreusage.Detail{InputTokens: 158985, OutputTokens: 31, TotalTokens: 159016},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	h.SetUsageStatistics(store)
	r := setupUsageRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	failedRequestsRaw, ok := resp["failed_requests"]
	if !ok {
		t.Fatal("expected failed_requests field")
	}
	var failedRequests int64
	if err := json.Unmarshal(failedRequestsRaw, &failedRequests); err != nil {
		t.Fatalf("failed to unmarshal failed_requests: %v", err)
	}
	if failedRequests != 0 {
		t.Fatalf("expected failed_requests 0, got %d", failedRequests)
	}
	var usageBody struct {
		APIs map[string]struct {
			Models map[string]struct {
				Details []struct {
					MachineID string `json:"machine_id"`
				} `json:"details"`
			} `json:"models"`
		} `json:"apis"`
	}
	if err := json.Unmarshal(resp["usage"], &usageBody); err != nil {
		t.Fatalf("failed to unmarshal usage: %v", err)
	}
	details := usageBody.APIs["test-api-key"].Models["accounts/fireworks/models/qwen3p6-plus"].Details
	if len(details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(details))
	}
	if details[0].MachineID != store.MachineID() {
		t.Fatalf("expected machine_id %q, got %q", store.MachineID(), details[0].MachineID)
	}
}
