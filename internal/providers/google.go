package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// GoogleProvider talks to Gemini through Google's OpenAI-compatible endpoint
// ({base}/v1beta/openai/chat/completions) and lists models through the native
// API ({base}/v1beta/models). The compat layer has a number of tool-calling
// quirks (missing tool-call ids, whole arguments per streamed chunk, repeated
// or missing indexes, finish_reason "stop" with tool calls, array-wrapped error
// bodies); this file normalises them to OpenAI semantics.
type GoogleProvider struct{}

func init() { Register(&GoogleProvider{}) }

func (p *GoogleProvider) Name() string { return "google" }

func (p *GoogleProvider) DefaultModels() []string {
	return []string{"gemini-2.0-flash", "gemini-1.5-flash-latest"}
}

func (p *GoogleProvider) baseURL() string {
	return envOrDef("GOOGLE_BASE_URL", "https://generativelanguage.googleapis.com")
}

func (p *GoogleProvider) openAICompatURL() string {
	base := strings.TrimRight(p.baseURL(), "/")
	return base + "/v1beta/openai"
}

// Google pricing (USD per million tokens) — from https://ai.google.dev/pricing
var googlePricing = map[string][2]float64{
	"gemini-2.0-flash":      {0.10, 0.40},
	"gemini-2.0-flash-lite": {0.075, 0.30},
	"gemini-1.5-flash":      {0.075, 0.30},
	"gemini-1.5-flash-8b":   {0.0375, 0.15},
	"gemini-1.5-pro":        {1.25, 5.00},
	"gemini-1.0-pro":        {0.50, 1.50},
}

// ---------------------------------------------------------------------------
// Tool-call ids and thought signatures
// ---------------------------------------------------------------------------

var googleCallSeq atomic.Int64

// newGoogleCallID returns a process-unique synthetic tool-call id (call_<n>).
func newGoogleCallID() string {
	return "call_" + strconv.FormatInt(googleCallSeq.Add(1), 10)
}

// Gemini 3 models return an opaque thought signature on tool calls
// (extra_content.google.thought_signature) that must be echoed back on the next
// turn. ChatMessage cannot carry it, so it is kept (bounded, in memory) keyed by
// tool-call id and re-attached when the assistant message comes back.
const googleSigCap = 2048

var googleSigs = struct {
	sync.Mutex
	m     map[string]json.RawMessage
	order []string
}{m: map[string]json.RawMessage{}}

func rememberGoogleExtra(id string, extra json.RawMessage) {
	if id == "" || len(extra) == 0 || string(extra) == "null" {
		return
	}
	s := &googleSigs
	s.Lock()
	defer s.Unlock()
	if _, ok := s.m[id]; !ok {
		s.order = append(s.order, id)
		if len(s.order) > googleSigCap {
			delete(s.m, s.order[0])
			s.order = s.order[1:]
		}
	}
	s.m[id] = append(json.RawMessage(nil), extra...)
}

func lookupGoogleExtra(id string) json.RawMessage {
	s := &googleSigs
	s.Lock()
	defer s.Unlock()
	return s.m[id]
}

// ---------------------------------------------------------------------------
// Request translation
// ---------------------------------------------------------------------------

type googleWireToolCall struct {
	Index        *int             `json:"index,omitempty"`
	ID           string           `json:"id,omitempty"`
	Type         string           `json:"type"`
	Function     ToolCallFunction `json:"function"`
	ExtraContent json.RawMessage  `json:"extra_content,omitempty"`
}

