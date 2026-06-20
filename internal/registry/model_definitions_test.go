package registry

import "testing"

func TestGetFireworksModelsIncludesTemporaryKimiCodeModel(t *testing.T) {
	models := GetFireworksModels()
	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == "accounts/fireworks/models/kimi-k2p7-code" {
			if model.Type != "fireworks" {
				t.Fatalf("model type = %q, want fireworks", model.Type)
			}
			return
		}
	}
	t.Fatal("expected Fireworks Kimi K2P7 Code model")
}

func TestGetFireworksModelsIncludesKimiCodePriorityModel(t *testing.T) {
	models := GetFireworksModels()
	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == "accounts/fireworks/models/kimi-k2p7-code-priority" {
			if model.Type != "fireworks" {
				t.Fatalf("model type = %q, want fireworks", model.Type)
			}
			return
		}
	}
	t.Fatal("expected Fireworks Kimi K2P7 Code Priority model")
}

func TestGetStaticModelDefinitionsByChannelFireworks(t *testing.T) {
	models := GetStaticModelDefinitionsByChannel("fireworks")
	if len(models) == 0 {
		t.Fatal("expected fireworks static models")
	}
	if got := LookupStaticModelInfo("accounts/fireworks/models/kimi-k2p7-code"); got == nil {
		t.Fatal("expected LookupStaticModelInfo to find fireworks model")
	}
}

func TestWithXAIBuiltinsIncludesVideoPreviewModel(t *testing.T) {
	models := WithXAIBuiltins(nil)

	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == xaiBuiltinVideo15PreviewModelID {
			return
		}
	}

	t.Fatalf("expected xAI builtin model %s", xaiBuiltinVideo15PreviewModelID)
}

func TestAntigravityWebSearchModelForRequiresRequestedModelCapability(t *testing.T) {
	registryRef := GetGlobalRegistry()
	registryRef.RegisterClient("test-antigravity-websearch-route", "antigravity", []*ModelInfo{
		{ID: "gemini-route-test"},
		{ID: "gemini-web-search-test", SupportsWebSearch: true},
	})
	registryRef.RegisterClient("test-gemini-websearch-route", "gemini", []*ModelInfo{
		{ID: "gemini-cross-provider-route"},
		{ID: "gemini-cross-provider-search", SupportsWebSearch: true},
	})
	t.Cleanup(func() {
		registryRef.UnregisterClient("test-antigravity-websearch-route")
		registryRef.UnregisterClient("test-gemini-websearch-route")
	})

	if got := AntigravityWebSearchModelFor("gemini-route-test"); got != "" {
		t.Fatalf("route model without web search support should not get fallback model, got %q", got)
	}
	if got := AntigravityWebSearchModelFor("gemini-route-test(high)"); got != "" {
		t.Fatalf("suffix route model without web search support should not get fallback model, got %q", got)
	}
	if got := AntigravityWebSearchModelFor("gemini-web-search-test"); got != "gemini-web-search-test" {
		t.Fatalf("AntigravityWebSearchModelFor capable model = %q, want itself", got)
	}
	if got := AntigravityWebSearchModelFor("gemini-cross-provider-route"); got != "" {
		t.Fatalf("cross-provider model should not get Antigravity web search model, got %q", got)
	}
	if got := AntigravityWebSearchModelFor("unknown-model"); got != "" {
		t.Fatalf("unknown model should not get Antigravity web search model, got %q", got)
	}
}
