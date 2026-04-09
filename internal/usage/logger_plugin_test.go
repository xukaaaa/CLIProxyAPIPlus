package usage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestRequestStatisticsRecordIncludesLatency(t *testing.T) {
	stats := NewRequestStatistics()
	timestamp := time.Date(2026, 4, 8, 10, 8, 34, 0, time.UTC)

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-api-key",
		Model:       "accounts/fireworks/models/qwen3p6-plus",
		RequestedAt: timestamp,
		Latency:     12251 * time.Millisecond,
		Source:      "fw_5SnYmbdUKHuZC5ZePi4g5n",
		AuthIndex:   "c1738e197b189fd3",
		Detail: coreusage.Detail{
			InputTokens:  158985,
			OutputTokens: 31,
			TotalTokens:  159016,
		},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-api-key"].Models["accounts/fireworks/models/qwen3p6-plus"].Details
	if len(details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(details))
	}
	if details[0].LatencyMs != 12251 {
		t.Fatalf("expected latency_ms 12251, got %d", details[0].LatencyMs)
	}
}

func TestRequestStatisticsMergeSnapshotDedupIgnoresLatency(t *testing.T) {
	stats := NewRequestStatistics()
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

	result := stats.MergeSnapshot(first)
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("first merge = %+v, want added=1 skipped=0", result)
	}

	result = stats.MergeSnapshot(second)
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("second merge = %+v, want added=0 skipped=1", result)
	}

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(details))
	}
}

func TestRequestStatisticsRecordFallsBackToRouteAndFailureFromContext(t *testing.T) {
	stats := NewRequestStatistics()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(w)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ginCtx.Params = gin.Params{{Key: "path", Value: "messages"}}
	ginCtx.Writer.WriteHeader(http.StatusBadRequest)

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	stats.Record(ctx, coreusage.Record{
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 8, 10, 8, 34, 0, time.UTC),
		Source:      "source-a",
		AuthIndex:   "auth-a",
		Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
	})

	snapshot := stats.Snapshot()
	api, ok := snapshot.APIs["POST /v1/messages"]
	if !ok {
		t.Fatalf("expected fallback API key POST /v1/messages, got %+v", snapshot.APIs)
	}
	model := api.Models["gpt-5.4"]
	if model.TotalRequests != 1 {
		t.Fatalf("expected model total_requests 1, got %d", model.TotalRequests)
	}
	if snapshot.FailureCount != 1 {
		t.Fatalf("expected failure_count 1, got %d", snapshot.FailureCount)
	}
	if snapshot.SuccessCount != 0 {
		t.Fatalf("expected success_count 0, got %d", snapshot.SuccessCount)
	}
	if len(model.Details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(model.Details))
	}
	if !model.Details[0].Failed {
		t.Fatal("expected detail to be marked failed")
	}
}

func TestLoggerPluginUsesSnapshottedFallbackMetadataThroughAsyncManager(t *testing.T) {
	stats := NewRequestStatistics()
	SetStatisticsBackend(NewInMemoryStatisticsBackend(stats))
	defer SetStatisticsBackend(nil)

	manager := coreusage.NewManager(1)
	manager.Register(NewLoggerPlugin())
	manager.Start(context.Background())
	defer manager.Stop()

	manager.Publish(context.Background(), coreusage.Record{
		Model:             "gpt-5.4",
		RequestedAt:       time.Date(2026, 4, 8, 10, 8, 34, 0, time.UTC),
		Source:            "source-a",
		AuthIndex:         "auth-a",
		FallbackAPIKey:    "POST /v1/messages",
		FallbackFailed:    true,
		HasFallbackFailed: true,
		Detail:            coreusage.Detail{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot := stats.Snapshot()
		api, ok := snapshot.APIs["POST /v1/messages"]
		if ok && api.Models["gpt-5.4"].TotalRequests == 1 {
			if snapshot.FailureCount != 1 {
				t.Fatalf("expected failure_count 1, got %d", snapshot.FailureCount)
			}
			if snapshot.SuccessCount != 0 {
				t.Fatalf("expected success_count 0, got %d", snapshot.SuccessCount)
			}
			details := api.Models["gpt-5.4"].Details
			if len(details) != 1 {
				t.Fatalf("expected 1 detail, got %d", len(details))
			}
			if !details[0].Failed {
				t.Fatal("expected async detail to be marked failed")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for async usage record, got %+v", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
