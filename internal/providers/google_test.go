package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func gfix(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/gemini/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// gServer serves fixture handler h and records the last request body/path/query.
type gRec struct {
	body  map[string]any
	path  string
	query string
	auth  string
}

func gServe(t *testing.T, h func(w http.ResponseWriter, r *http.Request, n int)) (*gRec, *int) {
	t.Helper()
	rec := &gRec{}
	n := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.body = nil
		json.Unmarshal(b, &rec.body)
		rec.path, rec.query, rec.auth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		*n++
		h(w, r, *n)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GOOGLE_BASE_URL", srv.URL)
	return rec, n
}

func gJSON(name string, t *testing.T, status int) func(w http.ResponseWriter, r *http.Request, n int) {
	b := gfix(t, name)
	return func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(b)
	}
}

func gSSE(name string, t *testing.T) func(w http.ResponseWriter, r *http.Request, n int) {
	b := gfix(t, name)
	return func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(b)
	}
}

var gCallID = regexp.MustCompile(`^call_\d+$`)

func gReq() *ChatRequest {
	return &ChatRequest{Model: "gemini-2.5-flash", Messages: []ChatMessage{{Role: "user", Content: "weather?"}}}
}

func TestGoogleRequestToolsAndRoundTrip(t *testing.T) {
	rec, _ := gServe(t, gJSON("complete_text.json", t, 200))
	req := &ChatRequest{
		Model:       "gemini-2.5-flash",
		Temperature: f64(0),
		Messages: []ChatMessage{
			{Role: "user", Content: "weather in Paris and time?"},
			{Role: "assistant", Content: "", ToolCalls: []ToolCall{
				{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "get_weather", Arguments: `{"city":"Paris"}`}},
				{ID: "call_2", Function: ToolCallFunction{Name: "get_time"}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: `{"t":20}`},
			{Role: "tool", ToolCallID: "call_2", Content: ""},
		},
		Tools: []Tool{
			{Function: ToolFunction{Name: "get_weather", Parameters: map[string]any{
				"$schema": "x", "type": "object", "additionalProperties": false,
				"properties": map[string]any{"city": map[string]any{"type": "string", "additionalProperties": false}, "additionalProperties": map[string]any{"type": "string"}},
			}}},
			{Type: "function", Function: ToolFunction{Name: "get_time", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		},
		ToolChoice: "ANY",
	}
	p := &GoogleProvider{}
	if _, err := p.Complete(context.Background(), req, "k1"); err != nil {
		t.Fatal(err)
	}
	if rec.path != "/v1beta/openai/chat/completions" || rec.auth != "Bearer k1" {
		t.Fatalf("path/auth %q %q", rec.path, rec.auth)
	}
	b := rec.body
	if v, ok := b["temperature"]; !ok || v.(float64) != 0 {
		t.Fatal("temperature 0 not sent")
	}
	if b["tool_choice"] != "required" {
		t.Fatalf("tool_choice %v", b["tool_choice"])
	}
	msgs := b["messages"].([]any)
	asst := msgs[1].(map[string]any)
	if c, ok := asst["content"]; !ok || c != nil {
		t.Fatalf("assistant content must be explicit null: %v", asst)
	}
	tcs := asst["tool_calls"].([]any)
	if len(tcs) != 2 || tcs[0].(map[string]any)["id"] != "call_1" || tcs[1].(map[string]any)["type"] != "function" {
		t.Fatalf("tool_calls %v", tcs)
	}
	if tcs[1].(map[string]any)["function"].(map[string]any)["arguments"] != "{}" {
		t.Fatal("empty arguments must become {}")
	}
	t1, t2 := msgs[2].(map[string]any), msgs[3].(map[string]any)
	if t1["role"] != "tool" || t1["tool_call_id"] != "call_1" || t1["name"] != "get_weather" || t2["name"] != "get_time" {
		t.Fatalf("tool msgs %v %v", t1, t2)
	}
	if c, ok := t2["content"]; !ok || c != "" {
		t.Fatalf("empty tool content must be sent as \"\": %v", t2)
	}
	tools := b["tools"].([]any)
	f0 := tools[0].(map[string]any)
	if f0["type"] != "function" {
		t.Fatal("tool type defaulted")
	}
	params := f0["function"].(map[string]any)["parameters"].(map[string]any)
	if _, ok := params["$schema"]; ok {
		t.Fatal("$schema not stripped")
	}
	if _, ok := params["additionalProperties"]; ok {
		t.Fatal("additionalProperties not stripped")
	}
	props := params["properties"].(map[string]any)
	if _, ok := props["additionalProperties"]; !ok {
		t.Fatal("a property literally named additionalProperties must survive")
	}
	if _, ok := props["city"].(map[string]any)["additionalProperties"]; ok {
		t.Fatal("nested additionalProperties not stripped")
	}
	if _, ok := tools[1].(map[string]any)["function"].(map[string]any)["parameters"]; ok {
		t.Fatal("empty-object parameters must be dropped")
	}
	// caller's request not mutated
	if _, ok := req.Tools[0].Function.Parameters.(map[string]any)["$schema"]; !ok {
		t.Fatal("request mutated")
	}
}

func TestGoogleToolChoiceForms(t *testing.T) {
	fn := map[string]any{"type": "function", "function": map[string]any{"name": "f"}}
	cases := []struct {
		in   any
		want any
	}{
		{"auto", "auto"}, {" None ", "none"}, {"required", "required"}, {"any", "required"}, {"", nil}, {nil, nil},
		{fn, fn},
		{map[string]any{"type": "function", "name": "f"}, fn},
		{map[string]any{"type": "auto"}, "auto"},
	}
	for _, c := range cases {
		got := googleToolChoice(c.in)
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(c.want)
		if string(gb) != string(wb) {
			t.Errorf("%v -> %s want %s", c.in, gb, wb)
		}
	}
	// tool_choice without tools is dropped
	w := googleBuildReq(&ChatRequest{Model: "m", ToolChoice: "auto"}, false)
	if w.ToolChoice != nil {
		t.Fatal("tool_choice without tools must be dropped")
	}
}

func TestGoogleCompleteMissingIDsAndFinishReason(t *testing.T) {
	gServe(t, gJSON("complete_tool_noid.json", t, 200))
	resp, err := (&GoogleProvider{}).Complete(context.Background(), gReq(), "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || !gCallID.MatchString(resp.ToolCalls[0].ID) || resp.ToolCalls[0].Type != "function" {
		t.Fatalf("%+v", resp.ToolCalls)
	}
	if resp.FinishReason != "tool_calls" || resp.Content != "" || resp.Usage.TotalTokens != 15 {
		t.Fatalf("%+v", resp)
	}
}

func TestGoogleCompleteParallelEmptyIDsUniqueAndSignatureEchoed(t *testing.T) {
	rec, _ := gServe(t, gJSON("complete_parallel_emptyid.json", t, 200))
	p := &GoogleProvider{}
	resp, err := p.Complete(context.Background(), gReq(), "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("%+v", resp.ToolCalls)
	}
	a, b := resp.ToolCalls[0], resp.ToolCalls[1]
	if !gCallID.MatchString(a.ID) || !gCallID.MatchString(b.ID) || a.ID == b.ID {
		t.Fatalf("ids %q %q", a.ID, b.ID)
	}
	if a.Function.Name != "get_weather" || b.Function.Name != "get_time" || resp.FinishReason != "tool_calls" {
		t.Fatalf("%+v", resp)
	}
	// Echo the ids back: upstream receives them plus the remembered thought signature.
	gServe(t, gJSON("complete_text.json", t, 200))
	rec2 := &gRec{}
	_ = rec
	rec2, _ = gServe(t, gJSON("complete_text.json", t, 200))
	_, err = p.Complete(context.Background(), &ChatRequest{Model: "m", Messages: []ChatMessage{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: resp.ToolCalls},
		{Role: "tool", ToolCallID: a.ID, Content: "1"},
		{Role: "tool", ToolCallID: b.ID, Content: "2"},
	}}, "k")
	if err != nil {
		t.Fatal(err)
	}
	msgs := rec2.body["messages"].([]any)
	tcs := msgs[1].(map[string]any)["tool_calls"].([]any)
	if tcs[0].(map[string]any)["id"] != a.ID || tcs[1].(map[string]any)["id"] != b.ID {
		t.Fatalf("ids not echoed: %v", tcs)
	}
	ec, _ := json.Marshal(tcs[0].(map[string]any)["extra_content"])
	if !strings.Contains(string(ec), "sig-A") {
		t.Fatalf("thought signature not re-attached: %s", ec)
	}
	if _, ok := tcs[1].(map[string]any)["extra_content"]; ok {
		t.Fatal("unexpected extra_content on second call")
	}
}

func TestGoogleCompleteArgsObjectAndMissingArgs(t *testing.T) {
	gServe(t, gJSON("complete_args_object.json", t, 200))
	resp, err := (&GoogleProvider{}).Complete(context.Background(), gReq(), "k")
	if err != nil {
		t.Fatal(err)
	}
	if resp.ToolCalls[0].ID != "abc" || resp.ToolCalls[0].Function.Arguments != `{"city":"Rome"}` {
		t.Fatalf("%+v", resp.ToolCalls[0])
	}
	if resp.ToolCalls[1].Function.Arguments != "{}" {
		t.Fatalf("%+v", resp.ToolCalls[1])
	}
}

func TestGoogleCompleteKeepsUpstreamIDAndNullContent(t *testing.T) {
	gServe(t, gJSON("complete_with_id.json", t, 200))
	resp, err := (&GoogleProvider{}).Complete(context.Background(), gReq(), "k")
	if err != nil {
		t.Fatal(err)
	}
	if resp.ToolCalls[0].ID != "upstream-id-1" || resp.FinishReason != "tool_calls" {
		t.Fatalf("%+v", resp)
	}
}

// gAccumulate folds streamed chunks the way an OpenAI client does.
type gAcc struct {
	calls  map[int]*ToolCall
	order  []int
	finish string
	text   string
	usage  *Usage
	done   bool
	err    *ProviderError
	chunks []ChatChunk
}

func gFold(t *testing.T, ch <-chan ChatChunk) *gAcc {
	t.Helper()
	a := &gAcc{calls: map[int]*ToolCall{}}
	for _, c := range collect(t, ch) {
		a.chunks = append(a.chunks, c)
		a.text += c.Content
		if c.FinishReason != "" {
			a.finish = c.FinishReason
		}
		if c.Usage != nil {
			a.usage = c.Usage
		}
		if c.Done {
			a.done = true
		}
		if c.Err != nil {
			a.err = c.Err
		}
		for _, tc := range c.ToolCalls {
			if tc.Index == nil {
				t.Fatalf("streamed tool call delta without index: %+v", tc)
			}
			cur, ok := a.calls[*tc.Index]
			if !ok {
				if tc.ID == "" || tc.Function.Name == "" {
					t.Fatalf("first delta of index %d must carry id+name: %+v", *tc.Index, tc)
				}
				cur = &ToolCall{ID: tc.ID, Function: ToolCallFunction{Name: tc.Function.Name}}
				a.calls[*tc.Index] = cur
				a.order = append(a.order, *tc.Index)
			} else if tc.ID != "" || tc.Function.Name != "" {
				t.Fatalf("later delta of index %d repeats id/name: %+v", *tc.Index, tc)
			}
			cur.Function.Arguments += tc.Function.Arguments
		}
	}
	for _, i := range a.order {
		if !json.Valid([]byte(a.calls[i].Function.Arguments)) {
			t.Fatalf("call %d arguments not valid JSON: %q", i, a.calls[i].Function.Arguments)
		}
	}
	return a
}

func gStream(t *testing.T, fixture string) *gAcc {
	t.Helper()
	gServe(t, gSSE(fixture, t))
	ch, err := (&GoogleProvider{}).Stream(context.Background(), gReq(), "k")
	if err != nil {
		t.Fatal(err)
	}
	return gFold(t, ch)
}

func TestGoogleStreamWholeArgsRepeatedIndex(t *testing.T) {
	a := gStream(t, "stream_whole_args_repeated_index.sse")
	if len(a.order) != 2 {
		t.Fatalf("want 2 calls, got %d", len(a.order))
	}
	c0, c1 := a.calls[0], a.calls[1]
	if c0.Function.Name != "get_weather" || c0.Function.Arguments != `{"city":"Paris"}` || c1.Function.Name != "get_time" || c1.Function.Arguments != `{"tz":"CET"}` {
		t.Fatalf("%+v %+v", c0, c1)
	}
	if !gCallID.MatchString(c0.ID) || !gCallID.MatchString(c1.ID) || c0.ID == c1.ID {
		t.Fatalf("ids %q %q", c0.ID, c1.ID)
	}
	if a.finish != "tool_calls" || a.text != "Checking. " || a.usage == nil || a.usage.TotalTokens != 18 || !a.done || a.err != nil {
		t.Fatalf("%+v", a)
	}
}

func TestGoogleStreamMissingIndexParallelInOneChunk(t *testing.T) {
	a := gStream(t, "stream_missing_index.sse")
	if len(a.order) != 2 || a.calls[0].Function.Name != "a" || a.calls[1].Function.Name != "b" ||
		a.calls[0].Function.Arguments != `{"x":1}` || a.calls[1].Function.Arguments != `{"y":2}` || a.finish != "tool_calls" {
		t.Fatalf("%+v", a)
	}
}

func TestGoogleStreamOpenAIFragmentsAccumulate(t *testing.T) {
	a := gStream(t, "stream_openai_fragments.sse")
	if len(a.order) != 2 {
		t.Fatalf("calls %d", len(a.order))
	}
	c0 := a.calls[0]
	if c0.ID != "call_x" || c0.Function.Arguments != `{"city":"Paris"}` || a.calls[1].ID != "call_y" || a.calls[1].Function.Arguments != `{"tz":"UTC"}` {
		t.Fatalf("%+v %+v", c0, a.calls[1])
	}
	var fragments int
	for _, c := range a.chunks {
		for _, tc := range c.ToolCalls {
			if tc.Index != nil && *tc.Index == 0 && tc.Function.Arguments != "" {
				fragments++
			}
		}
	}
	if fragments != 3 {
		t.Fatalf("argument fragments for call 0 = %d, want 3 (deltas preserved)", fragments)
	}
	if a.finish != "tool_calls" || a.usage == nil || a.usage.TotalTokens != 11 {
		t.Fatalf("%+v", a)
	}
}

func TestGoogleStreamRepeatedNameFragmentsAreOneCall(t *testing.T) {
	a := gStream(t, "stream_repeated_name_fragments.sse")
	if len(a.order) != 1 || a.calls[0].Function.Arguments != `{"city":"Oslo"}` || a.finish != "tool_calls" {
		t.Fatalf("%+v", a)
	}
}

func TestGoogleStreamNoArgCallGetsEmptyObject(t *testing.T) {
	a := gStream(t, "stream_noarg_call.sse")
	if len(a.order) != 1 || a.calls[0].ID != "c1" || a.calls[0].Function.Arguments != "{}" || a.finish != "tool_calls" {
		t.Fatalf("%+v", a)
	}
}

func TestGoogleStreamRequestShape(t *testing.T) {
	rec, _ := gServe(t, gSSE("stream_noarg_call.sse", t))
	ch, err := (&GoogleProvider{}).Stream(context.Background(), gReq(), "k")
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)
	so, _ := rec.body["stream_options"].(map[string]any)
	if rec.body["stream"] != true || so["include_usage"] != true {
		t.Fatalf("%v", rec.body)
	}
}

func TestGoogleStreamArrayWrappedErrorEvent(t *testing.T) {
	a := gStream(t, "stream_error_array.sse")
	if a.err == nil || a.err.Class != ErrorClassRateLimit || a.err.RetryAfter != 7*time.Second {
		t.Fatalf("%+v", a.err)
	}
	if a.text != "hi" {
		t.Fatalf("text %q", a.text)
	}
}

func TestGoogleStreamPrematureEOFAndCancel(t *testing.T) {
	gServe(t, func(w http.ResponseWriter, r *http.Request, n int) {
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
	})
	ch, err := (&GoogleProvider{}).Stream(context.Background(), gReq(), "k")
	if err != nil {
		t.Fatal(err)
	}
	a := gFold(t, ch)
	if a.err == nil || a.err.Class != ErrorClassTransient || a.err.Code != "stream_interrupted" {
		t.Fatalf("%+v", a.err)
	}
}

func TestGoogleCancelledContext(t *testing.T) {
	gServe(t, func(w http.ResponseWriter, r *http.Request, n int) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &GoogleProvider{}
	if _, err := p.Complete(ctx, gReq(), "k"); err != context.Canceled {
		t.Fatalf("complete: %v", err)
	}
	if _, err := p.Stream(ctx, gReq(), "k"); err != context.Canceled {
		t.Fatalf("stream: %v", err)
	}
	if _, err := p.ListModels(ctx, "k"); err != context.Canceled {
		t.Fatalf("list: %v", err)
	}
}

func TestGoogleClassificationTable(t *testing.T) {
	cases := []struct {
		fixture string
		status  int
		hdr     string
		class   ErrorClass
		code    string
		retry   time.Duration
	}{
		{"error_429_array.json", 429, "", ErrorClassRateLimit, "RESOURCE_EXHAUSTED", 4 * time.Second},  // 3.5s ceil
		{"error_429_array.json", 429, "9", ErrorClassRateLimit, "RESOURCE_EXHAUSTED", 9 * time.Second}, // header wins
		{"error_429_prepay.json", 429, "", ErrorClassInsufficientCredit, "RESOURCE_EXHAUSTED", 0},
		{"error_400_apikey.json", 400, "", ErrorClassAuth, "API_KEY_INVALID", 0},
		{"error_403.json", 403, "", ErrorClassAuth, "PERMISSION_DENIED", 0},
		{"error_400_billing.json", 400, "", ErrorClassInsufficientCredit, "FAILED_PRECONDITION", 0},
		{"error_404.json", 404, "", ErrorClassModelUnavailable, "NOT_FOUND", 0},
		{"error_500.json", 500, "", ErrorClassTransient, "INTERNAL", 0},
		{"error_503.json", 503, "", ErrorClassTransient, "UNAVAILABLE", 0},
		{"error_400_invalid_argument.json", 400, "", ErrorClassBadRequest, "INVALID_ARGUMENT", 0},
	}
	for _, c := range cases {
		t.Run(c.fixture+"/"+c.hdr, func(t *testing.T) {
			gServe(t, func(w http.ResponseWriter, r *http.Request, n int) {
				if c.hdr != "" {
					w.Header().Set("Retry-After", c.hdr)
				}
				w.WriteHeader(c.status)
				w.Write(gfix(t, c.fixture))
			})
			p := &GoogleProvider{}
			_, err := p.Complete(context.Background(), gReq(), "k")
			pe, ok := err.(*ProviderError)
			if !ok {
				t.Fatalf("err %v", err)
			}
			if pe.Class != c.class || pe.Code != c.code || pe.HTTP != c.status || pe.RetryAfter != c.retry {
				t.Fatalf("got class=%s code=%s http=%d retry=%v", pe.Class, pe.Code, pe.HTTP, pe.RetryAfter)
			}
			if pe.Message == "" || strings.Contains(pe.Error(), pe.Message) {
				t.Fatalf("Error() must not contain Message: %q / %q", pe.Error(), pe.Message)
			}
			// same through the streaming open path
			if _, err := p.Stream(context.Background(), gReq(), "k"); err.(*ProviderError).Class != c.class {
				t.Fatalf("stream class %v", err)
			}
		})
	}
}

func TestGoogleClassifyDefaultsAndBodies(t *testing.T) {
	p := &GoogleProvider{}
	// 429 without retryDelay and header -> default cooldown
	pe := p.Classify(429, nil, []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"q"}}`))
	if pe.Class != ErrorClassRateLimit || pe.RetryAfter != DefaultRateLimitCooldown() {
		t.Fatalf("%+v", pe)
	}
	// non-JSON body
	pe = p.Classify(502, nil, []byte("<html>bad gateway</html>"))
	if pe.Class != ErrorClassTransient {
		t.Fatalf("%+v", pe)
	}
	// error inside a 200 body
	pe = p.Classify(200, nil, []byte(`[{"error":{"code":503,"status":"UNAVAILABLE","message":"x"}}]`))
	if pe.Class != ErrorClassTransient || pe.HTTP != 503 {
		t.Fatalf("%+v", pe)
	}
	// retryDelay clamped to the minimum
	pe = p.Classify(429, nil, []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"q","details":[{"@type":"x.RetryInfo","retryDelay":"0.2s"}]}}`))
	if pe.RetryAfter != time.Second {
		t.Fatalf("%+v", pe)
	}
	// RESOURCE_EXHAUSTED with the usual boilerplate "billing details" stays a rate limit
	pe = p.Classify(429, nil, gfix(t, "error_429_array.json"))
	if pe.Class != ErrorClassRateLimit {
		t.Fatalf("%+v", pe)
	}
}

