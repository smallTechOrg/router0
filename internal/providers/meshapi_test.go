package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const meshModel = "mistral/mistral-7b-instruct-v0"

func meshFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "meshapi", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type meshCall struct {
	method, path, auth string
	hdr                http.Header
	body               map[string]any
}

// meshServer serves handler and records every request.
func meshServer(t *testing.T, handler func(n int, w http.ResponseWriter, r *http.Request)) (*[]meshCall, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	calls := &[]meshCall{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c := meshCall{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), hdr: r.Header.Clone()}
		_ = json.Unmarshal(b, &c.body)
		mu.Lock()
		*calls = append(*calls, c)
		n := len(*calls)
		mu.Unlock()
		handler(n, w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MESHAPI_BASE_URL", srv.URL+"/v1/") // trailing slash tolerated
	return calls, &mu
}

func meshReq() *ChatRequest {
	return &ChatRequest{Model: meshModel, Messages: []ChatMessage{{Role: "user", Content: "Hello!"}}}
}

func meshProvider(t *testing.T) Provider {
	t.Helper()
	p, err := Get("meshapi")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func meshDrain(t *testing.T, ch <-chan ChatChunk) []ChatChunk {
	t.Helper()
	var out []ChatChunk
	timeout := time.After(5 * time.Second)
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, c)
		case <-timeout:
			t.Fatal("stream did not finish")
		}
	}
}

func TestMeshAPICompleteRequestShape(t *testing.T) {
	calls, _ := meshServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		w.Write(meshFixture(t, "chat.json"))
	})
	resp, err := meshProvider(t).Complete(context.Background(), meshReq(), "rsk_test123")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hello from Mesh" || resp.Usage.TotalTokens != 8 {
		t.Fatalf("resp %+v", resp)
	}
	c := (*calls)[0]
	if c.method != http.MethodPost || c.path != "/v1/chat/completions" {
		t.Fatalf("got %s %s", c.method, c.path)
	}
	if c.auth != "Bearer rsk_test123" {
		t.Fatalf("auth %q", c.auth)
	}
	if c.hdr.Get("HTTP-Referer") != "" || c.hdr.Get("X-Title") != "" {
		t.Fatalf("unexpected attribution headers: %v", c.hdr)
	}
	if c.body["model"] != meshModel {
		t.Fatalf("model altered: %v", c.body["model"])
	}
}

func TestMeshAPIStreamToolCallAndUsage(t *testing.T) {
	calls, _ := meshServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(meshFixture(t, "stream.sse"))
	})
	ch, err := meshProvider(t).Stream(context.Background(), meshReq(), "rsk_x")
	if err != nil {
		t.Fatal(err)
	}
	chunks := meshDrain(t, ch)
	var tool *ToolCall
	var usage *Usage
	var finish string
	var done bool
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatalf("chunk err %v", c.Err)
		}
		if len(c.ToolCalls) > 0 {
			tool = &c.ToolCalls[0]
		}
		if c.Usage != nil {
			usage = c.Usage
		}
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
		done = done || c.Done
	}
	if tool == nil || tool.Function.Name != "get_weather" || tool.ID != "call_1" {
		t.Fatalf("tool call %+v", tool)
	}
	if finish != "tool_calls" || !done {
		t.Fatalf("finish=%q done=%v", finish, done)
	}
	if usage == nil || usage.TotalTokens != 18 || usage.PromptTokens != 11 {
		t.Fatalf("usage %+v", usage)
	}
	if len(*calls) != 1 || (*calls)[0].body["stream_options"] == nil {
		t.Fatalf("first call should include stream_options: %+v", *calls)
	}
}

func TestMeshAPIStreamOptionsRetry(t *testing.T) {
	calls, _ := meshServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			w.WriteHeader(400)
			w.Write(meshFixture(t, "error_400_stream_options.json"))
			return
		}
		w.Write(meshFixture(t, "stream.sse"))
	})
	ch, err := meshProvider(t).Stream(context.Background(), meshReq(), "rsk_x")
	if err != nil {
		t.Fatal(err)
	}
	chunks := meshDrain(t, ch)
	if len(chunks) == 0 || !chunks[len(chunks)-1].Done {
		t.Fatalf("stream incomplete: %d chunks", len(chunks))
	}
	if len(*calls) != 2 {
		t.Fatalf("want 2 calls, got %d", len(*calls))
	}
	if (*calls)[0].body["stream_options"] == nil {
		t.Fatal("first call lacked stream_options")
	}
	if _, has := (*calls)[1].body["stream_options"]; has {
		t.Fatal("retry still sent stream_options")
	}
	if (*calls)[1].body["stream"] != true || (*calls)[1].body["model"] != meshModel {
		t.Fatalf("retry body %v", (*calls)[1].body)
	}
}

