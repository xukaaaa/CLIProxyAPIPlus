// Package quota provides API key quota management functionality.
// It includes policy definitions, usage tracking, and enforcement
// for limiting API key access by model, tokens, cost, and expiration.
package quota

import (
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// Policy is an alias for config.APIKeyPolicy for convenience.
type Policy = config.APIKeyPolicy

// QuotaUsage tracks the cumulative usage for an API key.
type QuotaUsage struct {
	// APIKey is the key this usage belongs to.
	APIKey string `json:"api_key"`

	// TotalTokens is the total tokens consumed (lifetime).
	TotalTokens int64 `json:"total_tokens"`

	// TotalCostUSD is the total cost in USD (lifetime).
	TotalCostUSD float64 `json:"total_cost_usd"`

	// TotalRequests is the total number of requests made.
	TotalRequests int64 `json:"total_requests"`

	// LastUsedAt is the timestamp of the last request.
	LastUsedAt time.Time `json:"last_used_at"`

	// CreatedAt is when this usage record was created.
	CreatedAt time.Time `json:"created_at"`
}

// QuotaErrorType represents the type of quota violation.
type QuotaErrorType string

const (
	// QuotaErrorTypeExpired indicates the API key has expired.
	QuotaErrorTypeExpired QuotaErrorType = "api_key_expired"

	// QuotaErrorTypeModelNotAllowed indicates the model is not allowed.
	QuotaErrorTypeModelNotAllowed QuotaErrorType = "model_not_allowed"

	// QuotaErrorTypeTokenLimitExceeded indicates the token limit was exceeded.
	QuotaErrorTypeTokenLimitExceeded QuotaErrorType = "token_limit_exceeded"

	// QuotaErrorTypeCostLimitExceeded indicates the cost limit was exceeded.
	QuotaErrorTypeCostLimitExceeded QuotaErrorType = "cost_limit_exceeded"
)

// QuotaError represents a quota violation error.
type QuotaError struct {
	// Type is the type of quota error.
	Type QuotaErrorType `json:"type"`

	// Code is a machine-readable error code.
	Code string `json:"code"`

	// Message is a human-readable error message.
	Message string `json:"message"`

	// Details contains additional error details.
	Details map[string]any `json:"details,omitempty"`
}

// Error implements the error interface.
func (e *QuotaError) Error() string {
	return e.Message
}

// NewExpiredError creates a new API key expired error.
func NewExpiredError(expiresAt time.Time) *QuotaError {
	return &QuotaError{
		Type:    QuotaErrorTypeExpired,
		Code:    "api_key_expired",
		Message: fmt.Sprintf("API key has expired on %s", expiresAt.Format("2006-01-02")),
		Details: map[string]any{
			"expires_at": expiresAt.Format(time.RFC3339),
		},
	}
}

// NewModelNotAllowedError creates a new model not allowed error.
func NewModelNotAllowedError(model string, allowedModels []string) *QuotaError {
	return &QuotaError{
		Type:    QuotaErrorTypeModelNotAllowed,
		Code:    "model_not_allowed",
		Message: fmt.Sprintf("Model '%s' is not allowed for this API key", model),
		Details: map[string]any{
			"requested_model": model,
			"allowed_models":  allowedModels,
		},
	}
}

// NewTokenLimitExceededError creates a new token limit exceeded error.
func NewTokenLimitExceededError(used, limit int64) *QuotaError {
	return &QuotaError{
		Type:    QuotaErrorTypeTokenLimitExceeded,
		Code:    "token_limit_exceeded",
		Message: fmt.Sprintf("Token limit exceeded. Used: %d / Limit: %d", used, limit),
		Details: map[string]any{
			"used_tokens": used,
			"max_tokens":  limit,
		},
	}
}

// NewCostLimitExceededError creates a new cost limit exceeded error.
func NewCostLimitExceededError(used, limit float64) *QuotaError {
	return &QuotaError{
		Type:    QuotaErrorTypeCostLimitExceeded,
		Code:    "cost_limit_exceeded",
		Message: fmt.Sprintf("Cost limit exceeded. Used: $%.2f / Limit: $%.2f", used, limit),
		Details: map[string]any{
			"used_cost_usd": used,
			"max_cost_usd":  limit,
		},
	}
}

// CheckResult represents the result of a quota check.
type CheckResult struct {
	// Allowed indicates whether the request is allowed.
	Allowed bool `json:"allowed"`

	// Error contains the quota error if not allowed.
	Error *QuotaError `json:"error,omitempty"`

	// Usage contains the current usage for the API key.
	Usage *QuotaUsage `json:"usage,omitempty"`

	// Policy contains the policy for the API key.
	Policy *Policy `json:"policy,omitempty"`

	// RemainingTokens is the number of tokens remaining (if limited).
	RemainingTokens *int64 `json:"remaining_tokens,omitempty"`

	// RemainingCostUSD is the remaining cost budget in USD (if limited).
	RemainingCostUSD *float64 `json:"remaining_cost_usd,omitempty"`
}

// NewAllowedResult creates a new allowed check result.
func NewAllowedResult(policy *Policy, usage *QuotaUsage) *CheckResult {
	result := &CheckResult{
		Allowed: true,
		Policy:  policy,
		Usage:   usage,
	}

	if policy != nil && usage != nil {
		if policy.HasTokenLimit() {
			remaining := policy.MaxTokens - usage.TotalTokens
			result.RemainingTokens = &remaining
		}
		if policy.HasCostLimit() {
			remaining := policy.MaxCostUSD - usage.TotalCostUSD
			result.RemainingCostUSD = &remaining
		}
	}

	return result
}

// NewDeniedResult creates a new denied check result.
func NewDeniedResult(err *QuotaError, policy *Policy, usage *QuotaUsage) *CheckResult {
	return &CheckResult{
		Allowed: false,
		Error:   err,
		Policy:  policy,
		Usage:   usage,
	}
}
