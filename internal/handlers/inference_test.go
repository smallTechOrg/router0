package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/catalog"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/keystate"
	"github.com/zer0-router/zer0/internal/providers"
	"github.com/zer0-router/zer0/internal/routing"
)

// ---- fake provider (registered under unique names so adapters stay untouched) ----

type fakeProv struct {
	name  string
	mu    sync.Mutex
	calls []string // api keys used
	// behaviour by api key
	complete func(key string) (*providers.ChatResponse, error)
	stream   func(ctx context.Context, key string) (<-chan providers.ChatChunk, error)
}

func (f *fakeProv) Name() string { return f.name }
func (f *fakeProv) Complete(ctx context.Context, req *providers.ChatRequest, key string) (*providers.ChatResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, key+"|"+req.Model)
	f.mu.Unlock()
	return f.complete(key)
}
func (f *fakeProv) Stream(ctx context.Context, req *providers.ChatRequest, key string) (<-chan providers.ChatChunk, error) {
	f.mu.Lock()
	f.calls = append(f.calls, key+"|"+req.Model)
	f.mu.Unlock()
	return f.stream(ctx, key)
}
func (f *fakeProv) HealthCheck(ctx context.Context, k string) (*providers.HealthResult, error) {
	return nil, nil
}
func (f *fakeProv) DefaultModels() []string { return nil }
func (f *fakeProv) ListModels(ctx context.Context, k string) ([]providers.ModelInfo, error) {
	return nil, nil
}
func (f *fakeProv) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

type stubResolver struct{ entries []catalog.Entry }

func (s stubResolver) Resolve(ctx context.Context, org, req string) ([]catalog.Entry, error) {
	if req != "llama" {
		return nil, nil
	}
	return s.entries, nil
}

type stubOrder []string

func (s stubOrder) ProviderOrder(ctx context.Context, org string) ([]string, error) { return s, nil }

type stubKeys map[string][]*keys.ProviderKey

func (s stubKeys) ActiveByOrgProvider(ctx context.Context, org, p string) ([]*keys.ProviderKey, error) {
	return s[p], nil
}

type memStates struct {
	mu    sync.Mutex
	marks []string
}

func (m *memStates) Check(ctx context.Context, k, mm string) (keystate.State, error) {
	return keystate.State{}, nil
}
func (m *memStates) CheckMany(ctx context.Context, p []keystate.KeyModel) (map[keystate.KeyModel]keystate.State, error) {
	return map[keystate.KeyModel]keystate.State{}, nil
}
func (m *memStates) Mark(ctx context.Context, k, mm string, e *providers.ProviderError) error {
	m.mu.Lock()
	m.marks = append(m.marks, k+":"+string(e.Class))
	m.mu.Unlock()
	return nil
}

type verifier struct{}

func (verifier) Verify(ctx context.Context, raw string) (auth.Principal, error) {
	return auth.Principal{OrgID: "org1", TokenID: "tok1", TokenName: "t"}, nil
}

type rig struct {
	srv    *httptest.Server
	a, b   *fakeProv
	states *memStates
}

func pkey(id, label string) *keys.ProviderKey {
	return &keys.ProviderKey{ID: id, Label: label, Status: "active", Ciphertext: []byte(label)}
}

func newRig(t *testing.T, order []string) *rig {
	t.Helper()
	r := &rig{a: &fakeProv{name: "fakea"}, b: &fakeProv{name: "fakeb"}, states: &memStates{}}
	providers.Register(r.a)
	providers.Register(r.b)
	ex := routing.NewExecutor(routing.Config{States: r.states, MaxAttempts: 8, AttemptTimeout: 5 * time.Second})
	h := NewInferenceHandler(InferenceConfig{
		Resolver: stubResolver{entries: []catalog.Entry{
			{Canonical: "llama", Provider: "fakea", ProviderModelID: "llama-a", Kind: "chat"},
			{Canonical: "llama", Provider: "fakeb", ProviderModelID: "llama-b", Kind: "chat"},
		}},
		Priority: stubOrder(order),
		Keys: stubKeys{
			"fakea": {pkey("ka1", "a1"), pkey("ka2", "a2")},
			"fakeb": {pkey("kb1", "b1")},
		},
		Executor:         ex,
		FirstByteTimeout: time.Second,
		Decrypt:          func(k *keys.ProviderKey) (string, error) { return string(k.Ciphertext), nil },
	})
	r.srv = httptest.NewServer(auth.ServiceTokenMiddleware(verifier{})(http.HandlerFunc(h.ChatCompletions)))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *rig) post(t *testing.T, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", r.srv.URL, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer r0-sk-test")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func okResp(content string) (*providers.ChatResponse, error) {
	return &providers.ChatResponse{ID: "up-1", Model: "llama-a", Content: content, FinishReason: "stop",
		Usage: providers.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}}, nil
}

