package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type OpenRouterProvider struct{}

func (p *OpenRouterProvider) Name() string { return "openrouter" }

func (p *OpenRouterProvider) DefaultModels() []string {
	return []string{"openai/gpt-4o-mini", "meta-llama/llama-3.1-70b-instruct"}
}

func (p *OpenRouterProvider) baseURL() string {
	return envOrDef("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1")
}

func (p *OpenRouterProvider) headers() map[string]string {
	return map[string]string{
		"HTTP-Referer": "https://zer0-router.local",
		"X-Title":      "Zer0 Router",
	}
}

func (p *OpenRouterProvider) Complete(ctx context.Context, req *ChatRequest, apiKey string) (*ChatResponse, error) {
	return doOpenAIComplete(ctx, p.baseURL(), apiKey, req, p.headers())
}

func (p *OpenRouterProvider) Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error) {
	return doOpenAIStream(ctx, p.baseURL(), apiKey, req, p.headers())
}

func (p *OpenRouterProvider) HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error) {
	return doOpenAIHealthCheck(ctx, p.baseURL(), apiKey, "openai/gpt-oss-20b:free", p.headers())
}

func (p *OpenRouterProvider) ProbeModel(ctx context.Context, apiKey, model string) (*HealthResult, error) {
	return doOpenAIProbeModel(ctx, p.baseURL(), apiKey, model, p.headers())
}

// ListModels queries OpenRouter's model catalog — it returns pricing natively.
func (p *OpenRouterProvider) ListModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("HTTP-Referer", "https://zer0-router.local")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			ContextLen  int    `json:"context_length"`
			Pricing     struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
			TopProvider struct {
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	// Only include :free-tier models — paid models require credits and are inaccessible
	// on free-tier keys. OpenRouter marks free models with a ":free" suffix in the ID.
	models := make([]ModelInfo, 0)
	for _, m := range result.Data {
		if len(m.ID) < 5 || m.ID[len(m.ID)-5:] != ":free" {
			continue
		}
		paramB, arch := LookupModelMeta(m.ID)
		info := ModelInfo{
			ID:                m.ID,
			DisplayName:       m.Name,
			ContextWindow:     m.ContextLen,
			MaxOutputTokens:   m.TopProvider.MaxCompletionTokens,
			SupportsStreaming:  true,
			InputCostPerMTok:  parseMillionPrice(m.Pricing.Prompt),
			OutputCostPerMTok: parseMillionPrice(m.Pricing.Completion),
			ParameterCountB:   paramB,
			Architecture:      arch,
			Extra:             map[string]any{},
		}
		models = append(models, info)
	}
	return models, nil
}
