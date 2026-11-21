package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// sharedTransport is reused by every adapter call (keep-alive pooling). There is
// deliberately no client-level timeout: the caller bounds each attempt via ctx.
var sharedTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          200,
	MaxIdleConnsPerHost:   32,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: time.Second,
}

var sharedClient = &http.Client{Transport: sharedTransport}

const (
	sseScannerBuf   = 1 << 20  // 1 MiB max SSE line
	maxResponseBody = 32 << 20 // cap for non-stream success bodies
)

// openAICompletionReq is the wire format sent to OpenAI-compatible APIs.
type openAICompletionReq struct {
	Model          string         `json:"model"`
	Messages       []ChatMessage  `json:"messages"`
	MaxTokens      int            `json:"max_tokens,omitempty"`
	Temperature    *float64       `json:"temperature,omitempty"`
	TopP           *float64       `json:"top_p,omitempty"`
	Seed           *int           `json:"seed,omitempty"`
	User           string         `json:"user,omitempty"`
	Stream         bool           `json:"stream"`
	StreamOptions  *streamOptions `json:"stream_options,omitempty"`
	Stop           []string       `json:"stop,omitempty"`
	Tools          []Tool         `json:"tools,omitempty"`
	ToolChoice     any            `json:"tool_choice,omitempty"`
	ResponseFormat any            `json:"response_format,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func buildWireReq(req *ChatRequest, stream bool) openAICompletionReq {
	w := openAICompletionReq{
		Model:          req.Model,
		Messages:       req.Messages,
		MaxTokens:      req.MaxTokens,
		Temperature:    req.Temperature,
		TopP:           req.TopP,
		Seed:           req.Seed,
		User:           req.User,
		Stream:         stream,
		Stop:           req.Stop,
		Tools:          req.Tools,
		ToolChoice:     req.ToolChoice,
		ResponseFormat: req.ResponseFormat,
	}
	if stream {
		w.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	return w
}

type openAIChoice struct {
	Message      *ChatMessage `json:"message,omitempty"`
	Delta        *ChatMessage `json:"delta,omitempty"`
	FinishReason string       `json:"finish_reason,omitempty"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openAIResponse struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   openAIUsage    `json:"usage"`
	Cost    float64        `json:"cost"`
}

func parseRateLimitHeaders(h http.Header) *RateLimitHeaders {
	rl := &RateLimitHeaders{}
	parse := func(key string) int {
		v := h.Get(key)
		if v == "" {
			return 0
		}
		n := 0
		fmt.Sscanf(v, "%d", &n)
		return n
	}
	// Groq / OpenAI style
	rl.LimitRPM = parse("X-Ratelimit-Limit-Requests")
	rl.RemainingRPM = parse("X-Ratelimit-Remaining-Requests")
	rl.LimitTPM = parse("X-Ratelimit-Limit-Tokens")
	rl.RemainingTPM = parse("X-Ratelimit-Remaining-Tokens")
	rl.LimitTPD = parse("X-Ratelimit-Limit-Tokens-Day")
	rl.RemainingTPD = parse("X-Ratelimit-Remaining-Tokens-Day")
	rl.ResetAt = h.Get("X-Ratelimit-Reset-Requests")
	if rl.ResetAt == "" {
		rl.ResetAt = h.Get("X-Ratelimit-Reset-Tokens")
	}
	// Together AI uses different header names
	if rl.LimitRPM == 0 {
		rl.LimitRPM = parse("X-Rate-Limit-Limit")
		rl.RemainingRPM = parse("X-Rate-Limit-Remaining")
	}
	return rl
}

func envOrDef(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func newChatRequest(ctx context.Context, baseURL, apiKey string, wire openAICompletionReq, extraHeaders map[string]string) (*http.Request, error) {
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	if wire.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	for k, v := range extraHeaders {
		httpReq.Header.Set(k, v)
	}
	return httpReq, nil
}

// doRequest sends the request; transport failures are classified (nil,err).
// A cancelled ctx yields the raw context error.
func doRequest(ctx context.Context, httpReq *http.Request) (*http.Response, error) {
	resp, err := sharedClient.Do(httpReq)
	if err != nil {
		if pe, cancelled := TransportError(ctx, err); !cancelled {
			return nil, pe
		}
		return nil, context.Canceled
	}
	return resp, nil
}

// failureFromResponse classifies a non-2xx response and closes the body.
func failureFromResponse(resp *http.Response, cls Classifier) *ProviderError {
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, MaxErrorBodyBytes))
	return classifyWith(cls, resp.StatusCode, resp.Header, b)
}

