package providers

import (
	"bytes"
	"encoding/json"
	"strings"
)

const anthropicDefaultMaxTokens = 4096

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   *string         `json:"content,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
}

type anthropicToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type anthropicRequest struct {
	Model         string               `json:"model"`
	MaxTokens     int                  `json:"max_tokens"`
	System        string               `json:"system,omitempty"`
	Messages      []anthropicMessage   `json:"messages"`
	Temperature   *float64             `json:"temperature,omitempty"`
	TopP          *float64             `json:"top_p,omitempty"`
	StopSequences []string             `json:"stop_sequences,omitempty"`
	Tools         []anthropicTool      `json:"tools,omitempty"`
	ToolChoice    *anthropicToolChoice `json:"tool_choice,omitempty"`
	Stream        bool                 `json:"stream,omitempty"`
}

func badRequest(code, msg string) *ProviderError {
	return &ProviderError{Class: ErrorClassBadRequest, HTTP: 400, Code: code, Message: msg}
}

// buildAnthropicBody translates an OpenAI-shaped ChatRequest into the JSON body
// for POST /v1/messages. Errors are *ProviderError (bad_request) raised before
// any upstream call.
func buildAnthropicBody(req *ChatRequest, stream bool) ([]byte, error) {
	out := anthropicRequest{
		Model:         req.Model,
		MaxTokens:     req.MaxTokens,
		TopP:          req.TopP,
		StopSequences: req.Stop,
		Stream:        stream,
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = anthropicDefaultMaxTokens
	}
	if req.Temperature != nil {
		t := *req.Temperature
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
		out.Temperature = &t
	}

	var system []string
	msgs := []anthropicMessage{}
	add := func(role string, blocks ...anthropicBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
			return
		}
		msgs = append(msgs, anthropicMessage{Role: role, Content: blocks})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			if m.Content != "" {
				system = append(system, m.Content)
			}
		case "user":
			if m.Content != "" {
				add("user", anthropicBlock{Type: "text", Text: m.Content})
			}
		case "assistant":
			var blocks []anthropicBlock
			if m.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				input, err := normalizeToolInput(tc.Function.Arguments)
				if err != nil {
					return nil, badRequest("invalid_tool_arguments", "assistant tool_call arguments are not a valid JSON object")
				}
				blocks = append(blocks, anthropicBlock{Type: "tool_use", ID: tc.ID, Name: tc.Function.Name, Input: input})
			}
			add("assistant", blocks...)
		case "tool", "function":
			if m.ToolCallID == "" {
				return nil, badRequest("invalid_tool_message", "tool message is missing tool_call_id")
			}
			c := m.Content
			add("user", anthropicBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: &c})
		default:
			return nil, badRequest("invalid_role", "unsupported message role")
		}
	}
	out.System = strings.Join(system, "\n\n")
	out.Messages = msgs

	for _, t := range req.Tools {
		if t.Type != "" && t.Type != "function" {
			continue
		}
		schema := t.Function.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out.Tools = append(out.Tools, anthropicTool{Name: t.Function.Name, Description: t.Function.Description, InputSchema: schema})
	}
	if len(out.Tools) > 0 {
		tc, err := mapToolChoice(req.ToolChoice)
		if err != nil {
			return nil, err
		}
		out.ToolChoice = tc
	}
	return json.Marshal(out)
}

// normalizeToolInput validates tool_call arguments as a JSON object; empty -> {}.
func normalizeToolInput(args string) (json.RawMessage, error) {
	a := strings.TrimSpace(args)
	if a == "" {
		return json.RawMessage("{}"), nil
	}
	if a[0] != '{' || !json.Valid([]byte(a)) {
		return nil, errInvalidArgs
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(a)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var errInvalidArgs = &ProviderError{Class: ErrorClassBadRequest}

func mapToolChoice(tc any) (*anthropicToolChoice, error) {
	if tc == nil {
		return nil, nil
	}
	if s, ok := tc.(string); ok {
		return mapToolChoiceString(s)
	}
	b, err := json.Marshal(tc)
	if err != nil {
		return nil, badRequest("invalid_tool_choice", "tool_choice is not valid")
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		return mapToolChoiceString(s)
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
		Name string `json:"name"`
	}
	if json.Unmarshal(b, &obj) != nil {
		return nil, badRequest("invalid_tool_choice", "tool_choice is not valid")
	}
	name := obj.Function.Name
	if name == "" {
		name = obj.Name
	}
	if obj.Type == "function" || obj.Type == "tool" {
		if name == "" {
			return nil, badRequest("invalid_tool_choice", "tool_choice function name is missing")
		}
		return &anthropicToolChoice{Type: "tool", Name: name}, nil
	}
	return mapToolChoiceString(obj.Type)
}

func mapToolChoiceString(s string) (*anthropicToolChoice, error) {
	switch s {
	case "auto":
		return &anthropicToolChoice{Type: "auto"}, nil
	case "none":
		return &anthropicToolChoice{Type: "none"}, nil
	case "required", "any":
		return &anthropicToolChoice{Type: "any"}, nil
	}
	return nil, badRequest("invalid_tool_choice", "unsupported tool_choice")
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// toUsage: prompt tokens include cache creation/read tokens.
func (u anthropicUsage) toUsage() Usage {
	p := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	return Usage{PromptTokens: p, CompletionTokens: u.OutputTokens, TotalTokens: p + u.OutputTokens}
}

func mapStopReason(r string) string {
	switch r {
	case "end_turn", "stop_sequence", "pause_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	case "":
		return ""
	}
	return "stop"
}

func parseAnthropicResponse(raw []byte) (*ChatResponse, error) {
	var r struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage anthropicUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	out := &ChatResponse{ID: r.ID, Model: r.Model, FinishReason: mapStopReason(r.StopReason), Usage: r.Usage.toUsage()}
	var text strings.Builder
	for _, b := range r.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			args := "{}"
			if len(b.Input) > 0 && string(b.Input) != "null" {
				var buf bytes.Buffer
				if json.Compact(&buf, b.Input) == nil {
					args = buf.String()
				}
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.ID, Type: "function", Function: ToolCallFunction{Name: b.Name, Arguments: args}})
		}
	}
	out.Content = text.String()
	return out, nil
}
