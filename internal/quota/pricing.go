package quota

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	log "github.com/sirupsen/logrus"
)

// ModelPricing defines the pricing for a specific model.
type ModelPricing struct {
	// InputPricePerMillion is the cost per million input tokens in USD.
	InputPricePerMillion float64 `json:"input_price_per_million"`

	// OutputPricePerMillion is the cost per million output tokens in USD.
	OutputPricePerMillion float64 `json:"output_price_per_million"`

	// CachedInputPricePerMillion is the cost per million cached input tokens in USD.
	// If zero, defaults to InputPricePerMillion.
	CachedInputPricePerMillion float64 `json:"cached_input_price_per_million,omitempty"`
}

// PricingManager manages model pricing data.
type PricingManager struct {
	mu       sync.RWMutex
	pricing  map[string]ModelPricing
	filePath string
}

// pricingManagerInstance is the singleton instance.
var (
	pricingManagerInstance *PricingManager
	pricingManagerOnce     sync.Once
)

// GetPricingManager returns the singleton PricingManager instance.
func GetPricingManager() *PricingManager {
	pricingManagerOnce.Do(func() {
		pricingManagerInstance = &PricingManager{
			pricing: make(map[string]ModelPricing),
		}
	})
	return pricingManagerInstance
}

// SetPricingFilePath sets the path to the pricing JSON file.
// This should be called during server startup.
func (m *PricingManager) SetPricingFilePath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.filePath = path
}

// LoadFromFile loads pricing data from the configured JSON file.
func (m *PricingManager) LoadFromFile() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.filePath == "" {
		log.Debug("Pricing file path not set, using defaults only")
		return nil
	}

	data, err := os.ReadFile(m.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debugf("Pricing file does not exist: %s, using defaults", m.filePath)
			return nil
		}
		return fmt.Errorf("failed to read pricing file: %w", err)
	}

	var pricing map[string]ModelPricing
	if err := json.Unmarshal(data, &pricing); err != nil {
		return fmt.Errorf("failed to parse pricing file: %w", err)
	}

	// Merge with existing (file overrides defaults)
	for model, price := range pricing {
		m.pricing[model] = price
	}

	log.Infof("Loaded pricing for %d models from %s", len(pricing), m.filePath)
	return nil
}

// LoadFromGitStore loads pricing from a GitStore directory.
// It looks for a file named "pricing.json" in the config subdirectory.
func (m *PricingManager) LoadFromGitStore(gitStoreDir string) error {
	if gitStoreDir == "" {
		return nil
	}

	pricingPath := filepath.Join(gitStoreDir, "config", "pricing.json")
	m.SetPricingFilePath(pricingPath)
	return m.LoadFromFile()
}

// GetPricing returns the pricing for a specific model.
// Returns nil if the model is not found.
func (m *PricingManager) GetPricing(model string) *ModelPricing {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if pricing, ok := m.pricing[model]; ok {
		return &pricing
	}
	return nil
}

// SetPricing sets the pricing for a specific model.
func (m *PricingManager) SetPricing(model string, pricing ModelPricing) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pricing[model] = pricing
}

// SetDefaultPricing sets default pricing for models that don't have explicit pricing.
func (m *PricingManager) SetDefaultPricing(defaults map[string]ModelPricing) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for model, pricing := range defaults {
		if _, exists := m.pricing[model]; !exists {
			m.pricing[model] = pricing
		}
	}
}

// CalculateCost calculates the cost for a request based on token usage.
func (m *PricingManager) CalculateCost(model string, inputTokens, outputTokens, cachedTokens int64) float64 {
	pricing := m.GetPricing(model)
	if pricing == nil {
		// No pricing info, return 0
		return 0
	}

	// Calculate input cost
	inputCost := float64(inputTokens) * pricing.InputPricePerMillion / 1_000_000

	// Calculate cached input cost
	cachedPrice := pricing.CachedInputPricePerMillion
	if cachedPrice == 0 {
		cachedPrice = pricing.InputPricePerMillion
	}
	cachedCost := float64(cachedTokens) * cachedPrice / 1_000_000

	// Calculate output cost
	outputCost := float64(outputTokens) * pricing.OutputPricePerMillion / 1_000_000

	return inputCost + cachedCost + outputCost
}

// AllPricing returns a copy of all pricing data.
func (m *PricingManager) AllPricing() map[string]ModelPricing {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]ModelPricing, len(m.pricing))
	for k, v := range m.pricing {
		result[k] = v
	}
	return result
}