func TestGoogleErrorOn200Complete(t *testing.T) {
	gServe(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Write(gfix(t, "error_429_array.json"))
	})
	_, err := (&GoogleProvider{}).Complete(context.Background(), gReq(), "k")
	if pe, ok := err.(*ProviderError); !ok || pe.Class != ErrorClassRateLimit {
		t.Fatalf("%v", err)
	}
}

func TestGoogleListModelsPaginationAndMethods(t *testing.T) {
	pages := map[string]string{"": "models_page1.json", "tok2": "models_page2.json", "tok3": "models_page3.json"}
	var queries []string
	rec, n := gServe(t, func(w http.ResponseWriter, r *http.Request, i int) {
		queries = append(queries, r.URL.RawQuery)
		w.Write(gfix(t, pages[r.URL.Query().Get("pageToken")]))
	})
	models, err := (&GoogleProvider{}).ListModels(context.Background(), "key&1")
	if err != nil {
		t.Fatal(err)
	}
	if *n != 3 || rec.path != "/v1beta/models" {
		t.Fatalf("requests=%d path=%s", *n, rec.path)
	}
	if !strings.Contains(queries[0], "key=key%261") || !strings.Contains(queries[1], "pageToken=tok2") || !strings.Contains(queries[2], "pageToken=tok3") {
		t.Fatalf("queries %v", queries)
	}
	if len(models) != 10 {
		t.Fatalf("want all 10 models, got %d", len(models))
	}
	byID := map[string]ModelInfo{}
	for _, m := range models {
		if strings.HasPrefix(m.ID, "models/") {
			t.Fatalf("prefix not stripped: %s", m.ID)
		}
		if _, ok := m.Extra["methods"].([]string); !ok {
			t.Fatalf("%s: methods missing/typed wrong: %#v", m.ID, m.Extra["methods"])
		}
		byID[m.ID] = m
	}
	f := byID["gemini-2.5-flash"]
	if got := f.Extra["methods"].([]string); len(got) != 3 || got[0] != "generateContent" {
		t.Fatalf("%v", got)
	}
	if f.ContextWindow != 1048576 || f.MaxOutputTokens != 65536 || f.DisplayName != "Gemini 2.5 Flash" || !f.SupportsStreaming {
		t.Fatalf("%+v", f)
	}
	for _, id := range []string{"gemini-2.5-flash-image", "gemini-2.5-flash-preview-tts", "lyria-realtime-exp", "gemini-2.5-computer-use-preview-10-2025", "gemini-robotics-er-1.5-preview", "text-embedding-004", "gemini-2.5-flash-native-audio-preview"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("missing %s", id)
		}
	}
	if m := byID["text-embedding-004"]; m.Extra["methods"].([]string)[0] != "embedContent" {
		t.Fatal("embedding methods")
	}
	if m := byID["no-methods-model"]; len(m.Extra["methods"].([]string)) != 0 {
		t.Fatal("no-methods model must have empty methods")
	}
	if byID["gemini-2.0-flash"].InputCostPerMTok != 0.10 {
		t.Fatal("pricing kept")
	}
}

