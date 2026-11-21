package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func collect(t *testing.T, ch <-chan ChatChunk) []ChatChunk {
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

func TestCompleteSendsTemperatureZeroAndParsesTools(t *testing.T) {
	var gotBody map[string]any
	var gotAuth, gotReferer, gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		gotAuth = r.Header.Get("Authorization")
		gotReferer, gotTitle = r.Header.Get("HTTP-Referer"), r.Header.Get("X-Title")
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"id":"x","model":"m","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer srv.Close()
	t.Setenv("OPENROUTER_BASE_URL", srv.URL)

	p, err := Get("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), &ChatRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "hi"}}, Temperature: f64(0)}, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := gotBody["temperature"]; !ok || v.(float64) != 0 {
		t.Fatalf("temperature 0 missing from upstream body: %v", gotBody)
	}
	if _, ok := gotBody["stream_options"]; ok {
		t.Fatal("non-stream must not set stream_options")
	}
	if gotAuth != "Bearer sk-test" || gotReferer != "https://router0.local" || gotTitle != "router0" {
		t.Fatalf("headers: %q %q %q", gotAuth, gotReferer, gotTitle)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Name != "f" || resp.FinishReason != "tool_calls" || resp.Usage.TotalTokens != 7 {
		t.Fatalf("resp: %+v", resp)
	}
}

func TestTemperatureOmittedWhenNil(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	t.Setenv("GROQ_BASE_URL", srv.URL)
	p, _ := Get("groq")
	if _, err := p.Complete(context.Background(), &ChatRequest{Model: "m"}, "k"); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBody["temperature"]; ok {
		t.Fatal("nil temperature must be omitted")
	}
}

func TestStreamToolDeltasUsageAndLongLine(t *testing.T) {
	big := strings.Repeat("x", 16*1024)
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"1","model":"m","choices":[{"delta":{"role":"assistant","content":"He"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"f","arguments":""}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"1","choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"`+big+`"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("GROQ_BASE_URL", srv.URL)
	p, _ := Get("groq")
	ch, err := p.Stream(context.Background(), &ChatRequest{Model: "m", Temperature: f64(0)}, "k")
	if err != nil {
		t.Fatal(err)
	}
	chunks := collect(t, ch)

	so, _ := gotBody["stream_options"].(map[string]any)
	if so == nil || so["include_usage"] != true || gotBody["stream"] != true {
		t.Fatalf("stream options missing: %v", gotBody)
	}
	var content string
	var sawIdx0, sawIdx1, sawFinish, sawDone bool
	var usage *Usage
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatalf("unexpected err %v", c.Err)
		}
		content += c.Content
		for _, tc := range c.ToolCalls {
			if tc.Index == nil {
				t.Fatal("tool call index missing")
			}
			if *tc.Index == 0 && tc.ID == "c1" {
				sawIdx0 = true
			}
			if *tc.Index == 1 && len(tc.Function.Arguments) == len(big) {
				sawIdx1 = true
			}
		}
		if c.FinishReason == "tool_calls" {
			sawFinish = true
		}
		if c.Usage != nil {
			usage = c.Usage
		}
		if c.Done {
			sawDone = true
		}
	}
	if content != "He" || !sawIdx0 || !sawIdx1 || !sawFinish || !sawDone {
		t.Fatalf("content=%q idx0=%v idx1=%v finish=%v done=%v", content, sawIdx0, sawIdx1, sawFinish, sawDone)
	}
	if usage == nil || usage.TotalTokens != 11 {
		t.Fatalf("usage: %+v", usage)
	}
}

func TestStreamSSEErrorEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `data: {"id":"1","choices":[{"delta":{"content":"a"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"error":{"message":"upstream gone","code":502}}`+"\n\n")
	}))
	defer srv.Close()
	t.Setenv("OPENROUTER_BASE_URL", srv.URL)
	p, _ := Get("openrouter")
	ch, err := p.Stream(context.Background(), &ChatRequest{Model: "m"}, "k")
	if err != nil {
		t.Fatal(err)
	}
	chunks := collect(t, ch)
	last := chunks[len(chunks)-1]
	if last.Err == nil || last.Err.Class != ErrorClassTransient || last.Err.HTTP != 502 {
		t.Fatalf("last chunk: %+v", last)
	}
}

func TestStreamPrematureEOF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `data: {"id":"1","choices":[{"delta":{"content":"a"}}]}`+"\n\n")
	}))
	defer srv.Close()
	t.Setenv("GROQ_BASE_URL", srv.URL)
	p, _ := Get("groq")
	ch, _ := p.Stream(context.Background(), &ChatRequest{Model: "m"}, "k")
	chunks := collect(t, ch)
	last := chunks[len(chunks)-1]
	if last.Err == nil || last.Err.Class != ErrorClassTransient {
		t.Fatalf("want transient Err on premature EOF, got %+v", last)
	}
}

