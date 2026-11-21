package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// meshAPIVersion pins Mesh's date-versioned API contract (docs: "Pin the API
// contract to a date"). Without the header Mesh serves its OLDEST supported
// version, so a deliberate pin keeps the response shapes this adapter parses
// stable even if Mesh publishes newer versions.
const meshAPIVersion = "2026-08"

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

// headers are the extra request headers sent on every Mesh call.
func (m *MeshAPIProvider) headers() map[string]string {
	return map[string]string{"X-Mesh-Version": meshAPIVersion}
}

func (m *MeshAPIProvider) baseURL() string {
	return strings.TrimRight(envOrDef("MESHAPI_BASE_URL", "https://api.meshapi.ai/v1"), "/")
}

func (m *MeshAPIProvider) Complete(ctx context.Context, req *ChatRequest, apiKey string) (*ChatResponse, error) {
	return doOpenAIComplete(ctx, m.baseURL(), apiKey, req, m.headers(), nil)
}

func (m *MeshAPIProvider) HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error) {
	return doOpenAIHealthCheck(ctx, m.baseURL(), apiKey, m.DefaultModels()[0], m.headers(), nil)
}

func (m *MeshAPIProvider) ProbeModel(ctx context.Context, apiKey, model string) (*HealthResult, error) {
	return doOpenAIProbeModel(ctx, m.baseURL(), apiKey, model, m.headers(), nil)
}

// ListModels reads Mesh's catalogue. Unlike OpenAI's {"data":[...]} wrapper,
// Mesh's GET /v1/models returns a BARE JSON ARRAY of model objects (the wrapper
// is still accepted defensively). Each model declares its own `model_type`
// (text, embedding, image, audio, video, evaluation) and whether it supports the
// chat-completions API; that is surfaced as Extra["kind"], which the catalog
// treats as authoritative over name-pattern rules.
func (m *MeshAPIProvider) ListModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	b, err := doGetJSON(ctx, m.baseURL()+"/models", bearer(apiKey, m.headers()), nil)
	if err != nil {
		return nil, err
	}
	raw, ok := parseMeshModels(b)
	if !ok {
		return nil, &ProviderError{Class: ErrorClassTransient, Code: "invalid_response", Message: "unparseable model list"}
	}
	models := make([]ModelInfo, 0, len(raw))
	for _, r := range raw {
		id := jsonStr(r, "id")
		if id == "" {
			continue
		}
		cw := jsonInt(r, "context_length")
		if cw == 0 {
			cw = jsonInt(r, "context_window")
		}
		name := jsonStr(r, "name")
		if name == "" {
			name = id
		}
		extra := map[string]any{"kind": meshKind(r)}
		if v, ok := r["supports_tools"].(bool); ok {
			extra["supports_tools"] = v
		}
		models = append(models, ModelInfo{ID: id, DisplayName: name, ContextWindow: cw, SupportsStreaming: true, Extra: extra})
	}
	return models, nil
}

// parseMeshModels accepts Mesh's bare array or an OpenAI-style {"data":[...]}.
func parseMeshModels(b []byte) ([]map[string]any, bool) {
	var arr []map[string]any
	if err := json.Unmarshal(b, &arr); err == nil {
		return arr, true
	}
	var wrapped struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &wrapped); err == nil && wrapped.Data != nil {
		return wrapped.Data, true
	}
	return nil, false
}

// meshKind maps Mesh's declared model_type to a catalog kind. A text model that
// does not support the chat-completions API (Responses-API-only) cannot be
// served by router0's /v1/chat/completions, so it is "other".
func meshKind(r map[string]any) string {
	switch strings.ToLower(jsonStr(r, "model_type")) {
	case "text":
		if v, ok := r["supports_completions_api"].(bool); ok && !v {
			return "other"
		}
		return "chat"
	case "embedding":
		return "embedding"
	case "image", "video":
		return "image"
	case "audio":
		return "audio"
	case "":
		return "" // unknown: let the catalog's name rules decide
	default:
		return "other"
	}
}

// Stream opens the SSE stream. If the upstream rejects stream_options with an
// HTTP 400, it retries once without it (usage is then simply not reported).
func (m *MeshAPIProvider) Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error) {
	base := m.baseURL()
	ch, err := doOpenAIStream(ctx, base, apiKey, req, m.headers(), nil)
	if err == nil {
		return ch, nil
	}
	pe, ok := err.(*ProviderError)
	if !ok || pe.HTTP != http.StatusBadRequest || !strings.Contains(pe.Message+" "+pe.Code, "stream_options") {
		return nil, err
	}
	wire := buildWireReq(req, true)
	wire.StreamOptions = nil
	return meshStreamWire(ctx, base, apiKey, wire, m.headers())
}

// meshStreamWire is doOpenAIStream's SSE loop for a prebuilt wire request
// (duplicated because the shared helper always builds the wire itself).
func meshStreamWire(ctx context.Context, base, apiKey string, wire openAICompletionReq, headers map[string]string) (<-chan ChatChunk, error) {
	httpReq, err := newChatRequest(ctx, base, apiKey, wire, headers)
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
