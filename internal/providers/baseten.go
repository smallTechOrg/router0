package providers

import "context"

type BasetenProvider struct{}

func (p *BasetenProvider) Name() string { return "baseten" }

func (p *BasetenProvider) DefaultModels() []string {
	return []string{}
}

func (p *BasetenProvider) baseURL() string {
	return envOrDef("BASETEN_BASE_URL", "https://bridge.baseten.co/v1/direct")
}

func (p *BasetenProvider) Complete(ctx context.Context, req *ChatRequest, apiKey string) (*ChatResponse, error) {
	return doOpenAIComplete(ctx, p.baseURL(), apiKey, req, nil, nil)
}

func (p *BasetenProvider) Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error) {
	return doOpenAIStream(ctx, p.baseURL(), apiKey, req, nil, nil)
}

func (p *BasetenProvider) HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error) {
	return &HealthResult{OK: true, LatencyMs: 0}, nil
}

// Baseten doesn't have a public model catalog API — models are user-deployed.
func (p *BasetenProvider) ListModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	return nil, nil
}

func init() { Register(&BasetenProvider{}) }