type googleWireMessage struct {
	Role       string               `json:"role"`
	Content    any                  `json:"content"` // nil -> JSON null
	Name       string               `json:"name,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
	ToolCalls  []googleWireToolCall `json:"tool_calls,omitempty"`
}

type googleWireReq struct {
	Model          string              `json:"model"`
	Messages       []googleWireMessage `json:"messages"`
	MaxTokens      int                 `json:"max_tokens,omitempty"`
	Temperature    *float64            `json:"temperature,omitempty"`
	TopP           *float64            `json:"top_p,omitempty"`
	Seed           *int                `json:"seed,omitempty"`
	User           string              `json:"user,omitempty"`
	Stream         bool                `json:"stream"`
	StreamOptions  *streamOptions      `json:"stream_options,omitempty"`
	Stop           []string            `json:"stop,omitempty"`
	Tools          []Tool              `json:"tools,omitempty"`
	ToolChoice     any                 `json:"tool_choice,omitempty"`
	ResponseFormat any                 `json:"response_format,omitempty"`
}

func googleBuildReq(req *ChatRequest, stream bool) googleWireReq {
	w := googleWireReq{
		Model:          req.Model,
		MaxTokens:      req.MaxTokens,
		Temperature:    req.Temperature,
		TopP:           req.TopP,
		Seed:           req.Seed,
		User:           req.User,
		Stream:         stream,
		Stop:           req.Stop,
		ResponseFormat: req.ResponseFormat,
	}
	if stream {
		w.StreamOptions = &streamOptions{IncludeUsage: true}
	}

	callNames := map[string]string{} // tool_call id -> function name
	for _, m := range req.Messages {
		wm := googleWireMessage{Role: m.Role, Name: m.Name, ToolCallID: m.ToolCallID}
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			if m.Content != "" {
				wm.Content = m.Content
			} // else: content null, as OpenAI clients send for tool-only turns
			for _, tc := range m.ToolCalls {
				typ := tc.Type
				if typ == "" {
					typ = "function"
				}
				fn := tc.Function
				if strings.TrimSpace(fn.Arguments) == "" {
					fn.Arguments = "{}"
				}
				if tc.ID != "" {
					callNames[tc.ID] = fn.Name
				}
				w2 := googleWireToolCall{ID: tc.ID, Type: typ, Function: fn}
				if tc.ID != "" {
					w2.ExtraContent = lookupGoogleExtra(tc.ID)
				}
				wm.ToolCalls = append(wm.ToolCalls, w2)
			}
		case m.Role == "tool":
			wm.Content = m.Content // "" stays "" (never omitted)
			if wm.Name == "" && m.ToolCallID != "" {
				wm.Name = callNames[m.ToolCallID]
			}
		default:
			wm.Content = m.Content
		}
		w.Messages = append(w.Messages, wm)
	}

	for _, t := range req.Tools {
		if t.Type == "" {
			t.Type = "function"
		}
		t.Function.Parameters = googleSanitizeSchema(t.Function.Parameters, true)
		w.Tools = append(w.Tools, t)
	}
	if len(w.Tools) > 0 {
		w.ToolChoice = googleToolChoice(req.ToolChoice)
	}
	return w
}

// googleToolChoice normalises the accepted tool_choice forms: "auto", "none",
// "required" (also "any"), and {"type":"function","function":{"name":...}}
// (also the flat {"type":"function","name":...}).
func googleToolChoice(tc any) any {
	switch v := tc.(type) {
	case nil:
		return nil
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		switch s {
		case "":
			return nil
		case "any":
			return "required"
		}
		return s
	case map[string]any:
		typ, _ := v["type"].(string)
		var name string
		if fn, ok := v["function"].(map[string]any); ok {
			name, _ = fn["name"].(string)
		}
		if name == "" {
			name, _ = v["name"].(string)
		}
		if name != "" {
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}
		}
		switch strings.ToLower(typ) {
		case "auto", "none", "required":
			return strings.ToLower(typ)
		case "any":
			return "required"
		}
		return v
	}
	return tc
}

// googleSanitizeSchema deep-copies a JSON schema, dropping keywords Gemini
// rejects ($schema, additionalProperties) and replacing an empty-object schema
// by nil (Gemini 400s on OBJECT types without properties).
func googleSanitizeSchema(s any, top bool) any {
	m, ok := s.(map[string]any)
	if !ok {
		return s
	}
	out := googleCleanSchema(m)
	if top {
		if props, _ := out["properties"].(map[string]any); len(props) == 0 {
			if t, _ := out["type"].(string); t == "object" || t == "" {
				return nil
			}
		}
	}
	return out
}

func googleCleanSchema(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch k {
		case "$schema", "additionalProperties":
			continue
		case "properties":
			if pm, ok := v.(map[string]any); ok {
				np := make(map[string]any, len(pm))
				for pk, pv := range pm {
					np[pk] = googleCleanValue(pv)
				}
				out[k] = np
				continue
			}
		}
		out[k] = googleCleanValue(v)
	}
	return out
}

func googleCleanValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return googleCleanSchema(t)
	case []any:
		na := make([]any, len(t))
		for i, e := range t {
			na[i] = googleCleanValue(e)
		}
		return na
	}
	return v
}

func (p *GoogleProvider) newRequest(ctx context.Context, apiKey string, wire googleWireReq) (*http.Request, error) {
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.openAICompatURL()+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	if wire.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	return httpReq, nil
}

// ---------------------------------------------------------------------------
// Response parsing
// ---------------------------------------------------------------------------

type googleInToolCall struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
	ExtraContent json.RawMessage `json:"extra_content"`
}

// args renders the arguments as a string; Gemini normally sends a JSON string
// but an inline object is tolerated.
func (t googleInToolCall) args() string {
	raw := bytes.TrimSpace(t.Function.Arguments)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return string(raw)
}

type googleInUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (u *googleInUsage) toUsage() *Usage {
	if u == nil {
		return nil
	}
	return &Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
}

func (p *GoogleProvider) Complete(ctx context.Context, req *ChatRequest, apiKey string) (*ChatResponse, error) {
	httpReq, err := p.newRequest(ctx, apiKey, googleBuildReq(req, false))
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, failureFromResponse(resp, p)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		if pe, cancelled := TransportError(ctx, err); !cancelled {
			return nil, pe
		}
		return nil, context.Canceled
	}
	if pe := classify200(p, resp.Header, raw); pe != nil {
		return nil, pe
	}
	var r struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message *struct {
				Content   *string            `json:"content"`
				ToolCalls []googleInToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage googleInUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, &ProviderError{Class: ErrorClassTransient, HTTP: resp.StatusCode, Code: "invalid_response", Message: "provider returned an unparseable response"}
	}
	out := &ChatResponse{
		ID:        r.ID,
		Model:     r.Model,
		Usage:     *r.Usage.toUsage(),
		RateLimit: parseRateLimitHeaders(resp.Header),
	}
	if len(r.Choices) > 0 {
		ch := r.Choices[0]
		out.FinishReason = ch.FinishReason
		if ch.Message != nil {
			if ch.Message.Content != nil {
				out.Content = *ch.Message.Content
			}
			for _, tc := range ch.Message.ToolCalls {
				id := tc.ID
				if id == "" {
					id = newGoogleCallID()
				}
				rememberGoogleExtra(id, tc.ExtraContent)
				args := tc.args()
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				out.ToolCalls = append(out.ToolCalls, ToolCall{
					ID:       id,
					Type:     "function",
					Function: ToolCallFunction{Name: tc.Function.Name, Arguments: args},
				})
			}
		}
		if len(out.ToolCalls) > 0 && (out.FinishReason == "stop" || out.FinishReason == "") {
			out.FinishReason = "tool_calls"
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Streaming
// ---------------------------------------------------------------------------

type googleStreamCall struct {
	id   string
	name string
	args strings.Builder
}

// googleStreamState normalises streamed tool-call deltas to OpenAI semantics:
// per call, a first delta with index+id+name (arguments ""), then argument
// fragments carrying only the index.
type googleStreamState struct {
	calls   []*googleStreamCall
	byIndex map[int]int // upstream index -> normalised index (most recent call)
	flushed bool
}

func (s *googleStreamState) apply(tc googleInToolCall) []ToolCall {
	var out []ToolCall
	args := tc.args()
	name := tc.Function.Name
	target := -1

	if tc.ID != "" {
		for i, c := range s.calls {
			if c.id == tc.ID {
				target = i
				break
			}
		}
	}
	// candidate by upstream index, else the latest call
	cand := -1
	if tc.Index != nil {
		if n, ok := s.byIndex[*tc.Index]; ok {
			cand = n
		}
	} else if len(s.calls) > 0 {
		cand = len(s.calls) - 1
	}
	if target < 0 && cand >= 0 {
		c := s.calls[cand]
		switch {
		case name == "":
			target = cand // pure argument fragment
		case name == c.name && tc.ID == "":
			// name repeated on every fragment: a continuation only while the
			// arguments so far are an incomplete JSON document.
			if a := c.args.String(); a != "" && !json.Valid([]byte(a)) {
				target = cand
			}
		}
	}
	if target < 0 {
		if name == "" {
			return nil // arguments with no call to attach to
		}
		id := tc.ID
		if id == "" {
			id = newGoogleCallID()
		}
		rememberGoogleExtra(id, tc.ExtraContent)
		target = len(s.calls)
		s.calls = append(s.calls, &googleStreamCall{id: id, name: name})
		if tc.Index != nil {
			s.byIndex[*tc.Index] = target
		}
		idx := target
		out = append(out, ToolCall{Index: &idx, ID: id, Type: "function", Function: ToolCallFunction{Name: name}})
	}
	if args != "" {
		s.calls[target].args.WriteString(args)
		idx := target
		out = append(out, ToolCall{Index: &idx, Function: ToolCallFunction{Arguments: args}})
	}
	return out
}

// flush emits "{}" for calls that never received arguments so that the
// accumulated arguments are valid JSON.
func (s *googleStreamState) flush() []ToolCall {
	if s.flushed {
		return nil
	}
	s.flushed = true
	var out []ToolCall
	for i, c := range s.calls {
		if c.args.Len() == 0 {
			idx := i
			c.args.WriteString("{}")
			out = append(out, ToolCall{Index: &idx, Function: ToolCallFunction{Arguments: "{}"}})
		}
	}
	return out
}

func (p *GoogleProvider) Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error) {
	httpReq, err := p.newRequest(ctx, apiKey, googleBuildReq(req, true))
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, failureFromResponse(resp, p)
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
		st := &googleStreamState{byIndex: map[int]int{}}
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
				for _, tc := range st.flush() {
					if !send(ChatChunk{ToolCalls: []ToolCall{tc}}) {
						return
					}
				}
				send(ChatChunk{Done: true, Raw: "data: [DONE]"})
				return
			}
			// Errors: {"error":…} or the array-wrapped [{"error":…}].
			if data[0] == '[' || strings.Contains(data, `"error"`) {
				if eb := parseErrBody([]byte(data)); eb.Present {
					var probe struct {
						Choices json.RawMessage `json:"choices"`
					}
					if json.Unmarshal([]byte(data), &probe) != nil || len(probe.Choices) == 0 {
						send(ChatChunk{Err: p.Classify(http.StatusOK, resp.Header, []byte(data)), Raw: line})
						return
					}
				}
			}
			var chunk struct {
				ID      string `json:"id"`
				Model   string `json:"model"`
				Choices []struct {
					Delta struct {
						Content   *string            `json:"content"`
						ToolCalls []googleInToolCall `json:"tool_calls"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
				Usage *googleInUsage `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			var outs []ChatChunk
			var finish string
			if len(chunk.Choices) > 0 {
				c0 := chunk.Choices[0]
				finish = c0.FinishReason
				if c0.Delta.Content != nil && *c0.Delta.Content != "" {
					outs = append(outs, ChatChunk{ID: chunk.ID, Model: chunk.Model, Content: *c0.Delta.Content})
				}
				for _, tc := range c0.Delta.ToolCalls {
					for _, d := range st.apply(tc) {
						outs = append(outs, ChatChunk{ID: chunk.ID, Model: chunk.Model, ToolCalls: []ToolCall{d}})
					}
				}
			}
			usage := chunk.Usage.toUsage()
			if finish != "" {
				for _, d := range st.flush() {
					outs = append(outs, ChatChunk{ID: chunk.ID, Model: chunk.Model, ToolCalls: []ToolCall{d}})
				}
				if len(st.calls) > 0 && finish == "stop" {
					finish = "tool_calls"
				}
			}
			if finish != "" || usage != nil || len(outs) == 0 {
				outs = append(outs, ChatChunk{ID: chunk.ID, Model: chunk.Model, FinishReason: finish, Usage: usage})
			}
			outs[0].Raw = line
			for _, o := range outs {
				if !send(o) {
					return
				}
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

func (p *GoogleProvider) ProbeModel(ctx context.Context, apiKey, model string) (*HealthResult, error) {
	return doOpenAIProbeModel(ctx, p.openAICompatURL(), apiKey, model, nil, p)
}

// ---------------------------------------------------------------------------
// Native model listing + health
// ---------------------------------------------------------------------------

const googleMaxListPages = 50

type googleListedModel struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	InputTokenLimit            int      `json:"inputTokenLimit"`
	OutputTokenLimit           int      `json:"outputTokenLimit"`
}

func (p *GoogleProvider) listPage(ctx context.Context, apiKey string, pageSize int, token string) ([]googleListedModel, string, error) {
	q := url.Values{}
	q.Set("key", apiKey)
	q.Set("pageSize", strconv.Itoa(pageSize))
	if token != "" {
		q.Set("pageToken", token)
	}
	u := strings.TrimRight(p.baseURL(), "/") + "/v1beta/models?" + q.Encode()
	b, err := doGetJSON(ctx, u, nil, p)
	if err != nil {
		return nil, "", err
	}
	var result struct {
		Models        []googleListedModel `json:"models"`
		NextPageToken string              `json:"nextPageToken"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, "", &ProviderError{Class: ErrorClassTransient, Code: "invalid_response", Message: "unparseable model list"}
	}
	return result.Models, result.NextPageToken, nil
}

