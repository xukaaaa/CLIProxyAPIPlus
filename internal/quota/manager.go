package quota

import (
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

// Manager handles quota checking and usage tracking for API keys.
type Manager struct {
	mu       sync.RWMutex
	policies map[string]*Policy     // API key -> Policy
	usage    map[string]*QuotaUsage // API key -> Usage
	pricing  *PricingManager
}

// managerInstance is the singleton instance.
var (
	managerInstance *Manager
	managerOnce     sync.Once
)

// GetManager returns the singleton Manager instance.
func GetManager() *Manager {
	managerOnce.Do(func() {
		managerInstance = &Manager{
			policies: make(map[string]*Policy),
			usage:    make(map[string]*QuotaUsage),
			pricing:  GetPricingManager(),
		}
	})
	return managerInstance
}

// LoadPolicies loads API key policies from config.
func (m *Manager) LoadPolicies(cfg *config.SDKConfig) {
	if cfg == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Clear existing policies
	m.policies = make(map[string]*Policy)

	// Load from APIKeyPolicies map
	if cfg.APIKeyPolicies != nil {
		for apiKey, policy := range cfg.APIKeyPolicies {
			policyCopy := policy // Create a copy
			m.policies[apiKey] = &policyCopy
			log.Debugf("Loaded policy for API key: %s (models: %v, max_tokens: %d, max_cost: %.2f, expires: %s)",
				maskAPIKey(apiKey), policy.AllowedModels, policy.MaxTokens, policy.MaxCostUSD, policy.ExpiresAt)
		}
	}

	log.Infof("Loaded %d API key policies", len(m.policies))
}

// GetPolicy returns the policy for an API key.
// Returns nil if no policy exists (meaning no restrictions).
func (m *Manager) GetPolicy(apiKey string) *Policy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.policies[apiKey]
}

// GetUsage returns the usage for an API key.
// Creates a new usage record if one doesn't exist.
func (m *Manager) GetUsage(apiKey string) *QuotaUsage {
	m.mu.Lock()
	defer m.mu.Unlock()

	if usage, ok := m.usage[apiKey]; ok {
		return usage
	}

	// Create new usage record
	usage := &QuotaUsage{
		APIKey:    apiKey,
		CreatedAt: time.Now(),
	}
	m.usage[apiKey] = usage
	return usage
}

// GetUsageReadOnly returns the usage for an API key without creating one.
func (m *Manager) GetUsageReadOnly(apiKey string) *QuotaUsage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.usage[apiKey]
}

// CheckQuota checks if a request is allowed based on the API key's policy.
// This should be called BEFORE processing the request.
func (m *Manager) CheckQuota(apiKey, model string) *CheckResult {
	policy := m.GetPolicy(apiKey)
	usage := m.GetUsageReadOnly(apiKey)

	// No policy means no restrictions
	if policy == nil {
		return NewAllowedResult(nil, usage)
	}

	// Check 1: Expiration
	if policy.IsExpired() {
		return NewDeniedResult(
			NewExpiredError(policy.ParsedExpiresAt()),
			policy,
			usage,
		)
	}

	// Check 2: Model restriction
	if !policy.IsModelAllowed(model) {
		return NewDeniedResult(
			NewModelNotAllowedError(model, policy.AllowedModels),
			policy,
			usage,
		)
	}

	// Check 3: Token limit
	if policy.HasTokenLimit() && usage != nil {
		if usage.TotalTokens >= policy.MaxTokens {
			return NewDeniedResult(
				NewTokenLimitExceededError(usage.TotalTokens, policy.MaxTokens),
				policy,
				usage,
			)
		}
	}

	// Check 4: Cost limit
	if policy.HasCostLimit() && usage != nil {
		if usage.TotalCostUSD >= policy.MaxCostUSD {
			return NewDeniedResult(
				NewCostLimitExceededError(usage.TotalCostUSD, policy.MaxCostUSD),
				policy,
				usage,
			)
		}
	}

	return NewAllowedResult(policy, usage)
}

