package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
)

const explicitProviderBodyLimit = 4 << 20

func registerExplicitProviderRoutes(v1 *gin.RouterGroup) {
	v1.POST("/embeddings", explicitProviderRoute(
		"https://openrouter.ai/api/v1/embeddings", os.Getenv("OPENROUTER_EMBEDDINGS_API_KEY"),
		os.Getenv("OPENROUTER_EMBEDDINGS_MODEL"), http.DefaultTransport,
	))
	v1.POST("/decisions", explicitProviderRoute(
		"https://openrouter.ai/api/alpha/decisions", os.Getenv("OPENROUTER_DECISIONS_API_KEY"),
		os.Getenv("OPENROUTER_DECISIONS_MODEL"), http.DefaultTransport, translateDecisions,
	))
}

// explicitProviderRoute deliberately does not use the model router or its retries.
// This paid operation is chosen by the client and cannot be reached by Codex failover.
func explicitProviderRoute(endpoint, key, model string, transport http.RoundTripper, translators ...decisionTranslator) gin.HandlerFunc {
	fail := func(c *gin.Context, status int, kind, code, message string) {
		c.JSON(status, gin.H{"error": gin.H{
			"message": message, "type": kind, "param": nil, "code": code,
		}})
	}
	return func(c *gin.Context) {
		if key == "" || model == "" {
			fail(c, http.StatusServiceUnavailable, "server_error", "provider_not_configured", "This operation is not configured.")
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, explicitProviderBodyLimit))
		if err != nil {
			var limit *http.MaxBytesError
			if errors.As(err, &limit) {
				fail(c, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large", "The request exceeds 4 MiB.")
			} else {
				fail(c, http.StatusBadRequest, "invalid_request_error", "invalid_body", "The request body could not be read.")
			}
			return
		}
		var requested struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &requested); err != nil || requested.Model == "" {
			fail(c, http.StatusBadRequest, "invalid_request_error", "invalid_body", "Send a JSON object with a model.")
			return
		}
		if requested.Model != model {
			fail(c, http.StatusNotFound, "invalid_request_error", "model_not_found", "The requested model is not configured for this operation.")
			return
		}

		var decode decisionDecoder
		if len(translators) != 0 {
			body, decode, err = translators[0](body)
			if err != nil {
				fail(c, http.StatusBadRequest, "invalid_request_error", "unsupported_decision_request", err.Error())
				return
			}
		}

		request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			fail(c, http.StatusBadGateway, "server_error", "upstream_error", "Could not prepare the provider request.")
			return
		}
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json")
		response, err := transport.RoundTrip(request)
		if err != nil {
			fail(c, http.StatusBadGateway, "server_error", "upstream_error", "The provider request failed.")
			return
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			fail(c, http.StatusBadGateway, "server_error", "upstream_redirect", "The provider returned an unexpected redirect.")
			return
		}
		var decoded []byte
		if decode != nil && response.StatusCode < 300 {
			responseBytes, readErr := io.ReadAll(io.LimitReader(response.Body, explicitProviderBodyLimit+1))
			if readErr != nil || len(responseBytes) > explicitProviderBodyLimit {
				fail(c, http.StatusBadGateway, "server_error", "invalid_upstream_response", "Could not read the provider response.")
				return
			}
			decoded, err = decode(responseBytes)
			if err != nil {
				fail(c, http.StatusBadGateway, "server_error", "invalid_upstream_response", "The provider returned an incompatible Decisions response.")
				return
			}
		}
		for name, values := range response.Header {
			lower := strings.ToLower(name)
			if lower == "content-type" || lower == "retry-after" || lower == "x-request-id" || strings.HasPrefix(lower, "x-ratelimit-") {
				for _, value := range values {
					c.Writer.Header().Add(name, value)
				}
			}
		}
		c.Status(response.StatusCode)
		if decoded != nil {
			_, _ = c.Writer.Write(decoded)
			return
		}
		_, _ = io.Copy(c.Writer, response.Body)
	}
}
