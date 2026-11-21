package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func anthServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL+"/")
	return srv
}

func anthProvider(t *testing.T) Provider {
	t.Helper()
	p, err := Get("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAnthropicRegistered(t *testing.T) {
	if anthProvider(t).Name() != "anthropic" {
		t.Fatal("name")
	}
	if _, ok := anthProvider(t).(Classifier); !ok {
		t.Fatal("must implement Classifier")
	}
}

func TestAnthropicCompletePlainChatAndHeaders(t *testing.T) {
	var gotPath, gotKey, gotVer, gotCT string
	var body map[string]any
	anthServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey, gotVer, gotCT = r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("anthropic-version"), r.Header.Get("Content-Type")
		if r.Header.Get("Authorization") != "" {
			t.Error("must not send Authorization")
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.Write(anthFixture(t, "chat_response.json"))
	})
	resp, err := anthProvider(t).Complete(context.Background(), &ChatRequest{Model: "claude-sonnet-4-5", Messages: []ChatMessage{{Role: "user", Content: "hi"}}, Temperature: f64(0)}, "sk-ant-test")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/messages" || gotKey != "sk-ant-test" || gotVer != "2023-06-01" || gotCT != "application/json" {
		t.Fatalf("path=%s key=%s ver=%s ct=%s", gotPath, gotKey, gotVer, gotCT)
	}
	if body["temperature"].(float64) != 0 || body["max_tokens"].(float64) != 4096 {
		t.Fatalf("body %v", body)
	}
	if resp.Content != "Hello there!" || resp.FinishReason != "stop" || resp.ID != "msg_01Chat" || resp.Usage.TotalTokens != 17 || len(resp.ToolCalls) != 0 {
		t.Fatalf("%+v", resp)
	}
}

func TestAnthropicToolCallRoundTripUpstreamJSON(t *testing.T) {
	var sent []byte
	anthServer(t, func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Write(anthFixture(t, "tool_use_response.json"))
	})
	resp, err := anthProvider(t).Complete(context.Background(), roundTripRequest(), "k")
	if err != nil {
		t.Fatal(err)
	}
	want := string(trimSpace(anthFixture(t, "request_roundtrip.golden.json")))
	if string(sent) != want {
		t.Fatalf("upstream body\n got: %s\nwant: %s", sent, want)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "toolu_01A" || resp.FinishReason != "tool_calls" || resp.Content != "Checking the weather." {
		t.Fatalf("%+v", resp)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(resp.ToolCalls[0].Function.Arguments), &args); err != nil || args["city"] != "Paris" {
		t.Fatalf("args %q %v", resp.ToolCalls[0].Function.Arguments, err)
	}
}

func TestAnthropicParallelToolCallsResponse(t *testing.T) {
	anthServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write(anthFixture(t, "parallel_tool_response.json")) })
	resp, err := anthProvider(t).Complete(context.Background(), &ChatRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "x"}}, Tools: weatherTools()}, "k")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "" || resp.FinishReason != "tool_calls" || len(resp.ToolCalls) != 2 {
		t.Fatalf("%+v", resp)
	}
	if resp.ToolCalls[0].ID != "toolu_P1" || resp.ToolCalls[1].ID != "toolu_P2" || resp.ToolCalls[1].Function.Arguments != "{}" {
		t.Fatalf("%+v", resp.ToolCalls)
	}
}

func TestAnthropicBadToolArgsNoUpstreamCall(t *testing.T) {
	called := false
	anthServer(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	_, err := anthProvider(t).Complete(context.Background(), &ChatRequest{Model: "m", Messages: []ChatMessage{
		{Role: "user", Content: "x"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "a", Type: "function", Function: ToolCallFunction{Name: "f", Arguments: "oops"}}}},
	}}, "k")
	pe, ok := err.(*ProviderError)
	if !ok || pe.Class != ErrorClassBadRequest || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestAnthropicClassifyCases(t *testing.T) {
	now := time.Now()
	reset := now.Add(90 * time.Second).UTC().Format(time.RFC3339)
	body := func(typ, msg string) []byte {
		return []byte(`{"type":"error","error":{"type":"` + typ + `","message":"` + msg + `"}}`)
	}
	a := &AnthropicProvider{}
	cases := []struct {
		name   string
		status int
		h      http.Header
		body   []byte
		want   ErrorClass
		minRA  time.Duration
		maxRA  time.Duration
	}{
		{"429 retry-after", 429, hdr("Retry-After", "42"), body("rate_limit_error", "slow"), ErrorClassRateLimit, 42 * time.Second, 42 * time.Second},
		{"429 reset header", 429, hdr("Anthropic-Ratelimit-Tokens-Reset", reset, "Anthropic-Ratelimit-Tokens-Remaining", "0"), body("rate_limit_error", "slow"), ErrorClassRateLimit, 60 * time.Second, 95 * time.Second},
		{"429 default", 429, nil, body("rate_limit_error", "slow"), ErrorClassRateLimit, time.Second, time.Hour},
		{"529 overloaded", 529, nil, body("overloaded_error", "Overloaded"), ErrorClassTransient, 0, 0},
		{"500 api_error", 500, nil, body("api_error", "boom"), ErrorClassTransient, 0, 0},
		{"401", 401, nil, body("authentication_error", "bad key"), ErrorClassAuth, 0, 0},
		{"403", 403, nil, body("permission_error", "nope"), ErrorClassAuth, 0, 0},
		{"credit", 400, nil, anthFixture(t, "error_credit.json"), ErrorClassInsufficientCredit, 0, 0},
		{"404", 404, nil, body("not_found_error", "model: x"), ErrorClassModelUnavailable, 0, 0},
		{"413", 413, nil, body("request_too_large", "big"), ErrorClassBadRequest, 0, 0},
		{"400 invalid", 400, nil, body("invalid_request_error", "messages: bad"), ErrorClassBadRequest, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pe := a.Classify(c.status, c.h, c.body)
			if pe.Class != c.want {
				t.Fatalf("class %s want %s (%+v)", pe.Class, c.want, pe)
			}
			if c.maxRA > 0 && (pe.RetryAfter < c.minRA || pe.RetryAfter > c.maxRA) {
				t.Fatalf("retryAfter %v not in [%v,%v]", pe.RetryAfter, c.minRA, c.maxRA)
			}
			if strings.Contains(pe.Error(), "credit balance") || strings.Contains(pe.Error(), "bad key") {
				t.Fatalf("Error() leaks message: %s", pe.Error())
			}
		})
	}
}

