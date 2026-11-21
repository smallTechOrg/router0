package mockprovider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Scripted tool-calling conversations. A request opts into a scenario by
// carrying ScenarioMarker(name) in a user message and a non-empty `tools`
// list. The scenario is a list of ROUNDS; round N is answered with that round's
// tool calls while the conversation holds N assistant tool-call turns after
// the last user message, and with ToolFinalText once the rounds are used up.
//
//	single    [[get_weather Paris]]
//	parallel  [[get_weather Paris, get_weather Tokyo]]
//	chain     [[get_weather Paris], [get_weather Berlin]]
//
// Every request that carries tool turns is validated for well-formedness
// (ids echoed and matched, argument JSON, block structure); a malformed one
// gets a 400, which is how the integration tests prove the router's
// translation is correct.

// ToolFinalText is the final assistant answer after the last tool round.
const ToolFinalText = "It is sunny and 18C. Weather lookup complete."

// ScriptedCall is one tool call of a scenario round.
type ScriptedCall struct {
	Name string
	Args string // JSON object text
}

// ScenarioMarker is the text to put in a user message to select a scenario.
func ScenarioMarker(name string) string { return "[scenario:" + name + "]" }

// ScenarioRounds returns the scripted rounds of a scenario (nil if unknown).
func ScenarioRounds(name string) [][]ScriptedCall {
	switch name {
	case "single":
		return [][]ScriptedCall{{{"get_weather", `{"city":"Paris"}`}}}
	case "parallel":
		return [][]ScriptedCall{{{"get_weather", `{"city":"Paris"}`}, {"get_weather", `{"city":"Tokyo"}`}}}
	case "chain":
		return [][]ScriptedCall{{{"get_weather", `{"city":"Paris"}`}}, {{"get_weather", `{"city":"Berlin"}`}}}
	}
	return nil
}

func scenarioIn(text string) string {
	for _, n := range []string{"single", "parallel", "chain"} {
		if strings.Contains(text, ScenarioMarker(n)) {
			return n
		}
	}
	return ""
}

// splitParts cuts s into n roughly equal rune chunks (empty chunks dropped).
func splitParts(s string, n int) []string {
	r := []rune(s)
	var out []string
	step := (len(r) + n - 1) / n
	if step == 0 {
		return nil
	}
	for i := 0; i < len(r); i += step {
		e := i + step
		if e > len(r) {
			e = len(r)
		}
		out = append(out, string(r[i:e]))
	}
	return out
}

