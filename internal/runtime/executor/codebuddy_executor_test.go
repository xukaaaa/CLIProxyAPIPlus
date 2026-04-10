package executor

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codebuddy"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestValidateCodeBuddyModel(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "gpt-5.4 allowed", input: "gpt-5.4"},
		{name: "glm-5.0 allowed", input: "glm-5.0"},
		{name: "kimi-k2.5 allowed", input: "kimi-k2.5"},
		{name: "glm-5.1 rejected", input: "glm-5.1", wantErr: true},
		{name: "removed alias rejected", input: "codebuddy-gpt-5.4", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCodeBuddyModel(tt.input)
			if tt.wantErr && err == nil {
				t.Fatalf("validateCodeBuddyModel(%q) expected error", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateCodeBuddyModel(%q) unexpected error: %v", tt.input, err)
			}
		})
	}
}

func TestResolveCodeBuddyCredentials_DefaultsToMainland(t *testing.T) {
	creds := resolveCodeBuddyCredentials(&cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "token",
			"user_id":      "user-1",
		},
	})

	if creds.AccessToken != "token" {
		t.Fatalf("expected access token, got %q", creds.AccessToken)
	}
	if creds.UserID != "user-1" {
		t.Fatalf("expected user id, got %q", creds.UserID)
	}
	if creds.Domain != codebuddy.DefaultDomain {
		t.Fatalf("expected default domain %q, got %q", codebuddy.DefaultDomain, creds.Domain)
	}
	if creds.BaseURL != codebuddy.BaseURL {
		t.Fatalf("expected base URL %q, got %q", codebuddy.BaseURL, creds.BaseURL)
	}
	if creds.ChatBaseURL != codebuddy.BaseURL {
		t.Fatalf("expected chat base URL %q, got %q", codebuddy.BaseURL, creds.ChatBaseURL)
	}
	if creds.Environment != codebuddy.EnvironmentMainland {
		t.Fatalf("expected environment %q, got %q", codebuddy.EnvironmentMainland, creds.Environment)
	}
}

func TestResolveCodeBuddyCredentials_UsesMetadataOverrides(t *testing.T) {
	creds := resolveCodeBuddyCredentials(&cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token":   "token",
			"user_id":        "user-2",
			"environment":    codebuddy.EnvironmentInternational,
			"domain":         "intl.custom.domain",
			"base_url":       "https://auth.example.com",
			"chat_base_url":  "https://chat.example.com",
			"login_url_base": codebuddy.InternationalLoginURLBase,
		},
	})

	if creds.Domain != "intl.custom.domain" {
		t.Fatalf("expected overridden domain, got %q", creds.Domain)
	}
	if creds.BaseURL != codebuddy.InternationalLoginURLBase {
		t.Fatalf("expected international base URL, got %q", creds.BaseURL)
	}
	if creds.ChatBaseURL != codebuddy.InternationalLoginURLBase {
		t.Fatalf("expected international chat base URL, got %q", creds.ChatBaseURL)
	}
	if creds.Environment != codebuddy.EnvironmentInternational {
		t.Fatalf("expected environment %q, got %q", codebuddy.EnvironmentInternational, creds.Environment)
	}
}

func TestPrepareRequest_SetsDefaultDomainHeader(t *testing.T) {
	executor := NewCodeBuddyExecutor(nil)
	req, err := http.NewRequest(http.MethodPost, "https://example.com", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	err = executor.PrepareRequest(req, &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "token",
			"user_id":      "user-1",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := req.Header.Get("X-Domain"); got != codebuddy.DefaultDomain {
		t.Fatalf("expected X-Domain %q, got %q", codebuddy.DefaultDomain, got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer token" {
		t.Fatalf("expected Authorization header, got %q", got)
	}
}

func TestPrepareRequest_RequiresAccessToken(t *testing.T) {
	executor := NewCodeBuddyExecutor(nil)
	req, err := http.NewRequest(http.MethodPost, "https://example.com", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	err = executor.PrepareRequest(req, &cliproxyauth.Auth{Metadata: map[string]any{"user_id": "user-1"}})
	if err == nil {
		t.Fatal("expected error when access token is missing")
	}
}

func TestPrepareRequest_RejectsUnsupportedCodeBuddyChatModel(t *testing.T) {
	executor := NewCodeBuddyExecutor(nil)
	body := []byte(`{"model":"glm-5.1"}`)
	req, err := http.NewRequest(http.MethodPost, "https://www.codebuddy.ai/v2/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	req.URL = &url.URL{Path: "/v2/chat/completions"}

	err = executor.PrepareRequest(req, &cliproxyauth.Auth{Metadata: map[string]any{
		"access_token": "token",
		"user_id":      "user-1",
		"domain":       codebuddy.InternationalDomain,
	}})
	if err == nil {
		t.Fatal("expected error for unsupported CodeBuddy model")
	}
}

func TestPrepareRequest_RejectsUnsupportedCodeBuddyChatModelWithoutGetBody(t *testing.T) {
	executor := NewCodeBuddyExecutor(nil)
	body := []byte(`{"model":"glm-5.1"}`)
	req, err := http.NewRequest(http.MethodPost, "https://www.codebuddy.ai/v2/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.GetBody = nil
	req.URL = &url.URL{Path: "/v2/chat/completions"}

	err = executor.PrepareRequest(req, &cliproxyauth.Auth{Metadata: map[string]any{
		"access_token": "token",
		"user_id":      "user-1",
		"domain":       codebuddy.InternationalDomain,
	}})
	if err == nil {
		t.Fatal("expected error for unsupported CodeBuddy model without GetBody")
	}
}
