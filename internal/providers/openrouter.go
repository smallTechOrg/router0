package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type OpenRouterProvider struct{}

func init() { Register(&OpenRouterProvider{}) }

func (p *OpenRouterProvider) Name() string { return "openrouter" }

func (p *OpenRouterProvider) DefaultModels() []string {
	return []string{"openai/gpt-4o-mini", "meta-llama/llama-3.1-70b-instruct"}
}

func (p *OpenRouterProvider) baseURL() string {
	return envOrDef("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1")
}

// headers are OpenRouter's app-attribution headers.
func (p *OpenRouterProvider) headers() map[string]string {
	return map[string]string{
		"HTTP-Referer": "https://router0.local",
		"X-Title":      "router0",
	}
}

// Classify applies OpenRouter-specific rules on top of the generic ones.
func (p *OpenRouterProvider) Classify(status int, h http.Header, body []byte) *ProviderError {
	// A 200 response (or SSE event) carrying {"error":{"code":<int>,...}} is
	// classified using the numeric code as the HTTP status.
	if status >= 200 && status < 300 {
		if eb := parseErrBody(body); eb.Present && eb.CodeNum >= 400 {
			pe := p.Classify(eb.CodeNum, h, body)
			return pe
		}
		return Classify(status, h, body)
	}
	pe := Classify(status, h, body)
	switch status {
	case http.StatusPaymentRequired:
		pe.Class = ErrorClassInsufficientCredit
	case http.StatusForbidden: // input flagged by moderation
		pe.Class = ErrorClassBadRequest
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		pe.Class = ErrorClassTransient
	}
	return pe
}

func (p *OpenRouterProvider) Complete(ctx context.Context, req *ChatRequest, apiKey string) (*ChatResponse, error) {
	return doOpenAIComplete(ctx, p.baseURL(), apiKey, req, p.headers(), p)
}

func (p *OpenRouterProvider) Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error) {
	return doOpenAIStream(ctx, p.baseURL(), apiKey, req, p.headers(), p)
}

func (p *OpenRouterProvider) HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error) {
	return doOpenAIHealthCheck(ctx, p.baseURL(), apiKey, "openai/gpt-oss-20b:free", p.headers(), p)
}

func (p *OpenRouterProvider) ProbeModel(ctx context.Context, apiKey, model string) (*HealthResult, error) {
	return doOpenAIProbeModel(ctx, p.baseURL(), apiKey, model, p.headers(), p)
}

// ListModels queries OpenRouter's model catalog (paid and :free), which returns pricing natively.
func (p *OpenRouterProvider) ListModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	b, err := doGetJSON(ctx, strings.TrimRight(p.baseURL(), "/")+"/models", bearer(apiKey, p.headers()), p)
	if err != nil {
		return nil, err
	}
	var result struct {
		Data []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			ContextLen int    `json:"context_length"`
			Pricing    struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
			TopProvider struct {
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, &ProviderError{Class: ErrorClassTransient, Code: "invalid_response", Message: "unparseable model list"}
	}

	models := make([]ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID == "" {
			continue
		}
		paramB, arch := LookupModelMeta(m.ID)
		models = append(models, ModelInfo{
			ID:                m.ID,
			DisplayName:       m.Name,
			ContextWindow:     m.ContextLen,
			MaxOutputTokens:   m.TopProvider.MaxCompletionTokens,
			SupportsStreaming: true,
			InputCostPerMTok:  parseMillionPrice(m.Pricing.Prompt),
			OutputCostPerMTok: parseMillionPrice(m.Pricing.Completion),
			ParameterCountB:   paramB,
			Architecture:      arch,
			Extra:             map[string]any{},
		})
	}
	return models, nil
}