func TestHTTPErrorsAreClassified(t *testing.T) {
	cases := []struct {
		status int
		hdr    string
		body   string
		want   ErrorClass
		ra     time.Duration
	}{
		{429, "9", `{"error":{"message":"SENTINEL-BODY","code":"rate_limit_exceeded"}}`, ErrorClassRateLimit, 9 * time.Second},
		{402, "", `{"error":{"message":"SENTINEL-BODY"}}`, ErrorClassInsufficientCredit, 0},
		{401, "", `SENTINEL-BODY`, ErrorClassAuth, 0},
		{500, "", `SENTINEL-BODY`, ErrorClassTransient, 0},
		{400, "", `{"error":{"message":"bad temp","type":"invalid_request_error"}}`, ErrorClassBadRequest, 0},
	}
	for _, c := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/stream=%v", c.status, stream), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if c.hdr != "" {
						w.Header().Set("Retry-After", c.hdr)
					}
					w.WriteHeader(c.status)
					fmt.Fprint(w, c.body)
				}))
				defer srv.Close()
				t.Setenv("GROQ_BASE_URL", srv.URL)
				p, _ := Get("groq")
				var err error
				if stream {
					_, err = p.Stream(context.Background(), &ChatRequest{Model: "m"}, "k")
				} else {
					_, err = p.Complete(context.Background(), &ChatRequest{Model: "m"}, "k")
				}
				var pe *ProviderError
				if !errors.As(err, &pe) {
					t.Fatalf("want *ProviderError, got %T %v", err, err)
				}
				if pe.Class != c.want || pe.HTTP != c.status || pe.RetryAfter != c.ra {
					t.Fatalf("got %+v", pe)
				}
				if strings.Contains(pe.Error(), "SENTINEL-BODY") {
					t.Fatalf("Error() leaks body: %s", pe.Error())
				}
			})
		}
	}
}

func TestOpenRouter200ErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"error":{"message":"Insufficient credits","code":402}}`)
	}))
	defer srv.Close()
	t.Setenv("OPENROUTER_BASE_URL", srv.URL)
	p, _ := Get("openrouter")
	_, err := p.Complete(context.Background(), &ChatRequest{Model: "m"}, "k")
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Class != ErrorClassInsufficientCredit {
		t.Fatalf("got %v", err)
	}
}

func TestNetworkErrorIsTransientAndCancelIsNot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	t.Setenv("GROQ_BASE_URL", url)
	p, _ := Get("groq")
	_, err := p.Complete(context.Background(), &ChatRequest{Model: "m"}, "k")
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Class != ErrorClassTransient {
		t.Fatalf("want transient, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Complete(ctx, &ChatRequest{Model: "m"}, "k")
	if err == nil || errors.As(err, &pe) {
		t.Fatalf("cancelled ctx must not be classified: %v", err)
	}
}

func TestNoClientTimeoutBeyond15s(t *testing.T) {
	if sharedClient.Timeout != 0 {
		t.Fatalf("shared client must have no timeout, got %v", sharedClient.Timeout)
	}
}

func TestOpenRouterListModelsUsesBaseURLAndKeepsPaid(t *testing.T) {
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		fmt.Fprint(w, `{"data":[
		 {"id":"openai/gpt-4o-mini","name":"GPT-4o mini","context_length":128000,"pricing":{"prompt":"0.00000015","completion":"0.0000006"},"top_provider":{"max_completion_tokens":16384}},
		 {"id":"meta-llama/llama-3.3-70b-instruct:free","name":"Llama free","context_length":65536,"pricing":{"prompt":"0","completion":"0"},"top_provider":{}}]}`)
	}))
	defer srv.Close()
	t.Setenv("OPENROUTER_BASE_URL", srv.URL)
	p, _ := Get("openrouter")
	models, err := p.ListModels(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/models" || auth != "Bearer k" {
		t.Fatalf("path=%q auth=%q", path, auth)
	}
	if len(models) != 2 || models[0].ID != "openai/gpt-4o-mini" || models[0].InputCostPerMTok < 0.149 || models[0].InputCostPerMTok > 0.151 {
		t.Fatalf("models: %+v", models)
	}
}

func TestGroqListModelsBaseURLAndAuthError(t *testing.T) {
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if status == 200 {
			fmt.Fprint(w, `{"data":[{"id":"llama-3.3-70b-versatile","context_window":131072},{"id":"whisper-large-v3"}]}`)
		} else {
			fmt.Fprint(w, `{"error":{"message":"Invalid API Key","code":"invalid_api_key"}}`)
		}
	}))
	defer srv.Close()
	t.Setenv("GROQ_BASE_URL", srv.URL)
	p, _ := Get("groq")
	models, err := p.ListModels(context.Background(), "k")
	if err != nil || len(models) != 1 || models[0].ContextWindow != 131072 {
		t.Fatalf("%v %+v", err, models)
	}
	status = 401
	_, err = p.ListModels(context.Background(), "k")
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Class != ErrorClassAuth {
		t.Fatalf("want auth ProviderError, got %v", err)
	}
}

func TestRegistry(t *testing.T) {
	got := strings.Join(ValidProviders(), ",")
	if got != "anthropic,baseten,google,groq,meshapi,nvidia,openrouter,together" {
		t.Fatalf("ValidProviders = %s", got)
	}
	if p, err := Get("gemini"); err != nil || p.Name() != "google" {
		t.Fatalf("gemini alias: %v %v", p, err)
	}
	if _, err := Get("nope"); err == nil {
		t.Fatal("unknown provider must error")
	}
}
