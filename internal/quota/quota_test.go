package quota

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestAPIKeyPolicy_IsExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt string
		want      bool
	}{
		{
			name:      "no expiration",
			expiresAt: "",
			want:      false,
		},
		{
			name:      "future date",
			expiresAt: time.Now().AddDate(1, 0, 0).Format("2006-01-02"),
			want:      false,
		},
		{
			name:      "past date",
			expiresAt: time.Now().AddDate(-1, 0, 0).Format("2006-01-02"),
			want:      true,
		},
		{
			name:      "today (end of day)",
			expiresAt: time.Now().Format("2006-01-02"),
			want:      false, // End of day hasn't passed yet
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &config.APIKeyPolicy{
				ExpiresAt: tt.expiresAt,
			}
			if got := p.IsExpired(); got != tt.want {
				t.Errorf("IsExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAPIKeyPolicy_IsModelAllowed(t *testing.T) {
	tests := []struct {
		name          string
		allowedModels []string
		model         string
		want          bool
	}{
		{
			name:          "no restriction",
			allowedModels: nil,
			model:         "gpt-4o",
			want:          true,
		},
		{
			name:          "empty restriction",
			allowedModels: []string{},
			model:         "gpt-4o",
			want:          true,
		},
		{
			name:          "model allowed",
			allowedModels: []string{"gpt-4o", "claude-3-5-sonnet"},
			model:         "gpt-4o",
			want:          true,
		},
		{
			name:          "model not allowed",
			allowedModels: []string{"gpt-4o", "claude-3-5-sonnet"},
			model:         "gemini-2.0-flash",
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &config.APIKeyPolicy{
				AllowedModels: tt.allowedModels,
			}
			if got := p.IsModelAllowed(tt.model); got != tt.want {
				t.Errorf("IsModelAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPricingManager_CalculateCost(t *testing.T) {
	manager := &PricingManager{
		pricing: map[string]ModelPricing{
			"gpt-4o": {
				InputPricePerMillion:  2.5,
				OutputPricePerMillion: 10.0,
			},
			"claude-3-5-sonnet": {
				InputPricePerMillion:       3.0,
				OutputPricePerMillion:      15.0,
				CachedInputPricePerMillion: 0.3,
			},
		},
	}

	tests := []struct {
		name         string
		model        string
		inputTokens  int64
		outputTokens int64
		cachedTokens int64
		wantCost     float64
	}{
		{
			name:         "gpt-4o basic",
			model:        "gpt-4o",
			inputTokens:  1000,
			outputTokens: 500,
			cachedTokens: 0,
			wantCost:     0.0025 + 0.005, // 2.5/1M * 1000 + 10/1M * 500
		},
		{
			name:         "claude with cache",
			model:        "claude-3-5-sonnet",
			inputTokens:  1000,
			outputTokens: 500,
			cachedTokens: 2000,
			wantCost:     0.003 + 0.0075 + 0.0006, // input + output + cached
		},
		{
			name:         "unknown model",
			model:        "unknown-model",
			inputTokens:  1000,
			outputTokens: 500,
			cachedTokens: 0,
			wantCost:     0, // No pricing info
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := manager.CalculateCost(tt.model, tt.inputTokens, tt.outputTokens, tt.cachedTokens)
			// Use approximate comparison for floating point
			if diff := got - tt.wantCost; diff > 0.0001 || diff < -0.0001 {
				t.Errorf("CalculateCost() = %v, want %v", got, tt.wantCost)
			}
		})
	}
}

func TestManager_CheckQuota(t *testing.T) {
	// Create a fresh manager for testing
	manager := &Manager{
		policies: make(map[string]*Policy),
		usage:    make(map[string]*QuotaUsage),
		pricing:  GetPricingManager(),
	}

	// Setup test policy
	futureDate := time.Now().AddDate(1, 0, 0).Format("2006-01-02")
	pastDate := time.Now().AddDate(-1, 0, 0).Format("2006-01-02")

	manager.policies["key-valid"] = &Policy{
		AllowedModels: []string{"gpt-4o", "claude-3-5-sonnet"},
		MaxTokens:     100000,
		MaxCostUSD:    10.0,
		ExpiresAt:     futureDate,
	}

	manager.policies["key-expired"] = &Policy{
		ExpiresAt: pastDate,
	}

	manager.policies["key-over-tokens"] = &Policy{
		MaxTokens: 1000,
	}
	manager.usage["key-over-tokens"] = &QuotaUsage{
		TotalTokens: 1500,
	}

	manager.policies["key-over-cost"] = &Policy{
		MaxCostUSD: 5.0,
	}
	manager.usage["key-over-cost"] = &QuotaUsage{
		TotalCostUSD: 6.0,
	}

	tests := []struct {
		name      string
		apiKey    string
		model     string
		wantAllow bool
		wantError QuotaErrorType
	}{
		{
			name:      "no policy - allowed",
			apiKey:    "key-no-policy",
			model:     "any-model",
			wantAllow: true,
		},
		{
			name:      "valid key and model",
			apiKey:    "key-valid",
			model:     "gpt-4o",
			wantAllow: true,
		},
		{
			name:      "expired key",
			apiKey:    "key-expired",
			model:     "gpt-4o",
			wantAllow: false,
			wantError: QuotaErrorTypeExpired,
		},
		{
			name:      "model not allowed",
			apiKey:    "key-valid",
			model:     "gemini-2.0-flash",
			wantAllow: false,
			wantError: QuotaErrorTypeModelNotAllowed,
		},
		{
			name:      "token limit exceeded",
			apiKey:    "key-over-tokens",
			model:     "gpt-4o",
			wantAllow: false,
			wantError: QuotaErrorTypeTokenLimitExceeded,
		},
		{
			name:      "cost limit exceeded",
			apiKey:    "key-over-cost",
			model:     "gpt-4o",
			wantAllow: false,
			wantError: QuotaErrorTypeCostLimitExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := manager.CheckQuota(tt.apiKey, tt.model)
			if result.Allowed != tt.wantAllow {
				t.Errorf("CheckQuota() Allowed = %v, want %v", result.Allowed, tt.wantAllow)
			}
			if !tt.wantAllow && result.Error != nil {
				if result.Error.Type != tt.wantError {
					t.Errorf("CheckQuota() Error.Type = %v, want %v", result.Error.Type, tt.wantError)
				}
			}
		})
	}
}

func TestManager_UpdateUsage(t *testing.T) {
	// Initialize pricing
	InitDefaultPricing()

	manager := &Manager{
		policies: make(map[string]*Policy),
		usage:    make(map[string]*QuotaUsage),
		pricing:  GetPricingManager(),
	}

	apiKey := "test-key"
	model := "gpt-4o"

	// First update
	manager.UpdateUsage(apiKey, model, 1000, 500, 0)

	usage := manager.GetUsageReadOnly(apiKey)
	if usage == nil {
		t.Fatal("Expected usage to be created")
	}

	if usage.TotalTokens != 1500 {
		t.Errorf("TotalTokens = %d, want 1500", usage.TotalTokens)
	}

	if usage.TotalRequests != 1 {
		t.Errorf("TotalRequests = %d, want 1", usage.TotalRequests)
	}

	// Second update
	manager.UpdateUsage(apiKey, model, 2000, 1000, 0)

	usage = manager.GetUsageReadOnly(apiKey)
	if usage.TotalTokens != 4500 {
		t.Errorf("TotalTokens = %d, want 4500", usage.TotalTokens)
	}

	if usage.TotalRequests != 2 {
		t.Errorf("TotalRequests = %d, want 2", usage.TotalRequests)
	}
}

func TestQuotaErrors(t *testing.T) {
	t.Run("NewExpiredError", func(t *testing.T) {
		expiresAt := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
		err := NewExpiredError(expiresAt)
		if err.Type != QuotaErrorTypeExpired {
			t.Errorf("Type = %v, want %v", err.Type, QuotaErrorTypeExpired)
		}
		if err.Code != "api_key_expired" {
			t.Errorf("Code = %v, want api_key_expired", err.Code)
		}
	})

	t.Run("NewModelNotAllowedError", func(t *testing.T) {
		err := NewModelNotAllowedError("gpt-5", []string{"gpt-4o"})
		if err.Type != QuotaErrorTypeModelNotAllowed {
			t.Errorf("Type = %v, want %v", err.Type, QuotaErrorTypeModelNotAllowed)
		}
	})

	t.Run("NewTokenLimitExceededError", func(t *testing.T) {
		err := NewTokenLimitExceededError(150000, 100000)
		if err.Type != QuotaErrorTypeTokenLimitExceeded {
			t.Errorf("Type = %v, want %v", err.Type, QuotaErrorTypeTokenLimitExceeded)
		}
	})

	t.Run("NewCostLimitExceededError", func(t *testing.T) {
		err := NewCostLimitExceededError(15.5, 10.0)
		if err.Type != QuotaErrorTypeCostLimitExceeded {
			t.Errorf("Type = %v, want %v", err.Type, QuotaErrorTypeCostLimitExceeded)
		}
	})
}
