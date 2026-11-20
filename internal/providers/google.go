package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type GoogleProvider struct{}

func (p *GoogleProvider) Name() string { return "google" }

func (p *GoogleProvider) DefaultModels() []string {
	return []string{"gemini-2.0-flash", "gemini-1.5-flash-latest"}
}

func (p *GoogleProvider) baseURL() string {
	return envOrDef("GOOGLE_BASE_URL", "https://generativelanguage.googleapis.com")
}

func (p *GoogleProvider) openAICompatURL() string {
	base := strings.TrimRight(p.baseURL(), "/")
	return base + "/v1beta/openai"
}

func (p *GoogleProvider) Complete(ctx context.Context, req *ChatRequest, apiKey string) (*ChatResponse, error) {
	return doOpenAIComplete(ctx, p.openAICompatURL(), apiKey, req, nil, nil)
}

func (p *GoogleProvider) Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error) {
	return doOpenAIStream(ctx, p.openAICompatURL(), apiKey, req, nil, nil)
}

func (p *GoogleProvider) ProbeModel(ctx context.Context, apiKey, model string) (*HealthResult, error) {
	return doOpenAIProbeModel(ctx, p.openAICompatURL(), apiKey, model, nil, nil)
}

func (p *GoogleProvider) HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error) {
	start := time.Now()
	u := fmt.Sprintf("%s/v1beta/models?key=%s", strings.TrimRight(p.baseURL(), "/"), url.QueryEscape(apiKey))
	_, err := doGetJSON(ctx, u, nil, nil)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		if pe, ok := err.(*ProviderError); ok {
			return &HealthResult{OK: false, LatencyMs: latency, Error: string(pe.Class)}, nil
		}
		return &HealthResult{OK: false, LatencyMs: latency, Error: "request_failed"}, nil
	}
	return &HealthResult{OK: true, LatencyMs: latency}, nil
}

// Google pricing (USD per million tokens) — from https://ai.google.dev/pricing
var googlePricing = map[string][2]float64{
	"gemini-2.0-flash":      {0.10, 0.40},
	"gemini-2.0-flash-lite": {0.075, 0.30},
	"gemini-1.5-flash":      {0.075, 0.30},
	"gemini-1.5-flash-8b":   {0.0375, 0.15},
	"gemini-1.5-pro":        {1.25, 5.00},
	"gemini-1.0-pro":        {0.50, 1.50},
}

func (p *GoogleProvider) ListModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	u := fmt.Sprintf("%s/v1beta/models?key=%s&pageSize=100", strings.TrimRight(p.baseURL(), "/"), url.QueryEscape(apiKey))
	b, err := doGetJSON(ctx, u, nil, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			InputTokenLimit            int      `json:"inputTokenLimit"`
			OutputTokenLimit           int      `json:"outputTokenLimit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, &ProviderError{Class: ErrorClassTransient, Code: "invalid_response", Message: "unparseable model list"}
	}

	models := make([]ModelInfo, 0, len(result.Models))
	for _, m := range result.Models {
		// Only include models that support generateContent
		supportsChat := false
		for _, method := range m.SupportedGenerationMethods {
			if method == "generateContent" {
				supportsChat = true
				break
			}
		}
		if !supportsChat {
			continue
		}
		// Name is like "models/gemini-1.5-flash" — extract short ID
		shortID := m.Name
		if idx := strings.LastIndex(m.Name, "/"); idx >= 0 {
			shortID = m.Name[idx+1:]
		}
		paramB, arch := LookupModelMeta(shortID)
		info := ModelInfo{
			ID:                shortID,
			DisplayName:       m.DisplayName,
			ContextWindow:     m.InputTokenLimit,
			MaxOutputTokens:   m.OutputTokenLimit,
			SupportsStreaming: true,
			ParameterCountB:   paramB,
			Architecture:      arch,
			Extra:             map[string]any{"full_name": m.Name},
		}
		if pr, ok := googlePricing[shortID]; ok {
			info.InputCostPerMTok = pr[0]
			info.OutputCostPerMTok = pr[1]
		}
		models = append(models, info)
	}
	return models, nil
}

func init() { Register(&GoogleProvider{}) }
