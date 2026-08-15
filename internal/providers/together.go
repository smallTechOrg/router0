package providers

import "context"

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
