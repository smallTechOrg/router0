package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	return doOpenAIComplete(ctx, p.openAICompatURL(), apiKey, req, nil)
}

func (p *GoogleProvider) Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error) {
	return doOpenAIStream(ctx, p.openAICompatURL(), apiKey, req, nil)
}

func (p *GoogleProvider) ProbeModel(ctx context.Context, apiKey, model string) (*HealthResult, error) {
	return doOpenAIProbeModel(ctx, p.openAICompatURL(), apiKey, model, nil)
}

func (p *GoogleProvider) HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error) {
	start := time.Now()
	url := fmt.Sprintf("%s/v1beta/models?key=%s", p.baseURL(), url.QueryEscape(apiKey))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return &HealthResult{OK: false, Error: err.Error()}, nil
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return &HealthResult{OK: false, LatencyMs: latency, Error: err.Error()}, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return &HealthResult{OK: false, LatencyMs: latency, Error: fmt.Sprintf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))}, nil
	}
	return &HealthResult{OK: true, LatencyMs: latency}, nil
}

// Google pricing (USD per million tokens) — from https://ai.google.dev/pricing
var googlePricing = map[string][2]float64{
	"gemini-2.0-flash":           {0.10, 0.40},
	"gemini-2.0-flash-lite":      {0.075, 0.30},
	"gemini-1.5-flash":           {0.075, 0.30},
	"gemini-1.5-flash-8b":        {0.0375, 0.15},
	"gemini-1.5-pro":             {1.25, 5.00},
	"gemini-1.0-pro":             {0.50, 1.50},
}

func (p *GoogleProvider) ListModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	url := fmt.Sprintf("%s/v1beta/models?key=%s&pageSize=100", p.baseURL(), url.QueryEscape(apiKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			InputTokenLimit            int      `json:"inputTokenLimit"`
			OutputTokenLimit           int      `json:"outputTokenLimit"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
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
			ID:               shortID,
			DisplayName:      m.DisplayName,
			ContextWindow:    m.InputTokenLimit,
			MaxOutputTokens:  m.OutputTokenLimit,
			SupportsStreaming: true,
			ParameterCountB:  paramB,
			Architecture:     arch,
			Extra:            map[string]any{"full_name": m.Name},
		}
		if pr, ok := googlePricing[shortID]; ok {
			info.InputCostPerMTok = pr[0]
			info.OutputCostPerMTok = pr[1]
		}
		models = append(models, info)
	}
	return models, nil
}

// googleStreamChunk reads the native Google streaming format (if needed)
var _ = bufio.NewScanner
var _ = bytes.NewReader
var _ = json.Marshal
