// Package agentloop is an OpenAI-SDK-style tool-calling agent loop used by the
// integration and smoke tests. It sends tools=[get_weather] with
// tool_choice "auto", asserts the tool-call contract (finish_reason
// "tool_calls", non-empty id, type "function", arguments that parse as JSON),
// appends the assistant turn (content null, tool_calls intact) and role:tool
// results, resends, and finally asserts a "stop" text answer. The streaming
// variant accumulates tool_calls deltas by index (id and name on the first
// delta, arguments concatenated) exactly as the OpenAI SDK does.
package agentloop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Config selects the target and the conversation.
type Config struct {
	BaseURL string // gateway root, e.g. http://127.0.0.1:8080 (the loop appends /v1/chat/completions)
	Token   string
	Model   string
	// Marker is appended to the user prompt (mockprovider.ScenarioMarker(..)); "" for real providers.
	Marker  string
	Prompt  string // default: a weather question for Paris
	Stream  bool
	Headers map[string]string
	// MinRounds is the minimum number of tool rounds that must occur (default 1).
	MinRounds int
	// MaxRounds bounds the loop (default 6).
	MaxRounds int
}

// Call is one assistant tool call as the loop saw it.
type Call struct {
	ID, Name string
	Args     map[string]any
}

// Result is the outcome of a full loop.
type Result struct {
	Rounds    [][]Call // tool calls per assistant turn
	FinalText string
	Model     string // response model of the last request
	Header    http.Header
}

var weatherTool = map[string]any{"type": "function", "function": map[string]any{
	"name": "get_weather", "description": "Get the current weather for a city",
	"parameters": map[string]any{"type": "object",
		"properties": map[string]any{"city": map[string]any{"type": "string", "description": "City name"}},
		"required":   []string{"city"}},
}}

var client = &http.Client{Timeout: 60 * time.Second}

type rawCall struct {
	ID, Type, Name, Args string
	firstOK              bool
}

// Run executes the loop and fails t on any contract violation.
func Run(t testing.TB, cfg Config) *Result {
	t.Helper()
	if cfg.Prompt == "" {
		cfg.Prompt = "What is the weather in Paris right now? Use the get_weather tool."
	}
	if cfg.Marker != "" {
		cfg.Prompt += " " + cfg.Marker
	}
	if cfg.MaxRounds == 0 {
		cfg.MaxRounds = 6
	}
	if cfg.MinRounds == 0 {
		cfg.MinRounds = 1
	}
	msgs := []map[string]any{{"role": "user", "content": cfg.Prompt}}
	res := &Result{}
	for turn := 0; turn <= cfg.MaxRounds; turn++ {
		finish, content, calls, model, hdr := send(t, cfg, msgs)
		res.Model, res.Header = model, hdr
		if finish == "tool_calls" {
			if turn == cfg.MaxRounds {
				t.Fatalf("agent loop did not terminate after %d tool rounds", cfg.MaxRounds)
			}
			if len(calls) == 0 {
				t.Fatalf("finish_reason tool_calls without tool_calls")
			}
			var round []Call
			var wire []map[string]any
			for i, c := range calls {
				if c.ID == "" || c.Name == "" || c.Type != "function" {
					t.Fatalf("tool_calls[%d]: want non-empty id/name and type function, got %+v", i, c)
				}
				var args map[string]any
				if err := json.Unmarshal([]byte(c.Args), &args); err != nil {
					t.Fatalf("tool_calls[%d].function.arguments is not a JSON object: %q (%v)", i, c.Args, err)
				}
				round = append(round, Call{ID: c.ID, Name: c.Name, Args: args})
				wire = append(wire, map[string]any{"id": c.ID, "type": "function",
					"function": map[string]any{"name": c.Name, "arguments": c.Args}})
			}
			res.Rounds = append(res.Rounds, round)
			msgs = append(msgs, map[string]any{"role": "assistant", "content": nil, "tool_calls": wire})
			for _, c := range round {
				msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": c.ID,
					"content": `{"city":"` + str(c.Args["city"]) + `","temp_c":18,"condition":"sunny"}`})
			}
			continue
		}
		if finish != "stop" {
			t.Fatalf("final finish_reason = %q, want stop (content %q)", finish, content)
		}
		if strings.TrimSpace(content) == "" {
			t.Fatalf("final stop answer is empty")
		}
		if len(res.Rounds) < cfg.MinRounds {
			t.Fatalf("only %d tool rounds, want >= %d", len(res.Rounds), cfg.MinRounds)
		}
		res.FinalText = content
		return res
	}
	t.Fatalf("unreachable")
	return nil
}

func str(v any) string { s, _ := v.(string); return s }

func send(t testing.TB, cfg Config, msgs []map[string]any) (finish, content string, calls []rawCall, model string, hdr http.Header) {
	t.Helper()
	body := map[string]any{"model": cfg.Model, "messages": msgs, "tools": []any{weatherTool}, "tool_choice": "auto"}
	if cfg.Stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", cfg.BaseURL+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("chat request: %v", err)
	}
	defer resp.Body.Close()
	hdr = resp.Header
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("chat request: HTTP %d %s", resp.StatusCode, b)
	}
	if !cfg.Stream {
		var out struct {
			Model   string `json:"model"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Content   *string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct{ Name, Arguments string }
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Choices) != 1 {
			t.Fatalf("decode chat response: %v (choices %d)", err, len(out.Choices))
		}
		ch := out.Choices[0]
		if ch.Message.Content != nil {
			content = *ch.Message.Content
		}
		for _, c := range ch.Message.ToolCalls {
			calls = append(calls, rawCall{ID: c.ID, Type: c.Type, Name: c.Function.Name, Args: c.Function.Arguments})
		}
		return ch.FinishReason, content, calls, out.Model, hdr
	}

	// Streaming: accumulate like the OpenAI SDK.
	type acc struct{ rawCall }
	byIndex := map[int]*acc{}
	var order []int
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	done := false
	var sb strings.Builder
	for sc.Scan() {
		data, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			done = true
			break
		}
		var ck struct {
			Model   string `json:"model"`
			Error   any    `json:"error"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct{ Name, Arguments string }
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &ck); err != nil {
			t.Fatalf("bad SSE chunk %q: %v", data, err)
		}
		if ck.Error != nil {
			t.Fatalf("error event in stream: %s", data)
		}
		if ck.Model != "" {
			model = ck.Model
		}
		for _, c := range ck.Choices {
			sb.WriteString(c.Delta.Content)
			if c.FinishReason != "" {
				finish = c.FinishReason
			}
			for _, tc := range c.Delta.ToolCalls {
				if tc.Index == nil {
					t.Fatalf("tool_calls delta without index: %s", data)
				}
				a := byIndex[*tc.Index]
				if a == nil {
					a = &acc{}
					byIndex[*tc.Index] = a
					order = append(order, *tc.Index)
					if tc.ID == "" || tc.Function.Name == "" {
						t.Fatalf("first delta of tool call %d must carry id and name: %s", *tc.Index, data)
					}
				}
				if tc.ID != "" {
					a.ID = tc.ID
				}
				if tc.Type != "" {
					a.Type = tc.Type
				}
				if tc.Function.Name != "" {
					a.Name = tc.Function.Name
				}
				a.Args += tc.Function.Arguments
			}
		}
	}
	if !done {
		t.Fatalf("stream ended without [DONE] (scan err %v)", sc.Err())
	}
	for _, i := range order {
		calls = append(calls, byIndex[i].rawCall)
	}
	return finish, sb.String(), calls, model, hdr
}
