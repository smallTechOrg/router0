package mockprovider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Anthropic flavour: native Messages API (POST /v1/messages, GET /v1/models).
// Behaviours by x-api-key: mock-ok*, mock-429-<secs>*, mock-credits* (400
// "credit balance is too low"), mock-402* (billing_error), mock-529*
// (overloaded_error), mock-500*, mock-404*, mock-400*, mock-midstream*, mock-401*.

func anthropicKey(r *http.Request) string { return strings.TrimSpace(r.Header.Get("x-api-key")) }

func (m *Mock) anthropicError(w http.ResponseWriter, status int, typ, msg string) {
	writeJSON(w, status, map[string]any{"type": "error", "error": map[string]any{
		"type": typ, "message": fmt.Sprintf("%s %s", SentinelBody, msg)}})
}

func (m *Mock) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	key := anthropicKey(r)
	raw := readBody(r)
	var req anRequest
	_ = json.Unmarshal(raw, &req)
	m.record(Call{Key: key, Path: "/chat/completions", Model: req.Model, Stream: req.Stream, Body: raw})
	if r.Header.Get("anthropic-version") == "" {
		m.anthropicError(w, 400, "invalid_request_error", "anthropic-version header is required")
		return
	}
	name, secs := behaviour(m.behaviourKey(key))
	switch name {
	case "ok", "midstream":
		scenario, round, err := analyseAnthropic(&req)
		if err != nil {
			m.anthropicError(w, 400, "invalid_request_error", "invalid messages request: "+err.Error())
			return
		}
		if rounds := ScenarioRounds(scenario); len(req.Tools) > 0 && rounds != nil && round < len(rounds) {
			m.anthropicReply(w, req.Model, req.Stream, false, round, rounds[round], "")
			return
		}
		m.anthropicReply(w, req.Model, req.Stream, name == "midstream", 0, nil, replyText(scenario, len(req.Tools)))
	case "429":
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		m.anthropicError(w, 429, "rate_limit_error", "rate limit exceeded")
	case "credits":
		m.anthropicError(w, 400, "invalid_request_error", "Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits.")
	case "402":
		m.anthropicError(w, 402, "billing_error", "billing issue")
	case "529":
		m.anthropicError(w, 529, "overloaded_error", "Overloaded")
	case "500", "503":
		m.anthropicError(w, 500, "api_error", "internal error")
	case "404":
		m.anthropicError(w, 404, "not_found_error", "model: not found")
	case "400":
		m.anthropicError(w, 400, "invalid_request_error", "bad request")
	default:
		m.anthropicError(w, 401, "authentication_error", "invalid x-api-key")
	}
}

func (m *Mock) anthropicReply(w http.ResponseWriter, model string, stream, drop bool, round int, calls []ScriptedCall, text string) {
	if model == "" {
		model = "mock-model"
	}
	toolID := func(i int) string { return fmt.Sprintf("toolu_%d_%d", round, i) }
	stop := "end_turn"
	if calls != nil {
		stop = "tool_use"
	}
	if !stream {
		var content []map[string]any
		if calls == nil {
			content = []map[string]any{{"type": "text", "text": text}}
		}
		for i, c := range calls {
			var in any
			_ = json.Unmarshal([]byte(c.Args), &in)
			content = append(content, map[string]any{"type": "tool_use", "id": toolID(i), "name": c.Name, "input": in})
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "msg_mock", "type": "message", "role": "assistant", "model": model,
			"content": content, "stop_reason": stop, "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": 12, "output_tokens": 7}})
		return
	}
	s := newSSE(w)
	ev := func(name string, v map[string]any) {
		v["type"] = name
		b, _ := json.Marshal(v)
		s.raw("event: " + name + "\ndata: " + string(b) + "\n\n")
	}
	ev("message_start", map[string]any{"message": map[string]any{"id": "msg_mock", "type": "message", "role": "assistant",
		"model": model, "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 12, "output_tokens": 1}}})
	s.raw("event: ping\ndata: {\"type\": \"ping\"}\n\n")
	if calls == nil {
		ev("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		for i, p := range replyParts3(text) {
			ev("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": p}})
			if drop && i == 1 {
				time.Sleep(20 * time.Millisecond)
				panic(http.ErrAbortHandler)
			}
		}
		ev("content_block_stop", map[string]any{"index": 0})
	}
	for i, c := range calls {
		ev("content_block_start", map[string]any{"index": i, "content_block": map[string]any{
			"type": "tool_use", "id": toolID(i), "name": c.Name, "input": map[string]any{}}})
		for _, frag := range splitParts(c.Args, 3) {
			ev("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": frag}})
		}
		ev("content_block_stop", map[string]any{"index": i})
	}
	ev("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 7}})
	ev("message_stop", map[string]any{})
}

func (m *Mock) anthropicListModels(w http.ResponseWriter, r *http.Request) {
	key := anthropicKey(r)
	m.record(Call{Key: key, Path: "/models"})
	if r.Header.Get("anthropic-version") == "" {
		m.anthropicError(w, 400, "invalid_request_error", "anthropic-version header is required")
		return
	}
	k := m.behaviourKey(key)
	if !strings.HasPrefix(k, "mock-") || strings.HasPrefix(k, "mock-401") {
		m.anthropicError(w, 401, "authentication_error", "invalid x-api-key")
		return
	}
	m.mu.Lock()
	ids := append([]string(nil), m.models...)
	m.mu.Unlock()
	start := 0
	if after := r.URL.Query().Get("after_id"); after != "" {
		for i, id := range ids {
			if id == after {
				start = i + 1
			}
		}
	}
	limit := m.pageSize
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n < limit {
		limit = n
	}
	end := start + limit
	if end > len(ids) {
		end = len(ids)
	}
	page := ids[start:end]
	data := make([]map[string]any, 0, len(page))
	for _, id := range page {
		data = append(data, map[string]any{"type": "model", "id": id, "display_name": id, "created_at": "2026-01-01T00:00:00Z"})
	}
	out := map[string]any{"data": data, "has_more": end < len(ids)}
	if len(page) > 0 {
		out["first_id"], out["last_id"] = page[0], page[len(page)-1]
	}
	writeJSON(w, http.StatusOK, out)
}