func TestAnthropicClassifyTruncatesMessage(t *testing.T) {
	long := strings.Repeat("x", 5000)
	pe := (&AnthropicProvider{}).Classify(400, nil, []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"`+long+`"}}`))
	if len(pe.Message) > 1000 || len(pe.Code) > 200 {
		t.Fatalf("unbounded: msg=%d code=%d", len(pe.Message), len(pe.Code))
	}
}

func TestAnthropicHTTPErrorClassifiedEndToEnd(t *testing.T) {
	anthServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write(anthFixture(t, "error_credit.json"))
	})
	_, err := anthProvider(t).Complete(context.Background(), &ChatRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "x"}}}, "k")
	pe, ok := err.(*ProviderError)
	if !ok || pe.Class != ErrorClassInsufficientCredit || pe.HTTP != 400 {
		t.Fatalf("%v", err)
	}
}

func TestAnthropicListModelsPagination(t *testing.T) {
	var afters []string
	var key, ver string
	anthServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path %s", r.URL.Path)
		}
		key, ver = r.Header.Get("x-api-key"), r.Header.Get("anthropic-version")
		after := r.URL.Query().Get("after_id")
		afters = append(afters, after)
		if r.URL.Query().Get("limit") != "1000" {
			t.Errorf("limit %q", r.URL.Query().Get("limit"))
		}
		if after == "" {
			w.Write(anthFixture(t, "models_page1.json"))
		} else {
			w.Write(anthFixture(t, "models_page2.json"))
		}
	})
	ms, err := anthProvider(t).ListModels(context.Background(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 || ms[2].ID != "claude-opus-4-1" || ms[2].ContextWindow != 200000 || ms[2].MaxOutputTokens != 32000 || ms[0].DisplayName != "Claude Sonnet 4.5" {
		t.Fatalf("%+v", ms)
	}
	if len(afters) != 2 || afters[0] != "" || afters[1] != "claude-haiku-4-5" || key != "k1" || ver != "2023-06-01" {
		t.Fatalf("afters=%v key=%s ver=%s", afters, key, ver)
	}
}

func TestAnthropicListModelsAuthError(t *testing.T) {
	anthServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	})
	_, err := anthProvider(t).ListModels(context.Background(), "bad")
	if pe, ok := err.(*ProviderError); !ok || pe.Class != ErrorClassAuth {
		t.Fatalf("%v", err)
	}
}

func TestAnthropicHealthCheck(t *testing.T) {
	var body map[string]any
	anthServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.Header().Set("Anthropic-Ratelimit-Requests-Limit", "50")
		w.Header().Set("Anthropic-Ratelimit-Requests-Remaining", "49")
		w.Write(anthFixture(t, "chat_response.json"))
	})
	res, err := anthProvider(t).HealthCheck(context.Background(), "k")
	if err != nil || !res.OK || body["max_tokens"].(float64) != 1 {
		t.Fatalf("%v %+v %v", err, res, body)
	}
	if res.RateLimit == nil || res.RateLimit.LimitRPM != 50 || res.RateLimit.RemainingRPM != 49 {
		t.Fatalf("%+v", res.RateLimit)
	}
}

func TestAnthropicHealthCheckFailure(t *testing.T) {
	anthServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401); w.Write([]byte(`{}`)) })
	res, err := anthProvider(t).HealthCheck(context.Background(), "k")
	if err != nil || res.OK || res.Error != "auth" {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestAnthropicCancelledContext(t *testing.T) {
	anthServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := anthProvider(t).Complete(ctx, &ChatRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "x"}}}, "k")
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("err=%v elapsed=%v", err, time.Since(start))
	}
	if pe, ok := err.(*ProviderError); ok && pe.Class == ErrorClassTransient {
		// a cancelled caller context must not be reported as a provider failure
		t.Fatalf("cancel classified transient: %v", err)
	}
}
