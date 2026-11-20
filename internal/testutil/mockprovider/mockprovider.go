// Package mockprovider is a scripted OpenAI-compatible provider used by the
// integration tests (via httptest) and by `make mock` (standalone, see
// cmd/mockprovider). Behaviour is selected by the BEARER KEY, so a test (or a
// person using the dashboard) scripts a provider simply by choosing key values:
//
//	mock-ok*          200 chat completion / SSE stream with usage
//	mock-429-<secs>*  429 with Retry-After: <secs> (default 5)
//	mock-402*         402 insufficient credits
//	mock-401*         401 invalid key (also rejects /models)
//	mock-500*         500 upstream error
//	mock-503*         503 service unavailable
//	mock-404*         404 model not found
//	mock-400*         400 bad request
//	mock-midstream*   streams two chunks, then drops the connection
//
// Any other key is answered like mock-401. Every error body contains the
// literal SENTINEL-BODY so tests can assert it is never stored or echoed.
package mockprovider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// SentinelBody is embedded in every error body the mock produces.
	SentinelBody = "SENTINEL-BODY"
	// ReplyText is the full assistant reply of a successful completion.
	ReplyText = "SENTINEL-RESPONSE-9c1d done"
)

// replyParts are the three content chunks of a streamed reply (joined = ReplyText).
var replyParts = []string{"SENTINEL-RESP", "ONSE-9c1d", " done"}

// Flavour selects provider-specific error styling and the default model list.
type Flavour string

const (
	Groq       Flavour = "groq"
	OpenRouter Flavour = "openrouter"
)

// Call is one request the mock received.
type Call struct {
	Key    string
	Path   string // "/chat/completions" or "/models"
	Model  string // requested model (chat only)
	Stream bool
}

// Options configures a Server.
type Options struct {
	Flavour Flavour  // default Groq
	Models  []string // ids served by /models; default per flavour
}

// DefaultModels returns the default /models ids for a flavour.
func DefaultModels(f Flavour) []string {
	if f == OpenRouter {
		return []string{"meta-llama/llama-3.3-70b-instruct", "meta-llama/llama-3.1-8b-instruct"}
	}
	return []string{"llama-3.3-70b-versatile", "llama-3.1-8b-instant"}
}

// Mock is the scripted provider. Use Handler for standalone serving, or New for
// an httptest server.
type Mock struct {
	flavour Flavour

	mu     sync.Mutex
	models []string
	calls  []Call
	alias  map[string]string // key -> key whose behaviour it takes
}

// NewMock builds a mock without a listener.
func NewMock(o Options) *Mock {
	if o.Flavour == "" {
		o.Flavour = Groq
	}
	if o.Models == nil {
		o.Models = DefaultModels(o.Flavour)
	}
	return &Mock{flavour: o.Flavour, models: append([]string(nil), o.Models...)}
}

// Server is a Mock bound to an httptest server.
type Server struct {
	*Mock
	srv *httptest.Server
}

// New starts an httptest server around a new Mock. Close it with Close.
func New(o Options) *Server {
	m := NewMock(o)
	return &Server{Mock: m, srv: httptest.NewServer(m.Handler())}
}

// URL is the server root.
func (s *Server) URL() string { return s.srv.URL }

// BaseURL is the value for GROQ_BASE_URL / OPENROUTER_BASE_URL.
func (s *Server) BaseURL() string { return s.srv.URL + "/v1" }

// Close shuts the server down.
func (s *Server) Close() { s.srv.Close() }

// SetModels replaces the /models list.
func (m *Mock) SetModels(ids ...string) {
	m.mu.Lock()
	m.models = append([]string(nil), ids...)
	m.mu.Unlock()
}

// Alias makes requests bearing key behave as if they bore as (e.g. flip a
// "mock-401-x" key to "mock-ok-x" to simulate the provider recovering). Counters
// stay keyed by the key actually presented.
func (m *Mock) Alias(key, as string) {
	m.mu.Lock()
	if m.alias == nil {
		m.alias = map[string]string{}
	}
	m.alias[key] = as
	m.mu.Unlock()
}

// behaviourKey resolves aliases.
func (m *Mock) behaviourKey(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if as, ok := m.alias[key]; ok {
		return as
	}
	return key
}

// ChatCalls is the number of chat requests received with exactly this key.
func (m *Mock) ChatCalls(key string) int { return m.count(key, "/chat/completions") }

// ModelCalls is the number of /models requests received with exactly this key.
func (m *Mock) ModelCalls(key string) int { return m.count(key, "/models") }

// TotalChatCalls is the number of chat requests received across all keys.
func (m *Mock) TotalChatCalls() int { return m.count("", "/chat/completions") }

// TotalModelCalls is the number of /models requests received across all keys.
func (m *Mock) TotalModelCalls() int { return m.count("", "/models") }

// Calls returns a copy of every request received, in order.
func (m *Mock) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Call(nil), m.calls...)
}

func (m *Mock) count(key, path string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c.Path == path && (key == "" || c.Key == key) {
			n++
		}
	}
	return n
}