func pe(class providers.ErrorClass, httpStatus int, ra time.Duration) error {
	return &providers.ProviderError{Class: class, HTTP: httpStatus, Code: "c", RetryAfter: ra, Message: "SENTINEL-BODY"}
}

const body = `{"model":"llama","messages":[{"role":"user","content":"hi SENTINEL-PROMPT"}]}`

func decode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var m map[string]any
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("bad json %q: %v", b, err)
	}
	return m
}

func TestInference_FailoverNonStream(t *testing.T) {
	r := newRig(t, []string{"fakea", "fakeb"})
	r.a.complete = func(key string) (*providers.ChatResponse, error) {
		if key == "a1" {
			return nil, pe(providers.ErrorClassTransient, 503, 0)
		}
		return okResp("hello")
	}
	resp := r.post(t, body, nil)
	h := resp.Header
	m := decode(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d %v", resp.StatusCode, m)
	}
	if m["model"] != "llama" || m["object"] != "chat.completion" {
		t.Fatalf("%v", m)
	}
	if h.Get("X-Router0-Provider") != "fakea" || h.Get("X-Router0-Key") != "a2" || h.Get("X-Router0-Model") != "llama-a" ||
		h.Get("X-Router0-Attempts") != "2" || h.Get("X-Router0-Canonical-Model") != "llama" || !strings.HasPrefix(h.Get("X-Router0-Request-Id"), "r0-") {
		t.Fatalf("headers %v", h)
	}
	if got := m["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"]; got != "hello" {
		t.Fatalf("content %v", got)
	}
	if len(r.states.marks) != 1 || r.states.marks[0] != "ka1:transient" {
		t.Fatalf("marks %v", r.states.marks)
	}
}

