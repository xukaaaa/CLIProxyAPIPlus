package helps

import (
	"fmt"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// FireworksCreds returns the base URL and API key for a Fireworks auth entry.
// It prefers values from Attributes (used by config-synthesized API keys) and
// falls back to Metadata (used by uploaded auth files).
func FireworksCreds(auth *cliproxyauth.Auth) (baseURL, apiKey string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	}
	if baseURL == "" && auth.Metadata != nil {
		baseURL = strings.TrimSpace(stringMetadataValue(auth.Metadata["base_url"]))
	}
	if apiKey == "" && auth.Metadata != nil {
		apiKey = strings.TrimSpace(stringMetadataValue(auth.Metadata["api_key"]))
	}
	return baseURL, apiKey
}

func stringMetadataValue(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	if s, ok := v.(fmt.Stringer); ok {
		return s.String()
	}
	return fmt.Sprint(v)
}
