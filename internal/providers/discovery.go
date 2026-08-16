package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// doListModelsOpenAI fetches the /models endpoint from an OpenAI-compatible API.
func doListModelsOpenAI(ctx context.Context, baseURL, apiKey string, headers map[string]string) ([]map[string]any, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
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
func doListModelsOpenAIAsModelInfo(ctx context.Context, baseURL, apiKey string, headers map[string]string) ([]ModelInfo, error) {
	raw, err := doListModelsOpenAI(ctx, baseURL, apiKey, headers)
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
			ID:               id,
			DisplayName:      id,
			ContextWindow:    jsonInt(m, "context_length"),
			MaxOutputTokens:  jsonInt(m, "max_output_tokens"),
			SupportsStreaming: true,
			Extra:            map[string]any{},
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
