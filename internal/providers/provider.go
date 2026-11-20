package providers

import (
	"context"
	"fmt"
)

// ToolFunction describes a callable function in a tool definition.
type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// Tool is a tool definition passed to the model.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolCallFunction holds the function name and arguments for a tool call.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall represents a single tool invocation in an assistant message.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ChatMessage is a single message in the conversation.
// Content is a string for text-only messages; ToolCalls is populated for
// assistant messages that invoke tools; ToolCallID identifies tool-result messages.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ChatRequest is the unified request format passed to providers.
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	Stop        []string      `json:"stop,omitempty"`
	Tools       []Tool        `json:"tools,omitempty"`
	ToolChoice  any           `json:"tool_choice,omitempty"`
}

// Usage contains token counts.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// RateLimitHeaders holds rate limit information parsed from provider response headers.
type RateLimitHeaders struct {
	LimitRPM   int
	RemainingRPM int
	LimitTPM   int
	RemainingTPM int
	LimitTPD   int
	RemainingTPD int
	ResetAt    string
}

// ChatResponse is a non-streaming response.
type ChatResponse struct {
	ID           string            `json:"id"`
	Model        string            `json:"model"`
	Content      string            `json:"content"`
	ToolCalls    []ToolCall        `json:"tool_calls,omitempty"`
	FinishReason string            `json:"finish_reason,omitempty"`
	Usage        Usage             `json:"usage"`
	Cost         float64           `json:"cost,omitempty"` // OpenRouter reports this directly
	RateLimit    *RateLimitHeaders `json:"-"`
}

// ChatChunk is a single streaming chunk.
type ChatChunk struct {
	ID           string     `json:"id"`
	Model        string     `json:"model"`
	Content      string     `json:"content"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	FinishReason string     `json:"finish_reason,omitempty"`
	Done         bool       `json:"done"`
	// Raw SSE line for passthrough
	Raw string `json:"raw,omitempty"`
}

// HealthResult is the result of a 1-token health probe.
type HealthResult struct {
	OK        bool
	LatencyMs int64
	Error     string
	RateLimit *RateLimitHeaders // observed from probe response headers
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

// ModelInfo describes a model discovered from a provider's API.
type ModelInfo struct {
	ID                string
	DisplayName       string
	ContextWindow     int
	MaxOutputTokens   int
	InputCostPerMTok  float64 // USD per million input tokens
	OutputCostPerMTok float64 // USD per million output tokens
	SupportsStreaming  bool
	ParameterCountB   float64 // total parameters in billions (0 = unknown)
	Architecture      string  // e.g. "dense", "MoE", "SSM"
	Extra             map[string]any
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
	// DefaultModels returns the default models for this provider (used before discovery).
	DefaultModels() []string
	// ListModels queries the provider's API to discover available models and their metadata.
	ListModels(ctx context.Context, apiKey string) ([]ModelInfo, error)
}
