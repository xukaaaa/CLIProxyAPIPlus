package config

import (
	"time"
)

// APIKeyPolicy defines quota limits and restrictions for a specific API key.
// This is used in the api-key-policies config section.
type APIKeyPolicy struct {
	// Name is an optional human-readable identifier for the key.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// AllowedModels lists the exact model names this key can access.
	// If empty, all models are allowed.
	AllowedModels []string `yaml:"allowed_models,omitempty" json:"allowed_models,omitempty"`

	// MaxTokens is the maximum total tokens (lifetime) this key can consume.
	// Zero means unlimited.
	MaxTokens int64 `yaml:"max_tokens,omitempty" json:"max_tokens,omitempty"`

	// MaxCostUSD is the maximum total cost in USD (lifetime) this key can incur.
	// Zero means unlimited.
	MaxCostUSD float64 `yaml:"max_cost_usd,omitempty" json:"max_cost_usd,omitempty"`

	// ExpiresAt is the expiration date for this key (format: "2006-01-02" or RFC3339).
	// Empty means no expiration.
	ExpiresAt string `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`
}

// ParsedExpiresAt returns the parsed expiration time.
// Returns zero time if not set or invalid.
func (p *APIKeyPolicy) ParsedExpiresAt() time.Time {
	if p == nil || p.ExpiresAt == "" {
		return time.Time{}
	}
	// Try RFC3339 first
	if t, err := time.Parse(time.RFC3339, p.ExpiresAt); err == nil {
		return t
	}
	// Try date only format
	if t, err := time.Parse("2006-01-02", p.ExpiresAt); err == nil {
		// Set to end of day
		return t.Add(24*time.Hour - time.Second)
	}
	return time.Time{}
}

// HasModelRestriction returns true if this policy restricts model access.
func (p *APIKeyPolicy) HasModelRestriction() bool {
	return p != nil && len(p.AllowedModels) > 0
}

// HasTokenLimit returns true if this policy has a token limit.
func (p *APIKeyPolicy) HasTokenLimit() bool {
	return p != nil && p.MaxTokens > 0
}

// HasCostLimit returns true if this policy has a cost limit.
func (p *APIKeyPolicy) HasCostLimit() bool {
	return p != nil && p.MaxCostUSD > 0
}

// HasExpiration returns true if this policy has an expiration date.
func (p *APIKeyPolicy) HasExpiration() bool {
	return p != nil && p.ExpiresAt != ""
}

// IsExpired returns true if the key has expired.
func (p *APIKeyPolicy) IsExpired() bool {
	if !p.HasExpiration() {
		return false
	}
	expiresAt := p.ParsedExpiresAt()
	if expiresAt.IsZero() {
		return false
	}
	return time.Now().After(expiresAt)
}

// IsModelAllowed checks if the given model is allowed by this policy.
func (p *APIKeyPolicy) IsModelAllowed(model string) bool {
	if !p.HasModelRestriction() {
		return true
	}
	for _, allowed := range p.AllowedModels {
		if allowed == model {
			return true
		}
	}
	return false
}

// HasAnyRestriction returns true if this policy has any restrictions.
func (p *APIKeyPolicy) HasAnyRestriction() bool {
	if p == nil {
		return false
	}
	return p.HasModelRestriction() || p.HasTokenLimit() || p.HasCostLimit() || p.HasExpiration()
}
