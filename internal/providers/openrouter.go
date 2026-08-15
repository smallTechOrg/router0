package providers

import "context"

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
	return doOpenAIHealthCheck(ctx, p.baseURL(), apiKey, "openai/gpt-4o-mini", p.headers())
}
