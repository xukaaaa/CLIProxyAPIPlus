// Package responses provides response translation from Kiro to OpenAI Responses API format.
package responses

import (
	"context"
	"strings"

	claudeResponses "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/claude/openai/responses"
)

// ConvertKiroResponseToOpenAIResponses converts Kiro streaming response to OpenAI Responses API SSE format.
// Kiro executor generates Claude-compatible SSE format with "event:" prefix (e.g., "event: message_start\ndata: {...}").
// We need to extract the "data:" line and pass it to the Claude translator which expects "data: {...}" format.
func ConvertKiroResponseToOpenAIResponses(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) []string {
	raw := string(rawResponse)
	var results []string

	// Handle SSE format: extract "data:" lines from "event: xxx\ndata: {...}" format
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			// Pass the data line to Claude translator (it expects "data: {...}" format)
			dataLine := []byte(line)
			chunks := claudeResponses.ConvertClaudeResponseToOpenAIResponses(ctx, model, originalRequest, request, dataLine, param)
			results = append(results, chunks...)
		}
	}

	return results
}

// ConvertKiroResponseToOpenAIResponsesNonStream converts Kiro non-streaming response to OpenAI Responses API format.
func ConvertKiroResponseToOpenAIResponsesNonStream(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) string {
	return claudeResponses.ConvertClaudeResponseToOpenAIResponsesNonStream(ctx, model, originalRequest, request, rawResponse, param)
}