// ---- OpenAI wire ----

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type oaMsg struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []oaToolCall    `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name       string          `json:"name"`
		Parameters json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaRequest struct {
	Model    string   `json:"model"`
	Stream   bool     `json:"stream"`
	Messages []oaMsg  `json:"messages"`
	Tools    []oaTool `json:"tools"`
	MaxTok   int      `json:"max_tokens"`
}

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}

// analyseOpenAI validates the conversation and returns (scenario, round).
func analyseOpenAI(req *oaRequest) (string, int, error) {
	for i, t := range req.Tools {
		if t.Type != "function" || t.Function.Name == "" {
			return "", 0, fmt.Errorf("tools[%d]: want type function with a name", i)
		}
		if len(t.Function.Parameters) > 0 && string(t.Function.Parameters) != "null" {
			var p map[string]any
			if json.Unmarshal(t.Function.Parameters, &p) != nil {
				return "", 0, fmt.Errorf("tools[%d]: parameters must be an object", i)
			}
		}
	}
	msgs := req.Messages
	lastUser := -1
	scenario := ""
	for i, m := range msgs {
		if m.Role == "user" {
			lastUser = i
			if s := scenarioIn(contentText(m.Content)); s != "" {
				scenario = s
			}
		}
	}
	round := 0
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case "tool":
			return "", 0, fmt.Errorf("messages[%d]: tool message without a preceding assistant tool_calls turn", i)
		case "assistant":
			if len(m.ToolCalls) == 0 {
				continue
			}
			if len(m.Content) > 0 {
				var s *string
				if json.Unmarshal(m.Content, &s) != nil {
					return "", 0, fmt.Errorf("messages[%d]: assistant content must be a string or null", i)
				}
			}
			ids := map[string]bool{}
			for j, tc := range m.ToolCalls {
				if tc.ID == "" || ids[tc.ID] {
					return "", 0, fmt.Errorf("messages[%d].tool_calls[%d]: id must be non-empty and unique", i, j)
				}
				ids[tc.ID] = true
				if tc.Type != "function" || tc.Function.Name == "" {
					return "", 0, fmt.Errorf("messages[%d].tool_calls[%d]: want type function with a name", i, j)
				}
				var argStr string
				var obj map[string]any
				if json.Unmarshal(tc.Function.Arguments, &argStr) != nil || json.Unmarshal([]byte(argStr), &obj) != nil {
					return "", 0, fmt.Errorf("messages[%d].tool_calls[%d]: arguments must be a JSON-object string", i, j)
				}
			}
			got := map[string]bool{}
			k := i + 1
			for ; k < len(msgs) && msgs[k].Role == "tool"; k++ {
				id := msgs[k].ToolCallID
				if !ids[id] || got[id] {
					return "", 0, fmt.Errorf("messages[%d]: tool_call_id %q does not match an unanswered tool call", k, id)
				}
				if len(msgs[k].Content) == 0 || string(msgs[k].Content) == "null" {
					return "", 0, fmt.Errorf("messages[%d]: tool message has no content", k)
				}
				got[id] = true
			}
			if len(got) != len(ids) {
				return "", 0, fmt.Errorf("messages[%d]: assistant tool_calls not all answered by tool messages", i)
			}
			if i > lastUser {
				round++
			}
			i = k - 1
		}
	}
	return scenario, round, nil
}

// ---- Anthropic wire ----

type anBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

type anMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anTool struct {
	Name        string `json:"name"`
	InputSchema struct {
		Type string `json:"type"`
	} `json:"input_schema"`
}

type anRequest struct {
	Model      string          `json:"model"`
	MaxTokens  int             `json:"max_tokens"`
	Stream     bool            `json:"stream"`
	System     json.RawMessage `json:"system"`
	Messages   []anMsg         `json:"messages"`
	Tools      []anTool        `json:"tools"`
	ToolChoice json.RawMessage `json:"tool_choice"`
}

func anBlocks(raw json.RawMessage) ([]anBlock, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []anBlock{{Type: "text", Text: s}}, nil
	}
	var bs []anBlock
	if err := json.Unmarshal(raw, &bs); err != nil {
		return nil, fmt.Errorf("content must be a string or an array of blocks")
	}
	return bs, nil
}

// analyseAnthropic validates the Messages request and returns (scenario, round).
func analyseAnthropic(req *anRequest) (string, int, error) {
	if req.Model == "" {
		return "", 0, fmt.Errorf("model: field required")
	}
	if req.MaxTokens <= 0 {
		return "", 0, fmt.Errorf("max_tokens: field required")
	}
	if len(req.Messages) == 0 || req.Messages[0].Role != "user" {
		return "", 0, fmt.Errorf("messages: first message must have role user")
	}
	for i, t := range req.Tools {
		if t.Name == "" || t.InputSchema.Type != "object" {
			return "", 0, fmt.Errorf("tools[%d]: want name and input_schema of type object", i)
		}
	}
	type parsed struct {
		role   string
		blocks []anBlock
	}
	ms := make([]parsed, len(req.Messages))
	lastUser := -1
	scenario := ""
	for i, m := range req.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			return "", 0, fmt.Errorf("messages[%d]: role must be user or assistant", i)
		}
		bs, err := anBlocks(m.Content)
		if err != nil {
			return "", 0, fmt.Errorf("messages[%d]: %v", i, err)
		}
		ms[i] = parsed{m.Role, bs}
		if m.Role == "user" {
			hasText := false
			for _, b := range bs {
				if b.Type == "text" {
					hasText = true
					if s := scenarioIn(b.Text); s != "" {
						scenario = s
					}
				}
			}
			if hasText {
				lastUser = i
			}
		}
	}
	round := 0
	for i, m := range ms {
		switch m.role {
		case "user":
			for _, b := range m.blocks {
				if b.Type == "tool_result" && (i == 0 || !hasToolUse(ms[i-1].blocks)) {
					return "", 0, fmt.Errorf("messages[%d]: tool_result without a tool_use in the previous assistant message", i)
				}
			}
		case "assistant":
			ids := map[string]bool{}
			for j, b := range m.blocks {
				if b.Type != "tool_use" {
					continue
				}
				var obj map[string]any
				if b.ID == "" || ids[b.ID] || b.Name == "" || json.Unmarshal(b.Input, &obj) != nil {
					return "", 0, fmt.Errorf("messages[%d].content[%d]: tool_use needs unique id, name and object input", i, j)
				}
				ids[b.ID] = true
			}
			if len(ids) == 0 {
				continue
			}
			if i+1 >= len(ms) || ms[i+1].role != "user" {
				return "", 0, fmt.Errorf("messages[%d]: tool_use must be followed by a user message with tool_result blocks", i)
			}
			got := map[string]bool{}
			seenOther := false
			for _, b := range ms[i+1].blocks {
				if b.Type != "tool_result" {
					seenOther = true
					continue
				}
				if seenOther {
					return "", 0, fmt.Errorf("messages[%d]: tool_result blocks must come first", i+1)
				}
				if !ids[b.ToolUseID] || got[b.ToolUseID] {
					return "", 0, fmt.Errorf("messages[%d]: tool_use_id %q does not match an unanswered tool_use", i+1, b.ToolUseID)
				}
				got[b.ToolUseID] = true
			}
			if len(got) != len(ids) {
				return "", 0, fmt.Errorf("messages[%d]: every tool_use needs a tool_result in the next user message", i)
			}
			if i > lastUser {
				round++
			}
		}
	}
	return scenario, round, nil
}

func hasToolUse(bs []anBlock) bool {
	for _, b := range bs {
		if b.Type == "tool_use" {
			return true
		}
	}
	return false
}
