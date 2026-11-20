package providers

import (
	"context"
	"strings"
)

type GroqProvider struct{}

func (g *GroqProvider) Name() string { return "groq" }

func (g *GroqProvider) DefaultModels() []string {
	return []string{"llama-3.3-70b-versatile", "llama-3.1-8b-instant"}
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
	return doOpenAIHealthCheck(ctx, g.baseURL(), apiKey, "llama-3.3-70b-versatile", nil)
}

func (g *GroqProvider) ProbeModel(ctx context.Context, apiKey, model string) (*HealthResult, error) {
	return doOpenAIProbeModel(ctx, g.baseURL(), apiKey, model, nil)
}

// Groq pricing (USD per million tokens) sourced from https://console.groq.com/docs/models
var groqPricing = map[string][2]float64{
	"llama-3.3-70b-versatile":        {0.59, 0.79},
	"llama-3.3-70b-specdec":          {0.59, 0.99},
	"llama-3.1-70b-versatile":        {0.59, 0.79},
	"llama-3.1-8b-instant":           {0.05, 0.08},
	"llama3-70b-8192":                {0.59, 0.79},
	"llama3-8b-8192":                 {0.05, 0.08},
	"llama-3.2-90b-vision-preview":   {0.90, 0.90},
	"llama-3.2-11b-vision-preview":   {0.18, 0.18},
	"llama-3.2-3b-preview":           {0.06, 0.06},
	"llama-3.2-1b-preview":           {0.04, 0.04},
	"mixtral-8x7b-32768":             {0.24, 0.24},
	"gemma2-9b-it":                   {0.20, 0.20},
	"gemma-7b-it":                    {0.07, 0.07},
	"llama-guard-3-8b":               {0.20, 0.20},
	"deepseek-r1-distill-llama-70b":  {0.75, 0.99},
	"qwen-qwq-32b":                   {0.29, 0.39},
}

func (g *GroqProvider) ListModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	raw, err := doListModelsOpenAI(ctx, g.baseURL(), apiKey, nil)
	if err != nil {
		return nil, err
	}
	models := make([]ModelInfo, 0, len(raw))
	for _, m := range raw {
		id := jsonStr(m, "id")
		// Skip audio/transcription/guard models
		if isNonChatModel(id) {
			continue
		}
		paramB, arch := LookupModelMeta(id)
		info := ModelInfo{
			ID:               id,
			DisplayName:      id,
			ContextWindow:    jsonInt(m, "context_window"),
			MaxOutputTokens:  8192,
			SupportsStreaming: true,
			ParameterCountB:  paramB,
			Architecture:     arch,
			Extra:            map[string]any{"owned_by": jsonStr(m, "owned_by")},
		}
		if p, ok := groqPricing[id]; ok {
			info.InputCostPerMTok = p[0]
			info.OutputCostPerMTok = p[1]
		}
		models = append(models, info)
	}
	return models, nil
}

func isNonChatModel(id string) bool {
	for _, prefix := range []string{"whisper", "distil-whisper"} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}