// DefaultModelPricing returns default pricing for common models.
// This can be used as fallback when no pricing file is available.
func DefaultModelPricing() map[string]ModelPricing {
	return map[string]ModelPricing{
		// GPT-5 models
		"gpt-5":              {InputPricePerMillion: 1.25, OutputPricePerMillion: 10.0, CachedInputPricePerMillion: 0.125},
		"gpt-5-codex":        {InputPricePerMillion: 1.25, OutputPricePerMillion: 10.0, CachedInputPricePerMillion: 0.125},
		"gpt-5-codex-mini":   {InputPricePerMillion: 0.25, OutputPricePerMillion: 2.0, CachedInputPricePerMillion: 0.025},
		"gpt-5.1":            {InputPricePerMillion: 1.25, OutputPricePerMillion: 10.0, CachedInputPricePerMillion: 0.125},
		"gpt-5.1-codex":      {InputPricePerMillion: 1.25, OutputPricePerMillion: 10.0, CachedInputPricePerMillion: 0.125},
		"gpt-5.1-codex-mini": {InputPricePerMillion: 0.25, OutputPricePerMillion: 2.0, CachedInputPricePerMillion: 0.025},
		"gpt-5.1-codex-max":  {InputPricePerMillion: 1.25, OutputPricePerMillion: 10.0, CachedInputPricePerMillion: 0.125},
		"gpt-5.2":            {InputPricePerMillion: 1.75, OutputPricePerMillion: 14.0, CachedInputPricePerMillion: 0.175},
		"gpt-5.2-codex":      {InputPricePerMillion: 1.75, OutputPricePerMillion: 14.0, CachedInputPricePerMillion: 0.175},

		// Gemini models
		"gemini-2.5-pro":         {InputPricePerMillion: 1.25, OutputPricePerMillion: 10.0},
		"gemini-2.5-flash":       {InputPricePerMillion: 0.15, OutputPricePerMillion: 0.6},
		"gemini-2.5-flash-lite":  {InputPricePerMillion: 0.075, OutputPricePerMillion: 0.3},
		"gemini-3-pro-preview":   {InputPricePerMillion: 3.0, OutputPricePerMillion: 15.0},
		"gemini-3-flash-preview": {InputPricePerMillion: 0.5, OutputPricePerMillion: 3.0},

		// Kiro/Claude models
		"kiro-claude-sonnet-4":           {InputPricePerMillion: 3.0, OutputPricePerMillion: 15.0, CachedInputPricePerMillion: 0.3},
		"kiro-claude-sonnet-4-agentic":   {InputPricePerMillion: 3.0, OutputPricePerMillion: 15.0, CachedInputPricePerMillion: 0.3},
		"kiro-claude-sonnet-4-5":         {InputPricePerMillion: 3.0, OutputPricePerMillion: 15.0, CachedInputPricePerMillion: 0.3},
		"kiro-claude-sonnet-4-5-agentic": {InputPricePerMillion: 3.0, OutputPricePerMillion: 15.0, CachedInputPricePerMillion: 0.3},
		"kiro-claude-haiku-4-5":          {InputPricePerMillion: 0.8, OutputPricePerMillion: 4.0, CachedInputPricePerMillion: 0.08},
		"kiro-claude-haiku-4-5-agentic":  {InputPricePerMillion: 0.8, OutputPricePerMillion: 4.0, CachedInputPricePerMillion: 0.08},
		"kiro-claude-opus-4-5":           {InputPricePerMillion: 5.0, OutputPricePerMillion: 25.0, CachedInputPricePerMillion: 0.5},
		"kiro-claude-opus-4-5-agentic":   {InputPricePerMillion: 5.0, OutputPricePerMillion: 25.0, CachedInputPricePerMillion: 0.5},

		// Antigravity models
		"gemini-claude-sonnet-4-5":          {InputPricePerMillion: 3.0, OutputPricePerMillion: 15.0},
		"gemini-claude-sonnet-4-5-thinking": {InputPricePerMillion: 3.0, OutputPricePerMillion: 15.0},
		"gemini-claude-opus-4-5-thinking":   {InputPricePerMillion: 5.0, OutputPricePerMillion: 25.0},
		"gemini-3-pro-image-preview":        {InputPricePerMillion: 3.0, OutputPricePerMillion: 15.0},
		"gpt-oss-120b-medium":               {InputPricePerMillion: 1.0, OutputPricePerMillion: 5.0},
	}
}

// InitDefaultPricing initializes the pricing manager with default pricing.
func InitDefaultPricing() {
	manager := GetPricingManager()
	manager.SetDefaultPricing(DefaultModelPricing())
}
