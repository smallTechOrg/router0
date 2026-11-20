package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const listModelsTimeout = 30 * time.Second

// doGetJSON performs an authenticated GET (bounded by listModelsTimeout) and
// returns the 2xx body. Non-2xx and transport failures are *ProviderError.
func doGetJSON(ctx context.Context, url string, headers map[string]string, cls Classifier) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, listModelsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := doRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, failureFromResponse(resp, cls)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		pe, _ := TransportError(ctx, err)
		if pe == nil {
			return nil, err
		}
		return nil, pe
	}
	return b, nil
}

func bearer(apiKey string, extra map[string]string) map[string]string {
	h := map[string]string{"Authorization": "Bearer " + apiKey}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

// doListModelsOpenAI fetches the /models endpoint from an OpenAI-compatible API.
func doListModelsOpenAI(ctx context.Context, baseURL, apiKey string, headers map[string]string, cls Classifier) ([]map[string]any, error) {
	b, err := doGetJSON(ctx, strings.TrimRight(baseURL, "/")+"/models", bearer(apiKey, headers), cls)
	if err != nil {
		return nil, err
	}
	var result struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, &ProviderError{Class: ErrorClassTransient, Code: "invalid_response", Message: "unparseable model list"}
	}
	return result.Data, nil
}

// parseMillionPrice converts a provider price string (per token) to USD per million tokens.
// OpenRouter reports per-token prices as strings like "0.0000005".
func parseMillionPrice(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v * 1_000_000
}

func jsonStr(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// doListModelsOpenAIAsModelInfo is a convenience wrapper that returns []ModelInfo from an OpenAI-compat /models list.
func doListModelsOpenAIAsModelInfo(ctx context.Context, baseURL, apiKey string, headers map[string]string, cls Classifier) ([]ModelInfo, error) {
	raw, err := doListModelsOpenAI(ctx, baseURL, apiKey, headers, cls)
	if err != nil {
		return nil, err
	}
	models := make([]ModelInfo, 0, len(raw))
	for _, m := range raw {
		id := jsonStr(m, "id")
		if id == "" {
			continue
		}
		models = append(models, ModelInfo{
			ID:                id,
			DisplayName:       id,
			ContextWindow:     jsonInt(m, "context_length"),
			MaxOutputTokens:   jsonInt(m, "max_output_tokens"),
			SupportsStreaming: true,
			Extra:             map[string]any{},
		})
	}
	return models, nil
}

func jsonInt(m map[string]any, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}
