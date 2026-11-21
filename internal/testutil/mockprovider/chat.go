package mockprovider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// behaviour decodes a (normalised) key into its behaviour name and the
// Retry-After seconds of mock-429-<secs>.
func behaviour(key string) (name string, secs int) {
	secs = 5
	switch {
	case strings.HasPrefix(key, "mock-ok"):
		return "ok", 0
	case strings.HasPrefix(key, "mock-midstream"):
		return "midstream", 0
	case strings.HasPrefix(key, "mock-429"):
		if rest := strings.TrimPrefix(key, "mock-429-"); rest != key {
			digits := ""
			for _, c := range rest {
				if c < '0' || c > '9' {
					break
				}
				digits += string(c)
			}
			if n, err := strconv.Atoi(digits); err == nil {
				secs = n
			}
		}
		return "429", secs
	case strings.HasPrefix(key, "mock-402"):
		return "402", 0
	case strings.HasPrefix(key, "mock-credits"):
		return "credits", 0
	case strings.HasPrefix(key, "mock-529"):
		return "529", 0
	case strings.HasPrefix(key, "mock-500"):
		return "500", 0
	case strings.HasPrefix(key, "mock-503"):
		return "503", 0
	case strings.HasPrefix(key, "mock-404"):
		return "404", 0
	case strings.HasPrefix(key, "mock-400"):
		return "400", 0
	}
	return "401", 0
}

func (m *Mock) chat(w http.ResponseWriter, r *http.Request) {
	key := bearerKey(r)
	raw := readBody(r)
	var req oaRequest
	_ = json.Unmarshal(raw, &req)
	m.record(Call{Key: key, Path: "/chat/completions", Model: req.Model, Stream: req.Stream, Body: raw})
	name, secs := behaviour(m.behaviourKey(key))

	switch name {
	case "ok", "midstream":
		scenario, round, err := analyseOpenAI(&req)
		if err != nil {
			m.writeError(w, http.StatusBadRequest, "invalid_request", "invalid_request_error", 400, "invalid tool conversation: "+err.Error())
			return
		}
		if rounds := ScenarioRounds(scenario); len(req.Tools) > 0 && rounds != nil && round < len(rounds) {
			m.toolReply(w, req.Model, req.Stream, round, rounds[round])
			return
		}
		m.ok(w, req.Model, req.Stream, name == "midstream", replyText(scenario, len(req.Tools)))
	case "429":
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		m.writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "rate_limit_error", 429, "")
	case "402", "credits":
		m.writeError(w, http.StatusPaymentRequired, "insufficient_credits", "insufficient_credits", 402, "")
	case "500":
		m.writeError(w, http.StatusInternalServerError, "internal_error", "server_error", 500, "")
	case "503", "529":
		m.writeError(w, http.StatusServiceUnavailable, "service_unavailable", "server_error", 503, "")
	case "404":
		m.writeError(w, http.StatusNotFound, "model_not_found", "invalid_request_error", 404, "")
	case "400":
		m.writeError(w, http.StatusBadRequest, "invalid_request", "invalid_request_error", 400, "")
	default:
		m.writeError(w, http.StatusUnauthorized, "invalid_api_key", "authentication_error", 401, "")
	}
}

// writeError writes an error body in the flavour's style. OpenRouter puts the
// numeric status in error.code; Gemini wraps Google-style errors in an array;
// Groq/OpenAI/Mesh use a string code.
func (m *Mock) writeError(w http.ResponseWriter, status int, code, typ string, numeric int, override string) {
	msg := fmt.Sprintf("%s mock provider error (%s)", SentinelBody, code)
	if override != "" {
		msg = fmt.Sprintf("%s mock provider error (%s): %s", SentinelBody, code, override)
	}
	switch {
	case m.flavour == Gemini:
		m.geminiError(w, status, msg)
		return
	case m.flavour == OpenRouter && numeric != 0:
		writeJSON(w, status, map[string]any{"error": map[string]any{"code": numeric, "message": msg}})
	default:
		writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": code}})
	}
}

type sseWriter struct {
	w  http.ResponseWriter
	fl http.Flusher
}

func newSSE(w http.ResponseWriter) *sseWriter {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	return &sseWriter{w, fl}
}