func (m *Mock) record(c Call) {
	m.mu.Lock()
	m.calls = append(m.calls, c)
	m.mu.Unlock()
}

// Handler serves /<anything>/chat/completions, /<anything>/models and /_models
// (POST a JSON array of ids to replace the model list, for manual testing).
func (m *Mock) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions") && r.Method == http.MethodPost:
			m.chat(w, r)
		case strings.HasSuffix(r.URL.Path, "/models") && r.Method == http.MethodGet:
			m.listModels(w, r)
		case r.URL.Path == "/_models" && r.Method == http.MethodPost:
			var ids []string
			if err := json.NewDecoder(r.Body).Decode(&ids); err != nil {
				http.Error(w, "body must be a JSON array of model ids", http.StatusBadRequest)
				return
			}
			m.SetModels(ids...)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
}

func bearerKey(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

func (m *Mock) listModels(w http.ResponseWriter, r *http.Request) {
	key := bearerKey(r)
	m.record(Call{Key: key, Path: "/models"})
	key = m.behaviourKey(key)
	if !strings.HasPrefix(key, "mock-") || strings.HasPrefix(key, "mock-401") {
		m.writeError(w, http.StatusUnauthorized, "invalid_api_key", "authentication_error", 0)
		return
	}
	m.mu.Lock()
	ids := append([]string(nil), m.models...)
	m.mu.Unlock()
	data := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		item := map[string]any{"id": id, "object": "model", "created": 1763640000}
		if m.flavour == OpenRouter {
			item["name"] = id
			item["context_length"] = 131072
			item["pricing"] = map[string]string{"prompt": "0.0000005", "completion": "0.0000008"}
			item["top_provider"] = map[string]any{"max_completion_tokens": 8192}
		} else {
			item["owned_by"] = "mock"
			item["context_window"] = 131072
		}
		data = append(data, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (m *Mock) chat(w http.ResponseWriter, r *http.Request) {
	key := bearerKey(r)
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	m.record(Call{Key: key, Path: "/chat/completions", Model: req.Model, Stream: req.Stream})
	key = m.behaviourKey(key)

	switch {
	case strings.HasPrefix(key, "mock-ok"):
		m.ok(w, req.Model, req.Stream, false)
	case strings.HasPrefix(key, "mock-midstream"):
		m.ok(w, req.Model, req.Stream, true)
	case strings.HasPrefix(key, "mock-429"):
		secs := 5
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
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		m.writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "rate_limit_error", 429)
	case strings.HasPrefix(key, "mock-402"):
		m.writeError(w, http.StatusPaymentRequired, "insufficient_credits", "insufficient_credits", 402)
	case strings.HasPrefix(key, "mock-500"):
		m.writeError(w, http.StatusInternalServerError, "internal_error", "server_error", 500)
	case strings.HasPrefix(key, "mock-503"):
		m.writeError(w, http.StatusServiceUnavailable, "service_unavailable", "server_error", 503)
	case strings.HasPrefix(key, "mock-404"):
		m.writeError(w, http.StatusNotFound, "model_not_found", "invalid_request_error", 404)
	case strings.HasPrefix(key, "mock-400"):
		m.writeError(w, http.StatusBadRequest, "invalid_request", "invalid_request_error", 400)
	default: // mock-401* and anything unrecognised
		m.writeError(w, http.StatusUnauthorized, "invalid_api_key", "authentication_error", 401)
	}
}

// writeError writes an error body in the flavour's style. OpenRouter puts the
// numeric status in error.code; Groq/OpenAI use a string code.
func (m *Mock) writeError(w http.ResponseWriter, status int, code, typ string, numeric int) {
	msg := fmt.Sprintf("%s mock provider error (%s)", SentinelBody, code)
	var body map[string]any
	if m.flavour == OpenRouter && numeric != 0 {
		body = map[string]any{"error": map[string]any{"code": numeric, "message": msg}}
	} else {
		body = map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": code}}
	}
	writeJSON(w, status, body)
}

func (m *Mock) ok(w http.ResponseWriter, model string, stream, dropMidstream bool) {
	if model == "" {
		model = "mock-model"
	}
	if !stream {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion", "created": time.Now().Unix(), "model": model,
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": ReplyText}}},
			"usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19},
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	send := func(delta map[string]any, finish any, usage map[string]int) {
		chunk := map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk",
			"created": time.Now().Unix(), "model": model}
		if delta != nil {
			chunk["choices"] = []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}}
		} else {
			chunk["choices"] = []map[string]any{}
		}
		if usage != nil {
			chunk["usage"] = usage
		}
		b, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	for i, part := range replyParts {
		d := map[string]any{"content": part}
		if i == 0 {
			d["role"] = "assistant"
		}
		send(d, nil, nil)
		if dropMidstream && i == 1 {
			time.Sleep(20 * time.Millisecond)
			panic(http.ErrAbortHandler) // drop the connection without a terminating chunk
		}
	}
	send(map[string]any{}, "stop", nil)
	send(nil, nil, map[string]int{"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19})
	fmt.Fprint(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