// ListModels returns every model of the native listing (all pages). ID has the
// "models/" prefix stripped; Extra["methods"] carries supportedGenerationMethods
// ([]string) so the catalog can decide the model kind.
func (p *GoogleProvider) ListModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	var models []ModelInfo
	token := ""
	seen := map[string]bool{}
	for page := 0; page < googleMaxListPages; page++ {
		items, next, err := p.listPage(ctx, apiKey, 1000, token)
		if err != nil {
			return nil, err
		}
		for _, m := range items {
			id := strings.TrimPrefix(m.Name, "models/")
			if id == "" {
				continue
			}
			methods := append([]string{}, m.SupportedGenerationMethods...)
			streaming := false
			for _, mm := range methods {
				if mm == "streamGenerateContent" {
					streaming = true
				}
			}
			paramB, arch := LookupModelMeta(id)
			info := ModelInfo{
				ID:                id,
				DisplayName:       m.DisplayName,
				ContextWindow:     m.InputTokenLimit,
				MaxOutputTokens:   m.OutputTokenLimit,
				SupportsStreaming: streaming,
				ParameterCountB:   paramB,
				Architecture:      arch,
				Extra:             map[string]any{"full_name": m.Name, "methods": methods},
			}
			if pr, ok := googlePricing[id]; ok {
				info.InputCostPerMTok = pr[0]
				info.OutputCostPerMTok = pr[1]
			}
			models = append(models, info)
		}
		if next == "" || seen[next] {
			break
		}
		seen[next] = true
		token = next
	}
	return models, nil
}

