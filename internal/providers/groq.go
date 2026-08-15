package providers

import "context"

type GroqProvider struct{}

func (g *GroqProvider) Name() string { return "groq" }

func (g *GroqProvider) DefaultModels() []string {
	return []string{"llama-3.1-70b-versatile", "mixtral-8x7b-32768"}
}

func (g *GroqProvider) baseURL() string {
	return envOrDef("GROQ_BASE_URL", "https://api.groq.com/openai/v1")
}

func (g *GroqProvider) Complete(ctx context.Context, req *ChatRequest, apiKey string) (*ChatResponse, error) {
	return doOpenAIComplete(ctx, g.baseURL(), apiKey, req, nil)
}

func (g *GroqProvider) Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error) {
	return doOpenAIStream(ctx, g.baseURL(), apiKey, req, nil)
}

func (g *GroqProvider) HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error) {
	return doOpenAIHealthCheck(ctx, g.baseURL(), apiKey, "llama-3.1-70b-versatile", nil)
}
