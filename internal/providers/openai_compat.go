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
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, &ProviderError{Class: ErrorClassUpstream, Message: err.Error()}
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
	content := ""
	if len(r.Choices) > 0 && r.Choices[0].Message != nil {
		content = r.Choices[0].Message.Content
	}
	return &ChatResponse{
		ID:      r.ID,
		Model:   r.Model,
		Content: content,
		Usage:   Usage{PromptTokens: r.Usage.PromptTokens, CompletionTokens: r.Usage.CompletionTokens, TotalTokens: r.Usage.TotalTokens},
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
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, &ProviderError{Class: ErrorClassUpstream, Message: err.Error()}
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
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			content := ""
			if len(chunk.Choices) > 0 {
				content = chunk.Choices[0].Delta.Content
			}
			select {
			case ch <- ChatChunk{ID: chunk.ID, Model: chunk.Model, Content: content, Raw: line}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func doOpenAIHealthCheck(ctx context.Context, baseURL, apiKey, model string, extraHeaders map[string]string) (*HealthResult, error) {
	start := time.Now()
	req := &ChatRequest{
		Model:     model,
		Messages:  []ChatMessage{{Role: "user", Content: "1"}},
		MaxTokens: 1,
	}
	_, err := doOpenAIComplete(ctx, baseURL, apiKey, req, extraHeaders)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		if pe, ok := err.(*ProviderError); ok {
			return &HealthResult{OK: false, LatencyMs: latency, Error: string(pe.Class)}, nil
		}
		return &HealthResult{OK: false, LatencyMs: latency, Error: err.Error()}, nil
	}
	return &HealthResult{OK: true, LatencyMs: latency}, nil
}
