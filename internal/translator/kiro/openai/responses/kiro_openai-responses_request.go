// Package responses provides request translation from OpenAI Responses API to Kiro format.
package responses

import (
	"bytes"

	claudeResponses "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/claude/openai/responses"
)

// ConvertOpenAIResponsesRequestToKiro transforms an OpenAI Responses API request into Kiro (Claude) format.
// Kiro uses Claude-compatible format internally, so we delegate to the Claude translator.
func ConvertOpenAIResponsesRequestToKiro(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)
	return claudeResponses.ConvertOpenAIResponsesRequestToClaude(modelName, rawJSON, stream)
}
