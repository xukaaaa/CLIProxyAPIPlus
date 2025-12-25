package quota

import (
	"bytes"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// ContextKeyAPIKey is the context key for the API key.
const ContextKeyAPIKey = "apiKey"

// ContextKeyQuotaResult is the context key for the quota check result.
const ContextKeyQuotaResult = "quotaResult"

// ContextKeyRequestModel is the context key for the requested model.
const ContextKeyRequestModel = "requestModel"

// Middleware returns a Gin middleware that enforces quota limits.
// It should be placed AFTER the AuthMiddleware.
func Middleware(manager *Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if manager == nil {
			c.Next()
			return
		}

		// Get API key from context (set by AuthMiddleware)
		apiKey, exists := c.Get(ContextKeyAPIKey)
		if !exists || apiKey == nil {
			// No API key, skip quota check
			c.Next()
			return
		}

		apiKeyStr, ok := apiKey.(string)
		if !ok || apiKeyStr == "" {
			c.Next()
			return
		}

		// Extract model from request body
		model := extractModelFromRequest(c)
		if model != "" {
			c.Set(ContextKeyRequestModel, model)
		}

		// Check quota
		result := manager.CheckQuota(apiKeyStr, model)
		c.Set(ContextKeyQuotaResult, result)

		if !result.Allowed {
			// Quota exceeded, return error
			respondWithQuotaError(c, result.Error)
			c.Abort()
			return
		}

		c.Next()
	}
}

// PostRequestMiddleware returns a middleware that updates usage after request completion.
// It should be placed at the end of the middleware chain.
func PostRequestMiddleware(manager *Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Process request first
		c.Next()

		if manager == nil {
			return
		}

		// Only update usage for successful requests
		if c.Writer.Status() >= 400 {
			return
		}

		// Get API key
		apiKey, exists := c.Get(ContextKeyAPIKey)
		if !exists || apiKey == nil {
			return
		}

		apiKeyStr, ok := apiKey.(string)
		if !ok || apiKeyStr == "" {
			return
		}

		// Get model
		model := ""
		if m, exists := c.Get(ContextKeyRequestModel); exists {
			model, _ = m.(string)
		}

		// Get token usage from response (if available)
		// This will be set by the handler after processing
		inputTokens, outputTokens, cachedTokens := extractTokenUsageFromContext(c)

		if inputTokens > 0 || outputTokens > 0 {
			manager.UpdateUsage(apiKeyStr, model, inputTokens, outputTokens, cachedTokens)
		}
	}
}

// extractModelFromRequest extracts the model name from the request body.
func extractModelFromRequest(c *gin.Context) string {
	// Only process POST requests with JSON body
	if c.Request.Method != http.MethodPost {
		return ""
	}

	contentType := c.GetHeader("Content-Type")
	if contentType != "application/json" && contentType != "" {
		// Check if it starts with application/json
		if len(contentType) < 16 || contentType[:16] != "application/json" {
			return ""
		}
	}

	// Read body
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return ""
	}

	// Restore body for downstream handlers
	c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

	// Extract model using gjson
	model := gjson.GetBytes(body, "model").String()
	return model
}

// extractTokenUsageFromContext extracts token usage from the gin context.
// These values should be set by the handler after processing the response.
func extractTokenUsageFromContext(c *gin.Context) (inputTokens, outputTokens, cachedTokens int64) {
	if v, exists := c.Get("inputTokens"); exists {
		if tokens, ok := v.(int64); ok {
			inputTokens = tokens
		}
	}
	if v, exists := c.Get("outputTokens"); exists {
		if tokens, ok := v.(int64); ok {
			outputTokens = tokens
		}
	}
	if v, exists := c.Get("cachedTokens"); exists {
		if tokens, ok := v.(int64); ok {
			cachedTokens = tokens
		}
	}
	return
}

// respondWithQuotaError sends a quota error response.
func respondWithQuotaError(c *gin.Context, err *QuotaError) {
	if err == nil {
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"type":    "quota_exceeded",
				"code":    "quota_exceeded",
				"message": "Quota exceeded",
			},
		})
		return
	}

	statusCode := http.StatusForbidden
	errorType := "quota_exceeded"

	switch err.Type {
	case QuotaErrorTypeExpired:
		statusCode = http.StatusUnauthorized
		errorType = "authentication_error"
	case QuotaErrorTypeModelNotAllowed:
		statusCode = http.StatusForbidden
		errorType = "permission_error"
	case QuotaErrorTypeTokenLimitExceeded, QuotaErrorTypeCostLimitExceeded:
		statusCode = http.StatusForbidden
		errorType = "quota_exceeded"
	}

	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"type":    errorType,
			"code":    string(err.Code),
			"message": err.Message,
			"details": err.Details,
		},
	})

	log.Warnf("Quota check failed: %s - %s", err.Code, err.Message)
}

// SetTokenUsage is a helper function to set token usage in the gin context.
// This should be called by handlers after processing the response.
func SetTokenUsage(c *gin.Context, inputTokens, outputTokens, cachedTokens int64) {
	c.Set("inputTokens", inputTokens)
	c.Set("outputTokens", outputTokens)
	c.Set("cachedTokens", cachedTokens)
}

// GetQuotaResult returns the quota check result from the gin context.
func GetQuotaResult(c *gin.Context) *CheckResult {
	if result, exists := c.Get(ContextKeyQuotaResult); exists {
		if r, ok := result.(*CheckResult); ok {
			return r
		}
	}
	return nil
}
