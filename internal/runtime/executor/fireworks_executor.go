package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const defaultFireworksBaseURL = "https://api.fireworks.ai/inference"

var fireworksClaudeCodeSessionPattern = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

type FireworksExecutor struct {
	cfg *config.Config
}

func NewFireworksExecutor(cfg *config.Config) *FireworksExecutor { return &FireworksExecutor{cfg: cfg} }

func (e *FireworksExecutor) Identifier() string { return "fireworks" }

func (e *FireworksExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey := helps.FireworksCreds(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Del("x-api-key")
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

func (e *FireworksExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("fireworks executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *FireworksExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel, serviceTier := fireworksNormalizePriorityModel(thinking.ParseSuffix(req.Model).ModelName)
	baseURL, _ := helps.FireworksCreds(auth)
	if baseURL == "" {
		baseURL = defaultFireworksBaseURL
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("claude")
	// Fireworks requires stream=true when max_tokens > 5000 and the claude->openai
	// non-stream translators only parse SSE. For claude-format clients force
	// upstream streaming and aggregate the SSE back into a single message so a
	// non-stream claude client still gets a normal non-stream response.
	stream := opts.Stream || from == to
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, stream)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, stream)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	if serviceTier != "" {
		body, _ = sjson.SetBytes(body, "service_tier", serviceTier)
	}
	if stream {
		body, _ = sjson.SetBytes(body, "stream", true)
	}

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	reporter.SetTranslatedReasoningEffort(body, to.String())
	helps.LogUsageTraceRequest(ctx, e.Identifier(), req.Model, baseModel, serviceTier, stream, body, to.String())

	url := strings.TrimSuffix(baseURL, "/") + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	if sessionID := fireworksExecutionSessionID(req, opts); sessionID != "" {
		httpReq.Header.Set("x-session-affinity", sessionID)
	}
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return resp, errPrepare
	}
	recordFireworksRequest(ctx, e.cfg, e.Identifier(), auth, url, httpReq.Header.Clone(), body)

	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return resp, fireworksStatusError(ctx, e.cfg, httpResp)
	}
	decodedBody, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return resp, err
	}
	defer func() {
		if errClose := decodedBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()
	data, err := io.ReadAll(decodedBody)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	var usageDetail cliproxyusage.Detail
	if stream {
		if errValidate := validateClaudeStreamingResponse(data); errValidate != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errValidate)
			return resp, errValidate
		}
		for _, line := range bytes.Split(data, []byte("\n")) {
			if detail, ok := helps.ParseFireworksClaudeStreamUsage(line); ok {
				usageDetail = detail
				reporter.Publish(ctx, detail)
			}
		}
	} else {
		usageDetail = helps.ParseClaudeUsage(data)
		reporter.Publish(ctx, usageDetail)
	}
	helps.LogUsageTraceParsed(ctx, e.Identifier(), req.Model, usageDetail, 0)
	var out []byte
	if from == to {
		out = helps.AggregateClaudeSSEToMessage(data)
	} else {
		var param any
		out = sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, data, &param)
	}
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *FireworksExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel, serviceTier := fireworksNormalizePriorityModel(thinking.ParseSuffix(req.Model).ModelName)
	baseURL, _ := helps.FireworksCreds(auth)
	if baseURL == "" {
		baseURL = defaultFireworksBaseURL
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("claude")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, true)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.SetBytes(body, "stream", true)
	if serviceTier != "" {
		body, _ = sjson.SetBytes(body, "service_tier", serviceTier)
	}

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	reporter.SetTranslatedReasoningEffort(body, to.String())
	helps.LogUsageTraceRequest(ctx, e.Identifier(), req.Model, baseModel, serviceTier, true, body, to.String())

	url := strings.TrimSuffix(baseURL, "/") + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	if sessionID := fireworksExecutionSessionID(req, opts); sessionID != "" {
		httpReq.Header.Set("x-session-affinity", sessionID)
	}
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	recordFireworksRequest(ctx, e.cfg, e.Identifier(), auth, url, httpReq.Header.Clone(), body)

	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		err = fireworksStatusError(ctx, e.cfg, httpResp)
		return nil, err
	}
	decodedBody, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := decodedBody.Close(); errClose != nil {
				log.Errorf("response body close error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(decodedBody)
		scanner.Buffer(nil, 52_428_800)
		var param any
		var usageDetail cliproxyusage.Detail
		usageChunks := 0
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseFireworksClaudeStreamUsage(line); ok {
				usageDetail = detail
				usageChunks++
				reporter.Publish(ctx, detail)
			}
			clonedLine := bytes.Clone(line)
			if responseFormat == to {
				cloned := make([]byte, len(clonedLine)+1)
				copy(cloned, clonedLine)
				cloned[len(clonedLine)] = '\n'
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: cloned}:
				case <-ctx.Done():
					return
				}
				continue
			}
			chunks := sdktranslator.TranslateStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, clonedLine, &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
		helps.LogUsageTraceParsed(ctx, e.Identifier(), req.Model, usageDetail, usageChunks)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *FireworksExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel, _ := fireworksNormalizePriorityModel(thinking.ParseSuffix(req.Model).ModelName)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)
	translated, _ = sjson.SetBytes(translated, "model", baseModel)

	enc, err := helps.TokenizerForModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("fireworks executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("fireworks executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

func (e *FireworksExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("fireworks executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

const fireworksPriorityModelSuffix = "-priority"

// fireworksNormalizePriorityModel handles the virtual "-priority" Fireworks model
// convention: a model name ending in "-priority" maps to the base model and adds
// service_tier="priority" to the upstream request.
func fireworksNormalizePriorityModel(model string) (string, string) {
	if strings.HasSuffix(model, fireworksPriorityModelSuffix) {
		return strings.TrimSuffix(model, fireworksPriorityModelSuffix), "priority"
	}
	return model, ""
}

func fireworksExecutionSessionID(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	if value := fireworksMetadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return value
	}
	if value := fireworksMetadataString(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return value
	}
	if value := fireworksClaudeCodeSessionID(req.Payload); value != "" {
		return value
	}
	if promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key"); promptCacheKey.Exists() {
		return strings.TrimSpace(promptCacheKey.String())
	}
	return ""
}

func fireworksClaudeCodeSessionID(payload []byte) string {
	userID := strings.TrimSpace(gjson.GetBytes(payload, "metadata.user_id").String())
	if userID == "" {
		return ""
	}
	if matches := fireworksClaudeCodeSessionPattern.FindStringSubmatch(userID); len(matches) >= 2 {
		return matches[1]
	}
	if userID[0] != '{' {
		return ""
	}
	return strings.TrimSpace(gjson.Get(userID, "session_id").String())
}

func fireworksMetadataString(meta map[string]any, key string) string {
	if len(meta) == 0 || key == "" {
		return ""
	}
	value, ok := meta[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func recordFireworksRequest(ctx context.Context, cfg *config.Config, provider string, auth *cliproxyauth.Auth, url string, headers http.Header, body []byte) {
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   headers,
		Body:      body,
		Provider:  provider,
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

func fireworksStatusError(ctx context.Context, cfg *config.Config, httpResp *http.Response) error {
	decodedBody, decErr := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if decErr != nil {
		helps.RecordAPIResponseError(ctx, cfg, decErr)
		msg := fmt.Sprintf("failed to decode error response body: %v", decErr)
		helps.LogWithRequestID(ctx).Warn(msg)
		return statusErr{code: httpResp.StatusCode, msg: msg}
	}
	b, readErr := io.ReadAll(decodedBody)
	if readErr != nil {
		helps.RecordAPIResponseError(ctx, cfg, readErr)
		msg := fmt.Sprintf("failed to read error response body: %v", readErr)
		helps.LogWithRequestID(ctx).Warn(msg)
		b = []byte(msg)
	}
	helps.AppendAPIResponseChunk(ctx, cfg, b)
	helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
	if errClose := decodedBody.Close(); errClose != nil {
		log.Errorf("response body close error: %v", errClose)
	}
	return statusErr{code: httpResp.StatusCode, msg: string(b)}
}
