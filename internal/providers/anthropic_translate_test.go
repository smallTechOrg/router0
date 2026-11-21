package providers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func anthFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "anthropic", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func weatherTools() []Tool {
	return []Tool{
		{Type: "function", Function: ToolFunction{Name: "get_weather", Description: "Get weather", Parameters: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []string{"city"}}}},
		{Type: "function", Function: ToolFunction{Name: "get_time"}},
	}
}

func roundTripRequest() *ChatRequest {
	return &ChatRequest{
		Model: "claude-sonnet-4-5",
		Messages: []ChatMessage{
			{Role: "system", Content: "Be brief."},
			{Role: "developer", Content: "Use tools."},
			{Role: "user", Content: "Weather in Paris and the time?"},
			{Role: "assistant", ToolCalls: []ToolCall{
				{ID: "toolu_P1", Type: "function", Function: ToolCallFunction{Name: "get_weather", Arguments: `{"city": "Paris"}`}},
				{ID: "toolu_P2", Type: "function", Function: ToolCallFunction{Name: "get_time", Arguments: ""}},
			}},
			{Role: "tool", ToolCallID: "toolu_P1", Content: "18C sunny"},
			{Role: "tool", ToolCallID: "toolu_P2", Content: "14:00"},
		},
		Tools:      weatherTools(),
		ToolChoice: "auto",
	}
}

func TestAnthropicTranslateRoundTripGolden(t *testing.T) {
	got, err := buildAnthropicBody(roundTripRequest(), false)
	if err != nil {
		t.Fatal(err)
	}
	want := string(anthFixture(t, "request_roundtrip.golden.json"))
	want = string(json.RawMessage(trimSpace([]byte(want))))
	if string(got) != want {
		t.Fatalf("request body mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestAnthropicTranslateToolChoiceMappings(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"auto", `{"type":"auto"}`},
		{"none", `{"type":"none"}`},
		{"required", `{"type":"any"}`},
		{map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}, `{"type":"tool","name":"get_weather"}`},
	}
	for _, c := range cases {
		body, err := buildAnthropicBody(&ChatRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "x"}}, Tools: weatherTools(), ToolChoice: c.in}, false)
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			ToolChoice json.RawMessage `json:"tool_choice"`
		}
		json.Unmarshal(body, &m)
		if string(m.ToolChoice) != c.want {
			t.Errorf("tool_choice %v: got %s want %s", c.in, m.ToolChoice, c.want)
		}
	}
	if _, err := buildAnthropicBody(&ChatRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "x"}}, Tools: weatherTools(), ToolChoice: "bogus"}, false); err == nil {
		t.Error("expected error for bogus tool_choice")
	}
}

func TestAnthropicTranslateDefaultsAndClamp(t *testing.T) {
	hot, neg := 1.7, -0.5
	for _, c := range []struct {
		temp *float64
		want string
	}{{&hot, `"temperature":1`}, {&neg, `"temperature":0`}} {
		b, _ := buildAnthropicBody(&ChatRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "x"}}, Temperature: c.temp, Stop: []string{"END"}}, true)
		s := string(b)
		for _, sub := range []string{`"max_tokens":4096`, c.want, `"stop_sequences":["END"]`, `"stream":true`} {
			if !contains(s, sub) {
				t.Errorf("missing %s in %s", sub, s)
			}
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestAnthropicTranslateBadToolArguments(t *testing.T) {
	_, err := buildAnthropicBody(&ChatRequest{Model: "m", Messages: []ChatMessage{
		{Role: "user", Content: "x"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "a", Type: "function", Function: ToolCallFunction{Name: "f", Arguments: "{not json"}}}},
	}}, false)
	pe, ok := err.(*ProviderError)
	if !ok || pe.Class != ErrorClassBadRequest {
		t.Fatalf("want bad_request ProviderError, got %v", err)
	}
}

func TestAnthropicTranslateMergesSameRole(t *testing.T) {
	b, _ := buildAnthropicBody(&ChatRequest{Model: "m", Messages: []ChatMessage{
		{Role: "user", Content: "a"}, {Role: "user", Content: "b"},
		{Role: "assistant", Content: "c"}, {Role: "assistant", Content: "d"},
	}}, false)
	var r anthropicRequest
	json.Unmarshal(b, &r)
	if len(r.Messages) != 2 || len(r.Messages[0].Content) != 2 || len(r.Messages[1].Content) != 2 {
		t.Fatalf("not merged: %s", b)
	}
}

func TestAnthropicResponseTextAndToolUse(t *testing.T) {
	r, err := parseAnthropicResponse(anthFixture(t, "tool_use_response.json"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Content != "Checking the weather." || r.FinishReason != "tool_calls" || len(r.ToolCalls) != 1 {
		t.Fatalf("%+v", r)
	}
	tc := r.ToolCalls[0]
	if tc.ID != "toolu_01A" || tc.Type != "function" || tc.Function.Name != "get_weather" || tc.Function.Arguments != `{"city":"Paris","unit":"c"}` {
		t.Fatalf("%+v", tc)
	}
	if r.Usage.PromptTokens != 60 || r.Usage.CompletionTokens != 30 || r.Usage.TotalTokens != 90 {
		t.Fatalf("usage %+v", r.Usage)
	}
}

func TestAnthropicStopReasonMapping(t *testing.T) {
	for in, want := range map[string]string{"end_turn": "stop", "stop_sequence": "stop", "max_tokens": "length", "tool_use": "tool_calls"} {
		if got := mapStopReason(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}