// UpdateUsage updates the usage for an API key after a request.
// This should be called AFTER the request is processed.
func (m *Manager) UpdateUsage(apiKey, model string, inputTokens, outputTokens, cachedTokens int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	usage, ok := m.usage[apiKey]
	if !ok {
		usage = &QuotaUsage{
			APIKey:    apiKey,
			CreatedAt: time.Now(),
		}
		m.usage[apiKey] = usage
	}

	// Calculate total tokens
	totalTokens := inputTokens + outputTokens

	// Calculate cost
	cost := m.pricing.CalculateCost(model, inputTokens, outputTokens, cachedTokens)

	// Update usage
	usage.TotalTokens += totalTokens
	usage.TotalCostUSD += cost
	usage.TotalRequests++
	usage.LastUsedAt = time.Now()

	log.Debugf("Updated usage for API key %s: +%d tokens, +$%.4f (total: %d tokens, $%.2f)",
		maskAPIKey(apiKey), totalTokens, cost, usage.TotalTokens, usage.TotalCostUSD)
}

// SetUsage sets the usage for an API key (used when loading from persistence).
func (m *Manager) SetUsage(apiKey string, usage *QuotaUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage[apiKey] = usage
}

// AllUsage returns a copy of all usage data.
func (m *Manager) AllUsage() map[string]*QuotaUsage {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*QuotaUsage, len(m.usage))
	for k, v := range m.usage {
		usageCopy := *v
		result[k] = &usageCopy
	}
	return result
}

// AllPolicies returns a copy of all policies.
func (m *Manager) AllPolicies() map[string]*Policy {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*Policy, len(m.policies))
	for k, v := range m.policies {
		policyCopy := *v
		result[k] = &policyCopy
	}
	return result
}

// ResetUsage resets the usage for a specific API key.
func (m *Manager) ResetUsage(apiKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.usage, apiKey)
}

// ResetAllUsage resets all usage data.
func (m *Manager) ResetAllUsage() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage = make(map[string]*QuotaUsage)
}

// GetQuotaStatus returns a summary of quota status for an API key.
func (m *Manager) GetQuotaStatus(apiKey string) map[string]any {
	policy := m.GetPolicy(apiKey)
	usage := m.GetUsageReadOnly(apiKey)

	status := map[string]any{
		"api_key":    maskAPIKey(apiKey),
		"has_policy": policy != nil,
		"has_usage":  usage != nil,
	}

	if policy != nil {
		status["policy"] = map[string]any{
			"name":           policy.Name,
			"allowed_models": policy.AllowedModels,
			"max_tokens":     policy.MaxTokens,
			"max_cost_usd":   policy.MaxCostUSD,
			"expires_at":     policy.ExpiresAt,
			"is_expired":     policy.IsExpired(),
		}
	}

	if usage != nil {
		status["usage"] = map[string]any{
			"total_tokens":   usage.TotalTokens,
			"total_cost_usd": usage.TotalCostUSD,
			"total_requests": usage.TotalRequests,
			"last_used_at":   usage.LastUsedAt,
			"created_at":     usage.CreatedAt,
		}

		if policy != nil {
			if policy.HasTokenLimit() {
				remaining := policy.MaxTokens - usage.TotalTokens
				status["remaining_tokens"] = remaining
				status["token_usage_percent"] = float64(usage.TotalTokens) / float64(policy.MaxTokens) * 100
			}
			if policy.HasCostLimit() {
				remaining := policy.MaxCostUSD - usage.TotalCostUSD
				status["remaining_cost_usd"] = remaining
				status["cost_usage_percent"] = usage.TotalCostUSD / policy.MaxCostUSD * 100
			}
		}
	}

	return status
}

// maskAPIKey masks an API key for logging (shows first 8 and last 4 chars).
func maskAPIKey(key string) string {
	if len(key) <= 12 {
		return "***"
	}
	return key[:8] + "..." + key[len(key)-4:]
}
