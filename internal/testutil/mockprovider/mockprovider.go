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
// Extra behaviours: mock-credits* (Anthropic: 400 "credit balance is too low"),
// mock-529* (Anthropic overloaded_error). The behaviour is found by the FIRST
// "mock-" inside the bearer / x-api-key, so Mesh keys such as rsk_mock-429-15-a
// work. Flavours: Groq, OpenRouter, MeshAPI and Gemini speak the OpenAI chat
// wire (Gemini with Google quirks, see gemini.go); Anthropic speaks the native
// Messages API (anthropic.go). Scripted tool-calling conversations live in
// scenarios.go.
//
// Any other key is answered like mock-401. Every error body contains the
// literal SENTINEL-BODY so tests can assert it is never stored or echoed.
package mockprovider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	MeshAPI    Flavour = "meshapi"
	Gemini     Flavour = "gemini"
	Anthropic  Flavour = "anthropic"
)

// Call is one request the mock received.
type Call struct {
	Key    string
	Path   string // "/chat/completions" or "/models"
	Model  string // requested model (chat only)
	Stream bool
	Body   []byte // raw request body (chat only)
}

// Options configures a Server.
type Options struct {
	Flavour Flavour  // default Groq
	Models  []string // ids served by /models; default per flavour
	// PageSize caps models per listing page for the paginated listings
	// (Anthropic after_id, Gemini pageToken); default 2 / 4.
	PageSize int
}

// DefaultModels returns the default /models ids for a flavour.
func DefaultModels(f Flavour) []string {
	switch f {
	case OpenRouter:
		return []string{"meta-llama/llama-3.3-70b-instruct", "meta-llama/llama-3.1-8b-instruct", "anthropic/claude-sonnet-5.5"}
	case MeshAPI:
		return []string{"mistral/mistral-7b-instruct-v0", "meta-llama/llama-3.3-70b-instruct", "anthropic/claude-sonnet-5.5", "openai/dall-e-3"}
	case Anthropic:
		return []string{"claude-sonnet-5-5", "claude-haiku-4-5", "claude-opus-4-5"}
	case Gemini:
		return []string{"models/gemini-2.5-flash", "models/gemini-2.5-pro", "models/gemini-2.5-flash-image",
			"models/gemini-2.5-flash-preview-tts", "models/lyria-realtime-exp", "models/gemini-2.5-computer-use-preview",
			"models/gemini-robotics-er-1.5-preview", "models/text-embedding-004"}
	}
	return []string{"llama-3.3-70b-versatile", "llama-3.1-8b-instant"}
}

// Mock is the scripted provider. Use Handler for standalone serving, or New for
// an httptest server.
type Mock struct {
	flavour  Flavour
	pageSize int

	mu     sync.Mutex
	models []string
	calls  []Call
	alias  map[string]string // key -> key whose behaviour it takes
	// balance is the OpenRouter limit_remaining reported by GET /key (USD);
	// set with SetBalance or POST /_balance. Default 100.
	balance float64
}

// NewMock builds a mock without a listener.
func NewMock(o Options) *Mock {
	if o.Flavour == "" {
		o.Flavour = Groq
	}
	if o.Models == nil {
		o.Models = DefaultModels(o.Flavour)
	}
	if o.PageSize <= 0 {
		o.PageSize = 4
		if o.Flavour == Anthropic {
			o.PageSize = 2
		}
	}
	return &Mock{flavour: o.Flavour, pageSize: o.PageSize, models: append([]string(nil), o.Models...), balance: mockBalanceLimit}
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

// BaseURL is the value for GROQ_BASE_URL / OPENROUTER_BASE_URL /
// ANTHROPIC_BASE_URL / MESHAPI_BASE_URL (".../v1"); for Gemini use URL()
// (GOOGLE_BASE_URL has no /v1 suffix).
func (s *Server) BaseURL() string {
	if s.flavour == Gemini {
		return s.srv.URL
	}
	return s.srv.URL + "/v1"
}

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
		key = as
	}
	if i := strings.Index(key, "mock-"); i > 0 { // rsk_mock-429-15-a -> mock-429-15-a
		key = key[i:]
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

// Handler serves /<anything>/chat/completions (and /<anything>/messages for the
// Anthropic flavour), /<anything>/models, Gemini's /v1beta/models, and /_models
// (POST a JSON array of ids to replace the model list, for manual testing).
func (m *Mock) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/messages") && r.Method == http.MethodPost && m.flavour == Anthropic:
			m.anthropicMessages(w, r)
		case strings.HasSuffix(p, "/chat/completions") && r.Method == http.MethodPost && m.flavour != Anthropic:
			m.chat(w, r)
		case strings.HasSuffix(p, "/v1beta/models") && r.Method == http.MethodGet && m.flavour == Gemini:
			m.geminiListModels(w, r)
		case strings.HasSuffix(p, "/models") && r.Method == http.MethodGet && m.flavour == Anthropic:
			m.anthropicListModels(w, r)
		case strings.HasSuffix(p, "/models") && r.Method == http.MethodGet && m.flavour != Gemini:
			m.listModels(w, r)
		case strings.HasSuffix(p, "/key") && r.Method == http.MethodGet && m.flavour == OpenRouter:
			m.openRouterKey(w, r)
		case strings.HasSuffix(p, "/credits") && r.Method == http.MethodGet && m.flavour == OpenRouter:
			m.openRouterCredits(w, r)
		case p == "/_balance" && r.Method == http.MethodPost:
			var v float64
			if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
				http.Error(w, "body must be a JSON number (remaining USD)", http.StatusBadRequest)
				return
			}
			m.SetBalance(v)
			w.WriteHeader(http.StatusNoContent)
		case p == "/_models" && r.Method == http.MethodPost:
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
		m.writeError(w, http.StatusUnauthorized, "invalid_api_key", "authentication_error", 0, "")
		return
	}
	m.mu.Lock()
	ids := append([]string(nil), m.models...)
	m.mu.Unlock()
	if m.flavour == MeshAPI {
		// Real Mesh returns a BARE JSON ARRAY of model objects (not OpenAI's
		// {"data":[...]} wrapper), each with its own model_type; mirror that so
		// adapter bugs in parsing it cannot hide behind a friendlier mock.
		arr := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			mt, completions := "text", true
			switch {
			case strings.Contains(id, "dall-e") || strings.Contains(id, "image"):
				mt, completions = "image", false
			case strings.Contains(id, "embed"):
				mt, completions = "embedding", false
			case strings.Contains(id, "whisper") || strings.Contains(id, "tts"):
				mt, completions = "audio", false
			}
			arr = append(arr, map[string]any{
				"id": id, "name": id, "context_length": 131072, "is_free": false,
				"model_type": mt, "supports_completions_api": completions, "supports_responses_api": completions,
				"supports_tools": completions, "supports_thinking": false,
				"input_modalities": []string{"text"}, "output_modalities": []string{mt},
				"pricing": map[string]any{},
			})
		}
		writeJSON(w, http.StatusOK, arr)
		return
	}
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readBody(r *http.Request) []byte {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	return b
}