func doOpenAIComplete(ctx context.Context, baseURL, apiKey string, req *ChatRequest, extraHeaders map[string]string, cls Classifier) (*ChatResponse, error) {
	httpReq, err := newChatRequest(ctx, baseURL, apiKey, buildWireReq(req, false), extraHeaders)
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, failureFromResponse(resp, cls)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		if pe, cancelled := TransportError(ctx, err); !cancelled {
			return nil, pe
		}
		return nil, context.Canceled
	}
	if pe := classify200(cls, resp.Header, raw); pe != nil {
		return nil, pe
	}
	var r openAIResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, &ProviderError{Class: ErrorClassTransient, HTTP: resp.StatusCode, Code: "invalid_response", Message: "provider returned an unparseable response"}
	}
	var content string
	var toolCalls []ToolCall
	var finishReason string
	if len(r.Choices) > 0 {
		ch := r.Choices[0]
		finishReason = ch.FinishReason
		if ch.Message != nil {
			content = ch.Message.Content
			toolCalls = ch.Message.ToolCalls
		}
	}
	return &ChatResponse{
		ID:           r.ID,
		Model:        r.Model,
		Content:      content,
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		Usage:        Usage{PromptTokens: r.Usage.PromptTokens, CompletionTokens: r.Usage.CompletionTokens, TotalTokens: r.Usage.TotalTokens},
		Cost:         r.Cost,
		RateLimit:    parseRateLimitHeaders(resp.Header),
	}, nil
}

// doOpenAIStream opens the SSE stream. Errors before the stream starts are
// returned as *ProviderError. Errors after it starts arrive as ChatChunk.Err.
func doOpenAIStream(ctx context.Context, baseURL, apiKey string, req *ChatRequest, extraHeaders map[string]string, cls Classifier) (<-chan ChatChunk, error) {
	httpReq, err := newChatRequest(ctx, baseURL, apiKey, buildWireReq(req, true), extraHeaders)
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, failureFromResponse(resp, cls)
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
				send(ChatChunk{Err: classifyWith(cls, http.StatusOK, resp.Header, []byte(data)), Raw: line})
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
		// Reached EOF (or a read error) without [DONE]: interrupted stream.
		code := "stream_interrupted"
		if err := scanner.Err(); err != nil {
			code = "stream_read_error"
		}
		send(ChatChunk{Err: &ProviderError{Class: ErrorClassTransient, Code: code, Message: "upstream stream ended unexpectedly"}})
	}()
	return ch, nil
}

// doOpenAIProbeModel tests a specific model by sending a 1-token completion.
func doOpenAIProbeModel(ctx context.Context, baseURL, apiKey, model string, extraHeaders map[string]string, cls Classifier) (*HealthResult, error) {
	return doOpenAIHealthCheck(ctx, baseURL, apiKey, model, extraHeaders, cls)
}

func doOpenAIHealthCheck(ctx context.Context, baseURL, apiKey, model string, extraHeaders map[string]string, cls Classifier) (*HealthResult, error) {
	start := time.Now()
	req := &ChatRequest{
		Model:     model,
		Messages:  []ChatMessage{{Role: "user", Content: "1"}},
		MaxTokens: 1,
	}
	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := doOpenAIComplete(hctx, baseURL, apiKey, req, extraHeaders, cls)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		if pe, ok := err.(*ProviderError); ok {
			return &HealthResult{OK: false, LatencyMs: latency, Error: string(pe.Class)}, nil
		}
		return &HealthResult{OK: false, LatencyMs: latency, Error: fmt.Sprintf("%T", err)}, nil
	}
	return &HealthResult{OK: true, LatencyMs: latency, RateLimit: resp.RateLimit}, nil
}
