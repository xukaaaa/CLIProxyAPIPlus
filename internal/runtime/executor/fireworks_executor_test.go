package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestFireworksExecutorExecuteBuildsMessagesRequest(t *testing.T) {
	var gotPath string
	var gotQuery string
	var gotAuth string
	var gotXAPIKey string
	var gotBody []byte
	var gotAccept string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotXAPIKey = r.Header.Get("x-api-key")
		gotAccept = r.Header.Get("Accept")
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"accounts/fireworks/models/kimi-k2p7-code","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewFireworksExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "fw-test",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"accounts/fireworks/models/kimi-k2p7-code","max_tokens":4097,"messages":[{"role":"user","content":"hi"}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "accounts/fireworks/models/kimi-k2p7-code",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("path = %q, want /v1/messages", gotPath)
	}
	if gotQuery != "" {
		t.Fatalf("query = %q, want empty", gotQuery)
	}
	if gotAuth != "Bearer fw-test" {
		t.Fatalf("Authorization = %q, want bearer", gotAuth)
	}
	if gotXAPIKey != "" {
		t.Fatalf("x-api-key = %q, want empty", gotXAPIKey)
	}
	if bytes.Contains(gotBody, []byte(`"stream":true`)) {
		t.Fatalf("non-stream request forced stream=true: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "max_tokens").Int(); got != 4097 {
		t.Fatalf("max_tokens = %d, want 4097; body=%s", got, string(gotBody))
	}
	if gotAccept == "text/event-stream" {
		t.Fatalf("Accept = %q, non-stream should not request SSE", gotAccept)
	}
	if got := gjson.GetBytes(resp.Payload, "content.0.text").String(); got != "ok" {
		t.Fatalf("response text = %q, want ok; payload=%s", got, string(resp.Payload))
	}
}

func TestFireworksExecutionSessionIDPrefersExecutionMetadata(t *testing.T) {
	req := cliproxyexecutor.Request{
		Payload: []byte(`{"prompt_cache_key":"payload-cache"}`),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "req-session",
		},
	}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "opts-session",
	}}

	if got := fireworksExecutionSessionID(req, opts); got != "opts-session" {
		t.Fatalf("session ID = %q, want opts-session", got)
	}
}

func TestFireworksExecutionSessionIDFallsBackToClaudeMetadataUserID(t *testing.T) {
	req := cliproxyexecutor.Request{Payload: []byte(`{"metadata":{"user_id":"{\"session_id\":\"claude-session\"}"},"prompt_cache_key":"payload-cache"}`)}

	if got := fireworksExecutionSessionID(req, cliproxyexecutor.Options{}); got != "claude-session" {
		t.Fatalf("session ID = %q, want claude-session", got)
	}
}

func TestFireworksExecutionSessionIDFallsBackToClaudeSessionSuffix(t *testing.T) {
	req := cliproxyexecutor.Request{Payload: []byte(`{"metadata":{"user_id":"user_abc_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"},"prompt_cache_key":"payload-cache"}`)}

	if got := fireworksExecutionSessionID(req, cliproxyexecutor.Options{}); got != "ac980658-63bd-4fb3-97ba-8da64cb1e344" {
		t.Fatalf("session ID = %q, want Claude session suffix", got)
	}
}

func TestFireworksExecutionSessionIDFallsBackToPromptCacheKey(t *testing.T) {
	req := cliproxyexecutor.Request{Payload: []byte(`{"prompt_cache_key":"payload-cache"}`)}

	if got := fireworksExecutionSessionID(req, cliproxyexecutor.Options{}); got != "payload-cache" {
		t.Fatalf("session ID = %q, want payload-cache", got)
	}
}

func TestFireworksExecutorExecuteSetsSessionAffinityHeader(t *testing.T) {
	var gotSessionAffinity string
	var gotIsolationKey string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSessionAffinity = r.Header.Get("x-session-affinity")
		gotIsolationKey = r.Header.Get("x-prompt-cache-isolation-key")
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"accounts/fireworks/models/kimi-k2p7-code","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewFireworksExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "fw-test",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"accounts/fireworks/models/kimi-k2p7-code","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "accounts/fireworks/models/kimi-k2p7-code",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "fw-session-1",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotSessionAffinity != "fw-session-1" {
		t.Fatalf("x-session-affinity = %q, want fw-session-1", gotSessionAffinity)
	}
	if gotIsolationKey != "" {
		t.Fatalf("x-prompt-cache-isolation-key = %q, want empty", gotIsolationKey)
	}
	if gjson.GetBytes(gotBody, "prompt_cache_isolation_key").Exists() {
		t.Fatalf("body should not include prompt_cache_isolation_key: %s", string(gotBody))
	}
	if bytes.Contains(gotBody, []byte("cache_control")) {
		t.Fatalf("body should not include cache_control: %s", string(gotBody))
	}
}

