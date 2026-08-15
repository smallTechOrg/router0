package providers

import (
	"context"
	"fmt"
)

// ChatMessage is a single message in the conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the unified request format passed to providers.
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	Stop        []string      `json:"stop,omitempty"`
}

// Usage contains token counts.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse is a non-streaming response.
type ChatResponse struct {
	ID      string  `json:"id"`
	Model   string  `json:"model"`
	Content string  `json:"content"`
	Usage   Usage   `json:"usage"`
	Cost    float64 `json:"cost,omitempty"` // OpenRouter reports this directly
}

// ChatChunk is a single streaming chunk.
type ChatChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content string `json:"content"`
	Done    bool   `json:"done"`
	// Raw SSE line for passthrough
	Raw string `json:"raw,omitempty"`
}

// HealthResult is the result of a 1-token health probe.
type HealthResult struct {
	OK        bool
	LatencyMs int64
	Error     string
}

// ErrorClass classifies provider errors for routing decisions.
type ErrorClass string

const (
	ErrorClassRateLimit   ErrorClass = "rate_limit"
	ErrorClassAuthError   ErrorClass = "auth_error"
	ErrorClassUpstream    ErrorClass = "upstream_error"
	ErrorClassTimeout     ErrorClass = "timeout"
	ErrorClassUnavailable ErrorClass = "unavailable"
)

// ProviderError wraps a provider-specific error.
type ProviderError struct {
	Class   ErrorClass
	Message string
	HTTP    int
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider error [%s]: %s (http %d)", e.Class, e.Message, e.HTTP)
}

// Provider is the interface every provider adapter must implement.
type Provider interface {
	Name() string
	// Complete performs a non-streaming completion.
	Complete(ctx context.Context, req *ChatRequest, apiKey string) (*ChatResponse, error)
	// Stream performs a streaming completion; returns a channel of chunks.
	Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error)
	// HealthCheck performs a minimal-token probe.
	HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error)
	// DefaultModels returns the default models for this provider.
	DefaultModels() []string
}