// HealthCheck probes the key with a one-model native listing request.
func (p *GoogleProvider) HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error) {
	start := time.Now()
	_, _, err := p.listPage(ctx, apiKey, 1, "")
	latency := time.Since(start).Milliseconds()
	if err != nil {
		if pe, ok := err.(*ProviderError); ok {
			return &HealthResult{OK: false, LatencyMs: latency, Error: string(pe.Class)}, nil
		}
		return &HealthResult{OK: false, LatencyMs: latency, Error: "request_failed"}, nil
	}
	return &HealthResult{OK: true, LatencyMs: latency}, nil
}

// ---------------------------------------------------------------------------
// Error classification
// ---------------------------------------------------------------------------

type googleErr struct {
	present    bool
	code       int
	status     string
	message    string
	reason     string
	retryDelay time.Duration
	hasRetry   bool
}

// parseGoogleErr understands {"error":{code,message,status,details[]}} and the
// array-wrapped [{"error":…}] form.
func parseGoogleErr(body []byte) googleErr {
	b := trimSpace(body)
	if len(b) == 0 {
		return googleErr{}
	}
	if b[0] == '[' {
		var arr []json.RawMessage
		if json.Unmarshal(b, &arr) != nil || len(arr) == 0 {
			return googleErr{}
		}
		b = arr[0]
	}
	var top struct {
		Error struct {
			Code    json.RawMessage   `json:"code"`
			Message string            `json:"message"`
			Status  string            `json:"status"`
			Details []json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &top) != nil {
		return googleErr{}
	}
	e := top.Error
	if e.Message == "" && e.Status == "" && len(e.Code) == 0 {
		return googleErr{}
	}
	out := googleErr{present: true, status: e.Status, message: e.Message}
	var n float64
	if json.Unmarshal(e.Code, &n) == nil {
		out.code = int(n)
	} else {
		var s string
		if json.Unmarshal(e.Code, &s) == nil {
			out.code, _ = strconv.Atoi(s)
		}
	}
	for _, d := range e.Details {
		var det struct {
			Type       string `json:"@type"`
			Reason     string `json:"reason"`
			RetryDelay string `json:"retryDelay"`
		}
		if json.Unmarshal(d, &det) != nil {
			continue
		}
		if det.Reason != "" && out.reason == "" {
			out.reason = det.Reason
		}
		if det.RetryDelay != "" {
			if dur, err := time.ParseDuration(strings.TrimSpace(det.RetryDelay)); err == nil && dur >= 0 {
				out.retryDelay, out.hasRetry = dur, true
			}
		}
	}
	return out
}

// Classify applies Gemini-specific rules (error-classification.md) on top of the
// generic ones. Message is kept in memory only; Code is the Gemini status/reason.
func (p *GoogleProvider) Classify(status int, h http.Header, body []byte) *ProviderError {
	ge := parseGoogleErr(body)
	eff := status
	if status < 400 && ge.code >= 400 {
		eff = ge.code // error payload inside a 2xx body / SSE event
	}
	pe := Classify(eff, h, body)
	pe.HTTP = eff
	st := strings.ToUpper(ge.status)
	msg := strings.ToLower(ge.message)
	if ge.status != "" {
		pe.Code = truncate(ge.status, maxCodeLen)
	} else if ge.reason != "" && pe.Code == "" {
		pe.Code = truncate(ge.reason, maxCodeLen)
	}

	switch {
	case ge.reason == "API_KEY_INVALID" || strings.Contains(msg, "api key not valid") || strings.Contains(msg, "api_key_invalid"):
		pe.Class = ErrorClassAuth
		if ge.reason != "" {
			pe.Code = ge.reason
		} else {
			pe.Code = "API_KEY_INVALID"
		}
	case eff == http.StatusUnauthorized, eff == http.StatusForbidden, st == "PERMISSION_DENIED", st == "UNAUTHENTICATED":
		pe.Class = ErrorClassAuth
	case eff == http.StatusBadRequest && st == "FAILED_PRECONDITION" && strings.Contains(msg, "billing"):
		pe.Class = ErrorClassInsufficientCredit
	case eff == http.StatusTooManyRequests, st == "RESOURCE_EXHAUSTED":
		// A depleted prepaid balance needs a human; every other quota message
		// ("check your plan and billing details" is boilerplate) is a throttle.
		if strings.Contains(msg, "prepayment") || strings.Contains(msg, "credits are depleted") {
			pe.Class = ErrorClassInsufficientCredit
		} else {
			pe.Class = ErrorClassRateLimit
			if ge.hasRetry && h.Get("Retry-After") == "" {
				pe.RetryAfter = ClampCooldown(ceilSecond(ge.retryDelay))
			} else if pe.RetryAfter == 0 {
				pe.RetryAfter = rateLimitCooldown(h, nowFunc())
			}
		}
	case eff == http.StatusNotFound, st == "NOT_FOUND":
		pe.Class = ErrorClassModelUnavailable
	case eff >= 500, st == "INTERNAL", st == "UNAVAILABLE", st == "DEADLINE_EXCEEDED":
		pe.Class = ErrorClassTransient
	case eff == http.StatusBadRequest, st == "INVALID_ARGUMENT":
		pe.Class = ErrorClassBadRequest
	}
	if pe.Class != ErrorClassRateLimit {
		pe.RetryAfter = 0
	}
	if pe.Message == "" {
		pe.Message = fmt.Sprintf("provider error (http %d)", eff)
	}
	return pe
}
