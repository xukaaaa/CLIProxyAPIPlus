package helps

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// LogUsageTraceRequest emits a debug log of request fields that affect usage.
func LogUsageTraceRequest(ctx context.Context, provider, model, baseModel, serviceTier string, stream bool, body []byte, targetFormat string) {
	if !log.IsLevelEnabled(log.DebugLevel) {
		return
	}
	LogWithRequestID(ctx).WithFields(log.Fields{
		"provider":         provider,
		"model":            model,
		"base_model":       baseModel,
		"max_tokens":       firstInt64(body, "max_tokens", "max_output_tokens", "request.generationConfig.maxOutputTokens", "generationConfig.maxOutputTokens"),
		"thinking_type":    firstString(body, "thinking.type", "reasoning.effort", "reasoning_effort", "request.generationConfig.thinkingConfig.thinkingLevel", "generationConfig.thinkingConfig.thinkingLevel"),
		"thinking_budget":  firstInt64(body, "thinking.budget_tokens", "request.generationConfig.thinkingConfig.thinkingBudget", "generationConfig.thinkingConfig.thinkingBudget"),
		"stream":           stream,
		"reasoning_effort": thinking.ExtractTranslatedReasoningEffort(body, targetFormat),
		"service_tier":     serviceTier,
	}).Debug("usage trace: request")
}

// LogUsageTraceParsed emits a debug log of parsed usage details.
func LogUsageTraceParsed(ctx context.Context, provider, model string, detail usage.Detail, usageChunks int) {
	if !log.IsLevelEnabled(log.DebugLevel) {
		return
	}
	LogWithRequestID(ctx).WithFields(log.Fields{
		"provider":         provider,
		"model":            model,
		"input_tokens":     detail.InputTokens,
		"output_tokens":    detail.OutputTokens,
		"reasoning_tokens": detail.ReasoningTokens,
		"total_tokens":     detail.TotalTokens,
		"usage_found":      hasNonZeroTokenUsage(detail),
		"usage_chunks":     usageChunks,
	}).Debug("usage trace: parsed")
}

func firstInt64(body []byte, paths ...string) int64 {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return 0
	}
	for _, p := range paths {
		v := gjson.GetBytes(body, p)
		if v.Exists() {
			return v.Int()
		}
	}
	return 0
}

func firstString(body []byte, paths ...string) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	for _, p := range paths {
		v := gjson.GetBytes(body, p)
		if v.Exists() {
			return v.String()
		}
	}
	return ""
}
