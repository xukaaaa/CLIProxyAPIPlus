package helps

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestFireworksCreds_PrefersAttributes(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "attr-key",
			"base_url": "https://attr.example.com",
		},
		Metadata: map[string]any{
			"api_key":  "meta-key",
			"base_url": "https://meta.example.com",
		},
	}
	baseURL, apiKey := FireworksCreds(auth)
	if apiKey != "attr-key" {
		t.Fatalf("apiKey = %q, want attr-key", apiKey)
	}
	if baseURL != "https://attr.example.com" {
		t.Fatalf("baseURL = %q, want https://attr.example.com", baseURL)
	}
}

func TestFireworksCreds_FallsBackToMetadata(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{},
		Metadata: map[string]any{
			"api_key":  "meta-key",
			"base_url": "https://meta.example.com",
		},
	}
	baseURL, apiKey := FireworksCreds(auth)
	if apiKey != "meta-key" {
		t.Fatalf("apiKey = %q, want meta-key", apiKey)
	}
	if baseURL != "https://meta.example.com" {
		t.Fatalf("baseURL = %q, want https://meta.example.com", baseURL)
	}
}

func TestFireworksCreds_TrimsWhitespace(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"api_key":  "  meta-key  ",
			"base_url": "  https://meta.example.com  ",
		},
	}
	baseURL, apiKey := FireworksCreds(auth)
	if apiKey != "meta-key" {
		t.Fatalf("apiKey = %q, want meta-key", apiKey)
	}
	if baseURL != "https://meta.example.com" {
		t.Fatalf("baseURL = %q, want https://meta.example.com", baseURL)
	}
}

func TestFireworksCreds_ConvertsNonStringMetadata(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"api_key":  []byte("byte-key"),
			"base_url": []byte("https://byte.example.com"),
		},
	}
	baseURL, apiKey := FireworksCreds(auth)
	if apiKey != "byte-key" {
		t.Fatalf("apiKey = %q, want byte-key", apiKey)
	}
	if baseURL != "https://byte.example.com" {
		t.Fatalf("baseURL = %q, want https://byte.example.com", baseURL)
	}
}