func TestFireworksExecutorExecuteStreamSetsSessionAffinityHeader(t *testing.T) {
	var gotSessionAffinity string
	var gotIsolationKey string
	var gotAccept string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSessionAffinity = r.Header.Get("x-session-affinity")
		gotIsolationKey = r.Header.Get("x-prompt-cache-isolation-key")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n"))
	}))
	defer server.Close()

	executor := NewFireworksExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "fw-test",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"accounts/fireworks/models/kimi-k2p7-code","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":true}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "accounts/fireworks/models/kimi-k2p7-code",
		Payload: payload,
	}, cliproxyexecutor.Options{
		Stream:       true,
		SourceFormat: sdktranslator.FromString("claude"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "fw-stream-session-1",
		},
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
	if gotSessionAffinity != "fw-stream-session-1" {
		t.Fatalf("x-session-affinity = %q, want fw-stream-session-1", gotSessionAffinity)
	}
	if gotAccept != "text/event-stream" {
		t.Fatalf("Accept = %q, want text/event-stream", gotAccept)
	}
	if gotIsolationKey != "" {
		t.Fatalf("x-prompt-cache-isolation-key = %q, want empty", gotIsolationKey)
	}
}

func TestFireworksExecutorPrepareRequestUsesBearerOnly(t *testing.T) {
	executor := NewFireworksExecutor(&config.Config{})
	req := httptest.NewRequest(http.MethodPost, "https://api.fireworks.ai/inference/v1/messages", nil)
	req.Header.Set("x-api-key", "old")
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "fw-test"}}

	if err := executor.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer fw-test" {
		t.Fatalf("Authorization = %q, want bearer", got)
	}
	if got := req.Header.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key = %q, want empty", got)
	}
}

func TestFireworksExecutorPriorityModelAddsServiceTier(t *testing.T) {
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"accounts/fireworks/models/kimi-k2p7-code","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewFireworksExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "fw-test",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"accounts/fireworks/models/kimi-k2p7-code-priority","max_tokens":4097,"messages":[{"role":"user","content":"hi"}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "accounts/fireworks/models/kimi-k2p7-code-priority",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(gotBody, "model").String(); got != "accounts/fireworks/models/kimi-k2p7-code" {
		t.Fatalf("model = %q, want accounts/fireworks/models/kimi-k2p7-code; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want priority; body=%s", got, string(gotBody))
	}
}

func TestFireworksExecutorPriorityModelStreamAddsServiceTier(t *testing.T) {
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n"))
	}))
	defer server.Close()

	executor := NewFireworksExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "fw-test",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"accounts/fireworks/models/kimi-k2p7-code-priority","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":true}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "accounts/fireworks/models/kimi-k2p7-code-priority",
		Payload: payload,
	}, cliproxyexecutor.Options{
		Stream:       true,
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}

	if got := gjson.GetBytes(gotBody, "model").String(); got != "accounts/fireworks/models/kimi-k2p7-code" {
		t.Fatalf("model = %q, want accounts/fireworks/models/kimi-k2p7-code; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want priority; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "stream").Bool(); !got {
		t.Fatalf("stream = %v, want true; body=%s", got, string(gotBody))
	}
}

func TestFireworksExecutorExecuteUsesMetadataCredentials(t *testing.T) {
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"accounts/fireworks/models/kimi-k2p7-code","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewFireworksExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{},
		Metadata: map[string]any{
			"api_key":  "fw-meta",
			"base_url": server.URL,
		},
	}
	payload := []byte(`{"model":"accounts/fireworks/models/kimi-k2p7-code","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "accounts/fireworks/models/kimi-k2p7-code",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotAuth != "Bearer fw-meta" {
		t.Fatalf("Authorization = %q, want Bearer fw-meta", gotAuth)
	}
}
