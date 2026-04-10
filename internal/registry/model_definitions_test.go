package registry

import "testing"

func TestCodeBuddyModelsMatchSupportedInternationalSet(t *testing.T) {
	models := GetCodeBuddyModels()
	wanted := map[string]string{
		"gpt-5.4":   "GPT-5.4",
		"glm-5.0":   "GLM-5.0",
		"kimi-k2.5": "Kimi K2.5",
	}
	if len(models) != len(wanted) {
		t.Fatalf("CodeBuddy model count = %d, want %d", len(models), len(wanted))
	}
	for _, model := range models {
		displayName, ok := wanted[model.ID]
		if !ok {
			t.Fatalf("unexpected CodeBuddy model %q", model.ID)
		}
		if model.Type != "codebuddy" {
			t.Fatalf("model %q type = %q, want codebuddy", model.ID, model.Type)
		}
		if model.OwnedBy != "tencent" {
			t.Fatalf("model %q owned_by = %q, want tencent", model.ID, model.OwnedBy)
		}
		if model.DisplayName != displayName {
			t.Fatalf("model %q display name = %q, want %q", model.ID, model.DisplayName, displayName)
		}
		if len(model.SupportedEndpoints) != 1 || model.SupportedEndpoints[0] != "/chat/completions" {
			t.Fatalf("model %q supported endpoints = %v, want [/chat/completions]", model.ID, model.SupportedEndpoints)
		}
	}
}

func TestGitHubCopilotGeminiModelsAreChatOnly(t *testing.T) {
	models := GetGitHubCopilotModels()
	required := map[string]bool{
		"gemini-2.5-pro":         false,
		"gemini-3-pro-preview":   false,
		"gemini-3.1-pro-preview": false,
		"gemini-3-flash-preview": false,
	}

	for _, model := range models {
		if _, ok := required[model.ID]; !ok {
			continue
		}
		required[model.ID] = true
		if len(model.SupportedEndpoints) != 1 || model.SupportedEndpoints[0] != "/chat/completions" {
			t.Fatalf("model %q supported endpoints = %v, want [/chat/completions]", model.ID, model.SupportedEndpoints)
		}
	}

	for modelID, found := range required {
		if !found {
			t.Fatalf("expected GitHub Copilot model %q in definitions", modelID)
		}
	}
}
