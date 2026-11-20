package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type TogetherProvider struct{}

func (p *TogetherProvider) Name() string { return "together" }

func (p *TogetherProvider) DefaultModels() []string {
	return []string{"meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo", "mistralai/Mixtral-8x7B-Instruct-v0.1"}
}

func (p *TogetherProvider) baseURL() string {
	return envOrDef("TOGETHER_BASE_URL", "https://api.together.xyz/v1")
}

func (p *TogetherProvider) Complete(ctx context.Context, req *ChatRequest, apiKey string) (*ChatResponse, error) {
	return doOpenAIComplete(ctx, p.baseURL(), apiKey, req, nil)
}

func (p *TogetherProvider) Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error) {
	return doOpenAIStream(ctx, p.baseURL(), apiKey, req, nil)
}

func (p *TogetherProvider) HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error) {
	return doOpenAIHealthCheck(ctx, p.baseURL(), apiKey, "meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo", nil)
}

// Together AI returns a richer model list via their models endpoint.
func (p *TogetherProvider) ListModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL()+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Together returns a flat list of objects
	var raw []struct {
		ID               string  `json:"id"`
		DisplayName      string  `json:"display_name"`
		Type             string  `json:"type"`
		ContextLen       int     `json:"context_length"`
		PricingInput     float64 `json:"pricing_input"`   // per million tokens
		PricingOutput    float64 `json:"pricing_output"`  // per million tokens
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		// Try OpenAI-compat fallback
		return doListModelsOpenAIAsModelInfo(ctx, p.baseURL(), apiKey, nil)
	}

	models := make([]ModelInfo, 0, len(raw))
	for _, m := range raw {
		if m.Type != "chat" && m.Type != "language" {
			continue
		}
		models = append(models, ModelInfo{
			ID:                m.ID,
			DisplayName:       m.DisplayName,
			ContextWindow:     m.ContextLen,
			MaxOutputTokens:   4096,
			SupportsStreaming:  true,
			InputCostPerMTok:  m.PricingInput,
			OutputCostPerMTok: m.PricingOutput,
			Extra:             map[string]any{"type": m.Type},
		})
	}
	return models, nil
}
