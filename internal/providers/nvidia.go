package providers

import "context"

type NvidiaProvider struct{}

func (p *NvidiaProvider) Name() string { return "nvidia" }

func (p *NvidiaProvider) DefaultModels() []string {
	return []string{"meta/llama-3.1-70b-instruct", "meta/llama-3.1-8b-instruct"}
}

func (p *NvidiaProvider) baseURL() string {
	return envOrDef("NVIDIA_BASE_URL", "https://integrate.api.nvidia.com/v1")
}

func (p *NvidiaProvider) Complete(ctx context.Context, req *ChatRequest, apiKey string) (*ChatResponse, error) {
	return doOpenAIComplete(ctx, p.baseURL(), apiKey, req, nil)
}

func (p *NvidiaProvider) Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error) {
	return doOpenAIStream(ctx, p.baseURL(), apiKey, req, nil)
}

func (p *NvidiaProvider) HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error) {
	return doOpenAIHealthCheck(ctx, p.baseURL(), apiKey, "meta/llama-3.1-8b-instruct", nil)
}