func TestInference_ProviderFailoverAndOverride(t *testing.T) {
	r := newRig(t, []string{"fakea", "fakeb"})
	r.a.complete = func(string) (*providers.ChatResponse, error) {
		return nil, pe(providers.ErrorClassRateLimit, 429, 40*time.Second)
	}
	r.b.complete = func(string) (*providers.ChatResponse, error) { return okResp("from b") }
	resp := r.post(t, body, nil)
	if resp.StatusCode != 200 || resp.Header.Get("X-Router0-Provider") != "fakeb" || resp.Header.Get("X-Router0-Attempts") != "3" {
		t.Fatalf("status %d %v", resp.StatusCode, resp.Header)
	}
	resp.Body.Close()

	// override restricts to fakeb: fakea untouched
	before := r.a.callCount()
	resp = r.post(t, body, map[string]string{"X-Router0-Providers": "fakeb"})
	if resp.StatusCode != 200 || r.a.callCount() != before {
		t.Fatalf("status %d, fakea calls %d->%d", resp.StatusCode, before, r.a.callCount())
	}
	resp.Body.Close()

	// unknown override: 400 and no calls at all
	bb := r.b.callCount()
	resp = r.post(t, body, map[string]string{"X-Router0-Providers": "nosuch"})
	m := decode(t, resp)
	if resp.StatusCode != 400 || m["error"].(map[string]any)["code"] != "invalid_provider_override" || r.a.callCount() != before || r.b.callCount() != bb {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
}

func TestInference_RateLimitedFinalErrorWithTrail(t *testing.T) {
	r := newRig(t, []string{"fakea", "fakeb"})
	r.a.complete = func(key string) (*providers.ChatResponse, error) {
		if key == "a1" {
			return nil, pe(providers.ErrorClassRateLimit, 429, 5*time.Second)
		}
		return nil, pe(providers.ErrorClassRateLimit, 429, 9*time.Second)
	}
	r.b.complete = func(string) (*providers.ChatResponse, error) {
		return nil, pe(providers.ErrorClassInsufficientCredit, 402, 0)
	}
	resp := r.post(t, body, nil)
	m := decode(t, resp)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "5" {
		t.Fatalf("%d retry-after=%q %v", resp.StatusCode, resp.Header.Get("Retry-After"), m)
	}
	e := m["error"].(map[string]any)
	if e["code"] != "rate_limited" || e["type"] != "rate_limit_error" || e["param"] != nil {
		t.Fatalf("%v", e)
	}
	r0 := e["router0"].(map[string]any)
	trail := r0["attempts"].([]any)
	if len(trail) != 3 || r0["request_id"] != resp.Header.Get("X-Router0-Request-Id") {
		t.Fatalf("%v", r0)
	}
	first := trail[0].(map[string]any)
	if first["provider"] != "fakea" || first["key"] != "a1" || first["outcome"] != "error" || first["class"] != "rate_limit" || first["retry_after_s"] != float64(5) {
		t.Fatalf("%v", first)
	}
	if strings.Contains(mustString(m), "SENTINEL-BODY") {
		t.Fatal("provider body leaked")
	}
}

func mustString(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestInference_ModelNotFoundAndBadInput(t *testing.T) {
	r := newRig(t, []string{"fakea"})
	resp := r.post(t, `{"model":"nope","messages":[]}`, nil)
	m := decode(t, resp)
	e := m["error"].(map[string]any)
	if resp.StatusCode != 404 || e["code"] != "model_not_found" || e["param"] != "model" || r.a.callCount() != 0 {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	for _, c := range []struct{ body, code string }{
		{`{`, "invalid_json"},
		{`{"messages":[]}`, "missing_model"},
		{`{"model":"llama","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`, "unsupported_content"},
	} {
		resp := r.post(t, c.body, nil)
		m := decode(t, resp)
		if resp.StatusCode != 400 || m["error"].(map[string]any)["code"] != c.code {
			t.Fatalf("%s: %d %v", c.code, resp.StatusCode, m)
		}
	}
	// no principal
	req, _ := http.NewRequest("POST", r.srv.URL, strings.NewReader(body))
	resp2, _ := http.DefaultClient.Do(req)
	if resp2.StatusCode != 401 {
		t.Fatalf("no auth: %d", resp2.StatusCode)
	}
	resp2.Body.Close()
}

func chunks(items ...providers.ChatChunk) <-chan providers.ChatChunk {
	ch := make(chan providers.ChatChunk, len(items)+1)
	for _, c := range items {
		ch <- c
	}
	close(ch)
	return ch
}

func readSSE(t *testing.T, resp *http.Response) (events []map[string]any, done bool) {
	t.Helper()
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		d := strings.TrimPrefix(line, "data: ")
		if d == "[DONE]" {
			done = true
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(d), &m); err != nil {
			t.Fatalf("bad sse %q", d)
		}
		events = append(events, m)
	}
	return
}

const streamBody = `{"model":"llama","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func TestInference_StreamFailoverBeforeFirstChunk(t *testing.T) {
	r := newRig(t, []string{"fakea", "fakeb"})
	r.a.stream = func(ctx context.Context, key string) (<-chan providers.ChatChunk, error) {
		if key == "a1" {
			return nil, pe(providers.ErrorClassTransient, 503, 0)
		}
		return chunks(
			providers.ChatChunk{ID: "u", Model: "llama-a", Content: "one"},
			providers.ChatChunk{Content: "two"},
			providers.ChatChunk{Content: "three", FinishReason: "stop"},
			providers.ChatChunk{Usage: &providers.Usage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7}},
			providers.ChatChunk{Done: true},
		), nil
	}
	resp := r.post(t, streamBody, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" ||
		resp.Header.Get("X-Router0-Key") != "a2" || resp.Header.Get("X-Router0-Attempts") != "2" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	events, done := readSSE(t, resp)
	if !done {
		t.Fatal("no [DONE]")
	}
	// client did not ask for usage: 3 content chunks only
	if len(events) != 3 {
		t.Fatalf("events %v", events)
	}
	var text string
	for _, e := range events {
		if e["model"] != "llama" || e["object"] != "chat.completion.chunk" {
			t.Fatalf("%v", e)
		}
		d := e["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		if c, ok := d["content"].(string); ok {
			text += c
		}
	}
	if text != "onetwothree" {
		t.Fatalf("text %q", text)
	}
	if events[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["role"] != "assistant" {
		t.Fatal("first chunk missing role")
	}
}

func TestInference_StreamUsageForwardedOnlyWhenAsked(t *testing.T) {
	r := newRig(t, []string{"fakea"})
	r.a.stream = func(ctx context.Context, key string) (<-chan providers.ChatChunk, error) {
		return chunks(providers.ChatChunk{Content: "x"}, providers.ChatChunk{Usage: &providers.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}}, providers.ChatChunk{Done: true}), nil
	}
	resp := r.post(t, `{"model":"llama","stream":true,"stream_options":{"include_usage":true},"messages":[]}`, nil)
	events, _ := readSSE(t, resp)
	if len(events) != 2 || events[1]["usage"] == nil || len(events[1]["choices"].([]any)) != 0 {
		t.Fatalf("%v", events)
	}
}

func TestInference_StreamInterruptedAfterCommit(t *testing.T) {
	r := newRig(t, []string{"fakea", "fakeb"})
	r.a.stream = func(ctx context.Context, key string) (<-chan providers.ChatChunk, error) {
		return chunks(
			providers.ChatChunk{Content: "one"},
			providers.ChatChunk{Content: "two"},
			providers.ChatChunk{Err: &providers.ProviderError{Class: providers.ErrorClassTransient, Code: "stream_interrupted", Message: "SENTINEL-BODY"}},
		), nil
	}
	r.b.stream = func(ctx context.Context, key string) (<-chan providers.ChatChunk, error) {
		t.Error("no second provider after commit")
		return nil, nil
	}
	resp := r.post(t, streamBody, nil)
	events, done := readSSE(t, resp)
	if !done || len(events) != 3 {
		t.Fatalf("done=%v events=%v", done, events)
	}
	e := events[2]["error"].(map[string]any)
	if e["code"] != "stream_interrupted" || e["type"] != "upstream_error" || strings.Contains(mustString(events), "SENTINEL-BODY") {
		t.Fatalf("%v", e)
	}
	// key marked per classified error
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r.states.mu.Lock()
		n := len(r.states.marks)
		r.states.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(r.states.marks) != 1 || r.states.marks[0] != "ka1:transient" {
		t.Fatalf("marks %v", r.states.marks)
	}
}

func TestInference_StreamFirstByteTimeoutFailsOver(t *testing.T) {
	r := newRig(t, []string{"fakea", "fakeb"})
	r.a.stream = func(ctx context.Context, key string) (<-chan providers.ChatChunk, error) {
		ch := make(chan providers.ChatChunk)
		go func() { <-ctx.Done(); close(ch) }() // silent upstream until cancelled
		return ch, nil
	}
	r.b.stream = func(ctx context.Context, key string) (<-chan providers.ChatChunk, error) {
		return chunks(providers.ChatChunk{Content: "ok"}, providers.ChatChunk{Done: true}), nil
	}
	start := time.Now()
	resp := r.post(t, streamBody, nil)
	if resp.StatusCode != 200 || resp.Header.Get("X-Router0-Provider") != "fakeb" || resp.Header.Get("X-Router0-Attempts") != "3" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	resp.Body.Close()
	if time.Since(start) > 4*time.Second {
		t.Fatal("too slow")
	}
}

func TestInference_StreamAllFailBeforeCommitReturnsJSONError(t *testing.T) {
	r := newRig(t, []string{"fakea", "fakeb"})
	fail := func(ctx context.Context, key string) (<-chan providers.ChatChunk, error) {
		return nil, pe(providers.ErrorClassInsufficientCredit, 402, 0)
	}
	r.a.stream, r.b.stream = fail, fail
	resp := r.post(t, streamBody, nil)
	m := decode(t, resp)
	if resp.StatusCode != 402 || m["error"].(map[string]any)["code"] != "insufficient_credits" {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
}
