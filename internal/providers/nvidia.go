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
	return doOpenAIComplete(ctx, p.baseURL(), apiKey, req, nil, nil)
}

func (p *NvidiaProvider) Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error) {
	return doOpenAIStream(ctx, p.baseURL(), apiKey, req, nil, nil)
}

func (p *NvidiaProvider) HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error) {
	return doOpenAIHealthCheck(ctx, p.baseURL(), apiKey, "meta/llama-3.1-8b-instruct", nil, nil)
}

func (p *NvidiaProvider) ProbeModel(ctx context.Context, apiKey, model string) (*HealthResult, error) {
	return doOpenAIProbeModel(ctx, p.baseURL(), apiKey, model, nil, nil)
}

// nvidiaContextWindows maps known NVIDIA NIM model IDs to their context windows.
// When ListModels returns 0, this map provides the fallback.
var nvidiaContextWindows = map[string]int{
	"meta/llama-3.1-8b-instruct":                    131072,
	"meta/llama-3.1-70b-instruct":                   131072,
	"meta/llama-3.1-405b-instruct":                  131072,
	"meta/llama-3.2-11b-vision-instruct":            131072,
	"meta/llama-3.2-90b-vision-instruct":            131072,
	"meta/llama-3.3-70b-instruct":                   131072,
	"nvidia/llama-3.1-nemotron-70b-instruct":        131072,
	"nvidia/llama-3.1-nemotron-nano-8b-v1":          131072,
	"nvidia/llama-3.3-nemotron-super-49b-v1":        131072,
	"nvidia/llama-3.3-nemotron-super-49b-v1.5":      131072,
	"nvidia/nemotron-3-super-120b-a12b":             131072,
	"nvidia/nemotron-3-ultra-550b-a55b":             131072,
	"nvidia/nemotron-3-nano-30b-a3b":                131072,
	"nvidia/nemotron-nano-12b-v2-vl":                131072,
	"nvidia/llama-3.1-nemotron-safety-guard-8b-v3":  131072,
	"nvidia/llama-3.1-nemoguard-8b-content-safety":  131072,
	"nvidia/llama-3.1-nemoguard-8b-topic-control":   131072,
	"nvidia/llama-3.1-nemotron-nano-vl-8b-v1":       131072,
	"nvidia/nvidia-nemotron-nano-9b-v2":             131072,
	"nvidia/nemotron-3-nano-omni-30b-a3b-reasoning": 131072,
	"nvidia/nemotron-3.5-lightning-30b-a3b":         131072,
	"nvidia/ising-calibration-1.5-31b":              131072,
	"nvidia/riva-translate-4b-instruct-v1.1":        131072,
	"nvidia/riva-translate-4b-instruct-v2":          131072,
	"mistralai/mistral-nemotron":                    131072,
	"mistralai/mistral-7b-instruct-v0.3":            32768,
	"mistralai/mixtral-8x7b-instruct-v0.1":          32768,
	"mistralai/mixtral-8x22b-instruct-v0.1":         65536,
	"google/gemma-4-31b-it":                         131072,
	"qwen/qwen2.5-72b-instruct":                     131072,
	"minimaxai/minimax-m3":                          1000000,
	"z-ai/glm-5.2":                                  131072,
	"openai/gpt-oss-20b":                            131072,
	"openai/gpt-oss-120b":                           131072,
	"poolside/laguna-xs-2.1":                        131072,
	"thinkingmachines/inkling":                      131072,
	"stepfun-ai/step-3.7-flash":                     131072,
	"meta/muse-glimmer-30b":                         131072,
	"nvidia/nemotron-mini-4b-instruct":              4096,
}

// NVIDIA NIM pricing (USD per million tokens) — from https://build.nvidia.com/pricing
var nvidiaPricing = map[string][2]float64{
	"meta/llama-3.1-405b-instruct":           {3.50, 3.50},
	"meta/llama-3.1-70b-instruct":            {0.35, 0.40},
	"meta/llama-3.1-8b-instruct":             {0.10, 0.10},
	"meta/llama-3.2-90b-vision-instruct":     {0.90, 0.90},
	"meta/llama-3.2-11b-vision-instruct":     {0.20, 0.20},
	"mistralai/mistral-7b-instruct-v0.3":     {0.06, 0.06},
	"mistralai/mixtral-8x7b-instruct-v0.1":   {0.24, 0.24},
	"mistralai/mixtral-8x22b-instruct-v0.1":  {0.40, 0.40},
	"google/gemma-2-9b-it":                   {0.20, 0.20},
	"google/gemma-2-27b-it":                  {0.40, 0.40},
	"microsoft/phi-3-mini-128k-instruct":     {0.10, 0.10},
	"microsoft/phi-3-medium-128k-instruct":   {0.14, 0.14},
	"nvidia/llama-3.1-nemotron-70b-instruct": {0.35, 0.40},
	"qwen/qwen2.5-72b-instruct":              {0.35, 0.40},
}

func (p *NvidiaProvider) ListModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	raw, err := doListModelsOpenAI(ctx, p.baseURL(), apiKey, nil, nil)
	if err != nil {
		return nil, err
	}
	models := make([]ModelInfo, 0, len(raw))
	for _, m := range raw {
		id := jsonStr(m, "id")
		ctxWin := jsonInt(m, "context_window")
		if ctxWin == 0 {
			ctxWin = nvidiaContextWindows[id]
		}
		if ctxWin == 0 {
			ctxWin = 131072 // default: most NVIDIA NIM models are 128K
		}
		paramB, arch := LookupModelMeta(id)
		info := ModelInfo{
			ID:                id,
			DisplayName:       id,
			ContextWindow:     ctxWin,
			MaxOutputTokens:   4096,
			SupportsStreaming: true,
			ParameterCountB:   paramB,
			Architecture:      arch,
			Extra:             map[string]any{},
		}
		if p, ok := nvidiaPricing[id]; ok {
			info.InputCostPerMTok = p[0]
			info.OutputCostPerMTok = p[1]
		}
		models = append(models, info)
	}
	return models, nil
}

func init() { Register(&NvidiaProvider{}) }
