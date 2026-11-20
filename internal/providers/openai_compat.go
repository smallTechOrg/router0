package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// openAICompletionReq is the wire format sent to OpenAI-compatible APIs.
type openAICompletionReq struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	Stream      bool          `json:"stream"`
	Stop        []string      `json:"stop,omitempty"`
	Tools       []Tool        `json:"tools,omitempty"`
	ToolChoice  any           `json:"tool_choice,omitempty"`
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

func doOpenAIComplete(ctx context.Context, baseURL, apiKey string, req *ChatRequest, extraHeaders map[string]string) (*ChatResponse, error) {
	body, err := json.Marshal(openAICompletionReq{
		Model:       req.Model,
		Messages:    req.Messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      false,
		Stop:        req.Stop,
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
	})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	for k, v := range extraHeaders {
		httpReq.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, &ProviderError{Class: ErrorClassTimeout, Message: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		return nil, &ProviderError{Class: ErrorClassRateLimit, Message: "rate limit", HTTP: 429}
	}
	if resp.StatusCode == 401 {
		return nil, &ProviderError{Class: ErrorClassAuthError, Message: "auth error", HTTP: 401}
	}
	if resp.StatusCode >= 500 {
		return nil, &ProviderError{Class: ErrorClassUpstream, Message: fmt.Sprintf("provider returned %d", resp.StatusCode), HTTP: resp.StatusCode}
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, &ProviderError{Class: ErrorClassUpstream, Message: string(b), HTTP: resp.StatusCode}
	}
	var r openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
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
		RateLimit:    parseRateLimitHeaders(resp.Header),
	}, nil
}

func doOpenAIStream(ctx context.Context, baseURL, apiKey string, req *ChatRequest, extraHeaders map[string]string) (<-chan ChatChunk, error) {
	body, err := json.Marshal(openAICompletionReq{
		Model:       req.Model,
		Messages:    req.Messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      true,
		Stop:        req.Stop,
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
	})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")
	for k, v := range extraHeaders {
		httpReq.Header.Set(k, v)
	}
	// ResponseHeaderTimeout bounds connection+headers to 12s without affecting body reads.
	// Once streaming starts the body is read until ctx is cancelled or stream ends.
	streamClient := &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: 12 * time.Second,
		},
	}
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, &ProviderError{Class: ErrorClassTimeout, Message: err.Error()}
	}
	if resp.StatusCode == 429 {
		resp.Body.Close()
		return nil, &ProviderError{Class: ErrorClassRateLimit, Message: "rate limit", HTTP: 429}
	}
	if resp.StatusCode == 401 {
		resp.Body.Close()
		return nil, &ProviderError{Class: ErrorClassAuthError, Message: "auth error", HTTP: 401}
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &ProviderError{Class: ErrorClassUpstream, Message: string(b), HTTP: resp.StatusCode}
	}

	ch := make(chan ChatChunk, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				ch <- ChatChunk{Done: true, Raw: "data: [DONE]"}
				return
			}
			var chunk struct {
				ID      string `json:"id"`
				Model   string `json:"model"`
				Choices []struct {
					Delta        ChatMessage `json:"delta"`
					FinishReason string      `json:"finish_reason"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			var content string
			var toolCalls []ToolCall
			var finishReason string
			if len(chunk.Choices) > 0 {
				content = chunk.Choices[0].Delta.Content
				toolCalls = chunk.Choices[0].Delta.ToolCalls
				finishReason = chunk.Choices[0].FinishReason
			}
			select {
			case ch <- ChatChunk{ID: chunk.ID, Model: chunk.Model, Content: content, ToolCalls: toolCalls, FinishReason: finishReason, Raw: line}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// doOpenAIProbeModel tests a specific model by sending a 1-token completion.
func doOpenAIProbeModel(ctx context.Context, baseURL, apiKey, model string, extraHeaders map[string]string) (*HealthResult, error) {
	return doOpenAIHealthCheck(ctx, baseURL, apiKey, model, extraHeaders)
}

func doOpenAIHealthCheck(ctx context.Context, baseURL, apiKey, model string, extraHeaders map[string]string) (*HealthResult, error) {
	start := time.Now()
	req := &ChatRequest{
		Model:     model,
		Messages:  []ChatMessage{{Role: "user", Content: "1"}},
		MaxTokens: 1,
	}
	resp, err := doOpenAIComplete(ctx, baseURL, apiKey, req, extraHeaders)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		if pe, ok := err.(*ProviderError); ok {
			return &HealthResult{OK: false, LatencyMs: latency, Error: string(pe.Class)}, nil
		}
		return &HealthResult{OK: false, LatencyMs: latency, Error: err.Error()}, nil
	}
	return &HealthResult{OK: true, LatencyMs: latency, RateLimit: resp.RateLimit}, nil
}