func (s *sseWriter) raw(text string) {
	fmt.Fprint(s.w, text)
	if s.fl != nil {
		s.fl.Flush()
	}
}

func (s *sseWriter) data(v any) {
	b, _ := json.Marshal(v)
	s.raw("data: " + string(b) + "\n\n")
}

// chunk emits one chat.completion.chunk. delta nil = a usage-only chunk.
func (s *sseWriter) chunk(model string, delta map[string]any, finish any, usage map[string]int) {
	c := map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model}
	if delta != nil {
		c["choices"] = []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}}
	} else {
		c["choices"] = []map[string]any{}
	}
	if usage != nil {
		c["usage"] = usage
	}
	s.data(c)
}

var mockUsage = map[string]int{"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19}

// replyText is the final plain answer: the scripted ToolFinalText once a tool
// scenario has run its rounds, ReplyText otherwise.
func replyText(scenario string, tools int) string {
	if scenario != "" && tools > 0 {
		return ToolFinalText
	}
	return ReplyText
}

func replyParts3(text string) []string {
	if text == ReplyText {
		return replyParts
	}
	return splitParts(text, 3)
}

func (m *Mock) ok(w http.ResponseWriter, model string, stream, dropMidstream bool, text string) {
	if model == "" {
		model = "mock-model"
	}
	if !stream {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion", "created": time.Now().Unix(), "model": model,
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": text}}},
			"usage": mockUsage,
		})
		return
	}
	s := newSSE(w)
	for i, part := range replyParts3(text) {
		d := map[string]any{"content": part}
		if i == 0 {
			d["role"] = "assistant"
		}
		s.chunk(model, d, nil, nil)
		if dropMidstream && i == 1 {
			time.Sleep(20 * time.Millisecond)
			panic(http.ErrAbortHandler) // drop the connection without a terminating chunk
		}
	}
	s.chunk(model, map[string]any{}, "stop", nil)
	s.chunk(model, nil, nil, mockUsage)
	s.raw("data: [DONE]\n\n")
}

// toolReply answers one scripted tool round (non-stream or SSE) in the OpenAI
// wire. Gemini quirks: no tool-call ids, finish_reason "stop", and in a stream
// one whole-arguments chunk per call, every call carrying index 0.
func (m *Mock) toolReply(w http.ResponseWriter, model string, stream bool, round int, calls []ScriptedCall) {
	if model == "" {
		model = "mock-model"
	}
	gem := m.flavour == Gemini
	finish := "tool_calls"
	if gem {
		finish = "stop"
	}
	id := func(i int) string {
		if gem {
			return ""
		}
		return fmt.Sprintf("call_%s_%d_%d", m.flavour, round, i)
	}
	if !stream {
		tcs := make([]map[string]any, len(calls))
		for i, c := range calls {
			tcs[i] = map[string]any{"id": id(i), "type": "function", "function": map[string]any{"name": c.Name, "arguments": c.Args}}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion", "created": time.Now().Unix(), "model": model,
			"choices": []map[string]any{{"index": 0, "finish_reason": finish,
				"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": tcs}}},
			"usage": mockUsage,
		})
		return
	}
	s := newSSE(w)
	first := true
	withRole := func(d map[string]any) map[string]any {
		if first {
			d["role"] = "assistant"
			first = false
		}
		return d
	}
	for i, c := range calls {
		if gem {
			tc := map[string]any{"index": 0, "type": "function", "function": map[string]any{"name": c.Name, "arguments": c.Args}}
			var f any
			if i == len(calls)-1 {
				f = finish
			}
			s.chunk(model, withRole(map[string]any{"tool_calls": []any{tc}}), f, nil)
			continue
		}
		s.chunk(model, withRole(map[string]any{"tool_calls": []any{map[string]any{
			"index": i, "id": id(i), "type": "function", "function": map[string]any{"name": c.Name, "arguments": ""}}}}), nil, nil)
		for _, frag := range splitParts(c.Args, 3) {
			s.chunk(model, map[string]any{"tool_calls": []any{map[string]any{
				"index": i, "function": map[string]any{"arguments": frag}}}}, nil, nil)
		}
	}
	if !gem {
		s.chunk(model, map[string]any{}, finish, nil)
	}
	s.chunk(model, nil, nil, mockUsage)
	s.raw("data: [DONE]\n\n")
}