func TestGoogleListModelsPaginationLoopGuard(t *testing.T) {
	_, n := gServe(t, func(w http.ResponseWriter, r *http.Request, i int) {
		io.WriteString(w, `{"models":[{"name":"models/a"}],"nextPageToken":"same"}`)
	})
	models, err := (&GoogleProvider{}).ListModels(context.Background(), "k")
	if err != nil || *n > 3 || len(models) == 0 {
		t.Fatalf("n=%d err=%v", *n, err)
	}
}

func TestGoogleListModelsErrorClassified(t *testing.T) {
	gServe(t, gJSON("error_400_apikey.json", t, 400))
	_, err := (&GoogleProvider{}).ListModels(context.Background(), "bad")
	if pe, ok := err.(*ProviderError); !ok || pe.Class != ErrorClassAuth {
		t.Fatalf("%v", err)
	}
}

func TestGoogleHealthCheckViaListing(t *testing.T) {
	rec, _ := gServe(t, gJSON("models_page3.json", t, 200))
	h, err := (&GoogleProvider{}).HealthCheck(context.Background(), "k")
	if err != nil || !h.OK || rec.path != "/v1beta/models" || !strings.Contains(rec.query, "pageSize=1") {
		t.Fatalf("%+v %v %s %s", h, err, rec.path, rec.query)
	}
	gServe(t, gJSON("error_403.json", t, 403))
	h, _ = (&GoogleProvider{}).HealthCheck(context.Background(), "k")
	if h.OK || h.Error != string(ErrorClassAuth) {
		t.Fatalf("%+v", h)
	}
}

func TestGoogleDefaultBaseURLAndRegistry(t *testing.T) {
	t.Setenv("GOOGLE_BASE_URL", "")
	p := &GoogleProvider{}
	if p.openAICompatURL() != "https://generativelanguage.googleapis.com/v1beta/openai" {
		t.Fatal(p.openAICompatURL())
	}
	g, err := Get("gemini")
	if err != nil || g.Name() != "google" {
		t.Fatalf("%v", err)
	}
	var _ Classifier = p
}
