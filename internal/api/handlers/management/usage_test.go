package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestGetUsageStatisticsReturnsSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stats := withManagementUsageStatistics(t)
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-api-key",
		Model:       "gpt-5.5",
		Source:      "user@example.test",
		AuthIndex:   "auth-index-1",
		RequestedAt: time.Date(2026, 5, 26, 16, 2, 52, 553653000, time.UTC),
		Latency:     1500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:     100,
			OutputTokens:    20,
			ReasoningTokens: 5,
			CachedTokens:    80,
			TotalTokens:     125,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-api-key",
		Model:       "gpt-5.5",
		Source:      "user@example.test",
		AuthIndex:   "auth-index-1",
		RequestedAt: time.Date(2026, 5, 26, 16, 3, 0, 0, time.UTC),
		Failed:      true,
		Detail:      coreusage.Detail{TotalTokens: 10},
	})

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)

	h := &Handler{}
	h.GetUsageStatistics(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Usage          usage.StatisticsSnapshot `json:"usage"`
		FailedRequests int64                    `json:"failed_requests"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if payload.Usage.TotalRequests != 2 || payload.Usage.SuccessCount != 1 || payload.Usage.FailureCount != 1 {
		t.Fatalf("usage totals = requests:%d success:%d failure:%d", payload.Usage.TotalRequests, payload.Usage.SuccessCount, payload.Usage.FailureCount)
	}
	if payload.FailedRequests != payload.Usage.FailureCount {
		t.Fatalf("failed_requests = %d, want %d", payload.FailedRequests, payload.Usage.FailureCount)
	}
	apiSnapshot, ok := payload.Usage.APIs["test-api-key"]
	if !ok {
		t.Fatalf("missing API bucket: %#v", payload.Usage.APIs)
	}
	modelSnapshot, ok := apiSnapshot.Models["gpt-5.5"]
	if !ok {
		t.Fatalf("missing model bucket: %#v", apiSnapshot.Models)
	}
	if len(modelSnapshot.Details) != 2 {
		t.Fatalf("details = %d, want 2", len(modelSnapshot.Details))
	}
	if modelSnapshot.Details[0].Source != "user@example.test" || modelSnapshot.Details[0].Tokens.TotalTokens != 125 {
		t.Fatalf("first detail = %#v", modelSnapshot.Details[0])
	}
}

func TestGetUsageQueuePopsRequestedRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withManagementUsageQueue(t, func() {
		redisqueue.Enqueue([]byte(`{"id":1}`))
		redisqueue.Enqueue([]byte(`{"id":2}`))
		redisqueue.Enqueue([]byte(`{"id":3}`))

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)

		h := &Handler{}
		h.GetUsageQueue(ginCtx)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var payload []json.RawMessage
		if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
			t.Fatalf("unmarshal response: %v", errUnmarshal)
		}
		if len(payload) != 2 {
			t.Fatalf("response records = %d, want 2", len(payload))
		}
		requireRecordID(t, payload[0], 1)
		requireRecordID(t, payload[1], 2)

		remaining := redisqueue.PopOldest(10)
		if len(remaining) != 1 || string(remaining[0]) != `{"id":3}` {
			t.Fatalf("remaining queue = %q, want third item only", remaining)
		}
	})
}

func TestGetUsageQueueInvalidCountDoesNotPop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withManagementUsageQueue(t, func() {
		redisqueue.Enqueue([]byte(`{"id":1}`))

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=0", nil)

		h := &Handler{}
		h.GetUsageQueue(ginCtx)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}

		remaining := redisqueue.PopOldest(10)
		if len(remaining) != 1 || string(remaining[0]) != `{"id":1}` {
			t.Fatalf("remaining queue = %q, want original item", remaining)
		}
	})
}

func withManagementUsageQueue(t *testing.T, fn func()) {
	t.Helper()

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)

	defer func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	}()

	fn()
}

func withManagementUsageStatistics(t *testing.T) *usage.RequestStatistics {
	t.Helper()

	prevBackend := usage.GetStatisticsBackend()
	stats := usage.NewRequestStatistics()
	usage.SetStatisticsBackend(usage.NewInMemoryStatisticsBackend(stats))
	t.Cleanup(func() {
		usage.SetStatisticsBackend(prevBackend)
	})
	return stats
}

func requireRecordID(t *testing.T, raw json.RawMessage, want int) {
	t.Helper()

	var payload struct {
		ID int `json:"id"`
	}
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal record: %v", errUnmarshal)
	}
	if payload.ID != want {
		t.Fatalf("record id = %d, want %d", payload.ID, want)
	}
}
