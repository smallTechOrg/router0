package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type GoogleProvider struct{}

func (p *GoogleProvider) Name() string { return "google" }

func (p *GoogleProvider) DefaultModels() []string {
	return []string{"gemini-1.5-flash", "gemini-1.5-pro"}
}

func (p *GoogleProvider) baseURL() string {
	return envOrDef("GOOGLE_BASE_URL", "https://generativelanguage.googleapis.com")
}

// Google uses a different API format but for simplicity we proxy via OpenAI-compat endpoint
// which Google's Gemini API supports at v1beta/openai/
func (p *GoogleProvider) openAICompatURL() string {
	base := p.baseURL()
	// Remove trailing slash
	base = strings.TrimRight(base, "/")
	return base + "/v1beta/openai"
}

func (p *GoogleProvider) Complete(ctx context.Context, req *ChatRequest, apiKey string) (*ChatResponse, error) {
	return doOpenAIComplete(ctx, p.openAICompatURL(), apiKey, req, nil)
}

func (p *GoogleProvider) Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error) {
	return doOpenAIStream(ctx, p.openAICompatURL(), apiKey, req, nil)
}

func (p *GoogleProvider) HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error) {
	start := time.Now()
	// Use native Gemini API to list models as a health probe
	url := fmt.Sprintf("%s/v1beta/models?key=%s", p.baseURL(), apiKey)
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
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return &HealthResult{OK: false, LatencyMs: latency, Error: fmt.Sprintf("http %d", resp.StatusCode)}, nil
	}
	return &HealthResult{OK: true, LatencyMs: latency}, nil
}

// googleStreamChunk reads the native Google streaming format (if needed)
var _ = bufio.NewScanner
var _ = bytes.NewReader
var _ = json.Marshal
