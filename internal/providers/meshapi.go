package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// MeshAPIProvider is the Mesh API (Fiesta Labs, "AI Fiesta") adapter: an
// OpenAI-compatible gateway. Vendor-prefixed model ids (e.g.
// "mistral/mistral-7b-instruct-v0") are passed upstream unchanged. Error
// handling uses the generic classification rules.
type MeshAPIProvider struct{}

func init() { Register(&MeshAPIProvider{}) }

func (m *MeshAPIProvider) Name() string { return "meshapi" }

func (m *MeshAPIProvider) DefaultModels() []string {
	return []string{"mistral/mistral-7b-instruct-v0", "meta-llama/llama-3.3-70b-instruct"}
}

func (m *MeshAPIProvider) baseURL() string {
	return strings.TrimRight(envOrDef("MESHAPI_BASE_URL", "https://api.meshapi.ai/v1"), "/")
}

func (m *MeshAPIProvider) Complete(ctx context.Context, req *ChatRequest, apiKey string) (*ChatResponse, error) {
	return doOpenAIComplete(ctx, m.baseURL(), apiKey, req, nil, nil)
}

func (m *MeshAPIProvider) HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error) {
	return doOpenAIHealthCheck(ctx, m.baseURL(), apiKey, m.DefaultModels()[0], nil, nil)
}

func (m *MeshAPIProvider) ProbeModel(ctx context.Context, apiKey, model string) (*HealthResult, error) {
	return doOpenAIProbeModel(ctx, m.baseURL(), apiKey, model, nil, nil)
}

func (m *MeshAPIProvider) ListModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	return doListModelsOpenAIAsModelInfo(ctx, m.baseURL(), apiKey, nil, nil)
}

// Stream opens the SSE stream. If the upstream rejects stream_options with an
// HTTP 400, it retries once without it (usage is then simply not reported).
func (m *MeshAPIProvider) Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error) {
	base := m.baseURL()
	ch, err := doOpenAIStream(ctx, base, apiKey, req, nil, nil)
	if err == nil {
		return ch, nil
	}
	pe, ok := err.(*ProviderError)
	if !ok || pe.HTTP != http.StatusBadRequest || !strings.Contains(pe.Message+" "+pe.Code, "stream_options") {
		return nil, err
	}
	wire := buildWireReq(req, true)
	wire.StreamOptions = nil
	return meshStreamWire(ctx, base, apiKey, wire)
}

// meshStreamWire is doOpenAIStream's SSE loop for a prebuilt wire request
// (duplicated because the shared helper always builds the wire itself).
func meshStreamWire(ctx context.Context, base, apiKey string, wire openAICompletionReq) (<-chan ChatChunk, error) {
	httpReq, err := newChatRequest(ctx, base, apiKey, wire, nil)
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, failureFromResponse(resp, nil)
	}
	ch := make(chan ChatChunk, 64)
	send := func(c ChatChunk) bool {
		select {
		case ch <- c:
			return true
		case <-ctx.Done():
			return false
		}
	}
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64<<10), sseScannerBuf)
		for scanner.Scan() {
			line := scanner.Text()
			data, ok := strings.CutPrefix(line, "data:")
			if !ok {
				continue
			}
			data = strings.TrimSpace(data)
			if data == "" {
				continue
			}
			if data == "[DONE]" {
				send(ChatChunk{Done: true, Raw: "data: [DONE]"})
				return
			}
			var chunk struct {
				ID      string `json:"id"`
				Model   string `json:"model"`
				Choices []struct {
					Delta        ChatMessage `json:"delta"`
					FinishReason string      `json:"finish_reason"`
				} `json:"choices"`
				Usage *openAIUsage    `json:"usage"`
				Error json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
				send(ChatChunk{Err: classifyWith(nil, http.StatusOK, resp.Header, []byte(data)), Raw: line})
				return
			}
			out := ChatChunk{ID: chunk.ID, Model: chunk.Model, Raw: line}
			if len(chunk.Choices) > 0 {
				out.Content = chunk.Choices[0].Delta.Content
				out.ToolCalls = chunk.Choices[0].Delta.ToolCalls
				out.FinishReason = chunk.Choices[0].FinishReason
			}
			if chunk.Usage != nil {
				out.Usage = &Usage{PromptTokens: chunk.Usage.PromptTokens, CompletionTokens: chunk.Usage.CompletionTokens, TotalTokens: chunk.Usage.TotalTokens}
			}
			if !send(out) {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		code := "stream_interrupted"
		if err := scanner.Err(); err != nil {
			code = "stream_read_error"
		}
		send(ChatChunk{Err: &ProviderError{Class: ErrorClassTransient, Code: code, Message: "upstream stream ended unexpectedly"}})
	}()
	return ch, nil
}