func TestMeshAPIStream400WithoutStreamOptionsNotRetried(t *testing.T) {
	calls, _ := meshServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"message":"bad messages","type":"invalid_request_error","code":"invalid_request"}}`))
	})
	_, err := meshProvider(t).Stream(context.Background(), meshReq(), "rsk_x")
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Class != ErrorClassBadRequest {
		t.Fatalf("err %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(*calls))
	}
}

func TestMeshAPIListModels(t *testing.T) {
	calls, _ := meshServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		w.Write(meshFixture(t, "models.json"))
	})
	models, err := meshProvider(t).ListModels(context.Background(), "rsk_x")
	if err != nil {
		t.Fatal(err)
	}
	if (*calls)[0].path != "/v1/models" || (*calls)[0].auth != "Bearer rsk_x" {
		t.Fatalf("call %+v", (*calls)[0])
	}
	want := []string{"mistral/mistral-7b-instruct-v0", "meta-llama/llama-3.3-70b-instruct", "anthropic/claude-sonnet-5.5", "openai/dall-e-3"}
	if len(models) != len(want) {
		t.Fatalf("got %d models: %+v", len(models), models)
	}
	for i, id := range want {
		if models[i].ID != id {
			t.Fatalf("model %d = %q want %q", i, models[i].ID, id)
		}
	}
	if models[0].ContextWindow != 32768 || models[1].ContextWindow != 131072 {
		t.Fatalf("context windows %d %d", models[0].ContextWindow, models[1].ContextWindow)
	}
}

func TestMeshAPIErrorClassification(t *testing.T) {
	cases := []struct {
		fixture string
		status  int
		retry   string
		class   ErrorClass
	}{
		{"error_401.json", 401, "", ErrorClassAuth},
		{"error_429.json", 429, "30", ErrorClassRateLimit},
		{"error_402.json", 402, "", ErrorClassInsufficientCredit},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			meshServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
				if tc.retry != "" {
					w.Header().Set("Retry-After", tc.retry)
				}
				w.WriteHeader(tc.status)
				w.Write(meshFixture(t, tc.fixture))
			})
			_, err := meshProvider(t).Complete(context.Background(), meshReq(), "rsk_x")
			var pe *ProviderError
			if !errors.As(err, &pe) {
				t.Fatalf("err %v", err)
			}
			if pe.Class != tc.class || pe.HTTP != tc.status {
				t.Fatalf("got class=%s http=%d", pe.Class, pe.HTTP)
			}
			if tc.class == ErrorClassRateLimit && pe.RetryAfter != 30*time.Second {
				t.Fatalf("retry-after %v", pe.RetryAfter)
			}
			if strings.Contains(err.Error(), "SECRETBODY") {
				t.Fatalf("Error() leaks body: %s", err.Error())
			}
		})
	}
}

func TestMeshAPIAliases(t *testing.T) {
	for _, a := range []string{"aifiesta", "mesh", "AIFiesta", " Mesh "} {
		if got := CanonicalName(a); got != "meshapi" {
			t.Fatalf("CanonicalName(%q)=%q", a, got)
		}
		p, err := Get(a)
		if err != nil || p.Name() != "meshapi" {
			t.Fatalf("Get(%q)=%v,%v", a, p, err)
		}
	}
	n := 0
	for _, v := range ValidProviders() {
		if v == "meshapi" {
			n++
		}
		if v == "aifiesta" || v == "mesh" {
			t.Fatalf("alias %q listed in ValidProviders", v)
		}
	}
	if n != 1 {
		t.Fatalf("meshapi listed %d times", n)
	}
}

func TestMeshAPICancelledContext(t *testing.T) {
	meshServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		w.Write(meshFixture(t, "chat.json"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := meshProvider(t).Complete(ctx, meshReq(), "rsk_x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Complete err %v", err)
	}
	if _, err := meshProvider(t).Stream(ctx, meshReq(), "rsk_x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream err %v", err)
	}
}
