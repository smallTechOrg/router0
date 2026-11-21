package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func anthSSE(t *testing.T, fixture string) {
	anthServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" || r.Header.Get("anthropic-version") == "" {
			t.Error("missing headers")
		}
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"stream":true`) {
			t.Errorf("stream flag missing: %s", b)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(anthFixture(t, fixture))
	})
}

func anthStream(t *testing.T) []ChatChunk {
	ch, err := anthProvider(t).Stream(context.Background(), &ChatRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "x"}}, Tools: weatherTools()}, "k")
	if err != nil {
		t.Fatal(err)
	}
	return collect(t, ch)
}

func TestAnthropicStreamText(t *testing.T) {
	anthSSE(t, "stream_text.sse")
	chunks := anthStream(t)
	var text, finish string
	var usage *Usage
	done := false
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
		text += c.Content
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
		if c.Usage != nil {
			usage = c.Usage
		}
		done = done || c.Done
	}
	if text != "Hello!" || finish != "stop" || !done {
		t.Fatalf("text=%q finish=%q done=%v", text, finish, done)
	}
	if usage == nil || usage.PromptTokens != 25 || usage.CompletionTokens != 7 || usage.TotalTokens != 32 {
		t.Fatalf("usage %+v", usage)
	}
	if chunks[0].ID != "msg_S1" {
		t.Fatalf("first chunk %+v", chunks[0])
	}
}

func TestAnthropicStreamToolCalls(t *testing.T) {
	anthSSE(t, "stream_tool.sse")
	chunks := anthStream(t)
	args := map[int]string{}
	ids := map[int]string{}
	names := map[int]string{}
	var text, finish string
	var usage *Usage
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
		text += c.Content
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
		if c.Usage != nil {
			usage = c.Usage
		}
		for _, tc := range c.ToolCalls {
			if tc.Index == nil {
				t.Fatalf("missing index: %+v", tc)
			}
			i := *tc.Index
			if tc.ID != "" {
				if _, dup := ids[i]; dup {
					t.Fatalf("id repeated for index %d", i)
				}
				ids[i], names[i] = tc.ID, tc.Function.Name
			}
			args[i] += tc.Function.Arguments
		}
	}
	if text != "Let me check." || finish != "tool_calls" {
		t.Fatalf("text=%q finish=%q", text, finish)
	}
	if ids[0] != "toolu_S1" || names[0] != "get_weather" || ids[1] != "toolu_S2" || names[1] != "get_time" || len(ids) != 2 {
		t.Fatalf("ids=%v names=%v", ids, names)
	}
	var a0 map[string]string
	if err := json.Unmarshal([]byte(args[0]), &a0); err != nil || a0["city"] != "Paris" || a0["unit"] != "c" {
		t.Fatalf("args0 %q %v", args[0], err)
	}
	if args[1] != "{}" {
		t.Fatalf("args1 %q", args[1])
	}
	if usage == nil || usage.PromptTokens != 60 || usage.CompletionTokens != 42 {
		t.Fatalf("usage %+v", usage)
	}
	if !chunks[len(chunks)-1].Done {
		t.Fatal("last chunk not Done")
	}
}

func TestAnthropicStreamInterrupted(t *testing.T) {
	anthSSE(t, "stream_interrupted.sse")
	chunks := anthStream(t)
	last := chunks[len(chunks)-1]
	if last.Err == nil || last.Err.Class != ErrorClassTransient || last.Err.Code != "stream_interrupted" {
		t.Fatalf("%+v", last)
	}
	for _, c := range chunks {
		if c.Done {
			t.Fatal("Done must not be sent on interrupted stream")
		}
	}
}

func TestAnthropicStreamErrorEvent(t *testing.T) {
	anthSSE(t, "stream_error.sse")
	chunks := anthStream(t)
	last := chunks[len(chunks)-1]
	if last.Err == nil || last.Err.Class != ErrorClassTransient || last.Err.Code != "overloaded_error" {
		t.Fatalf("%+v", last.Err)
	}
}

func TestAnthropicStreamHTTPErrorBeforeStart(t *testing.T) {
	anthServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Header().Set("Retry-After", "7")
		w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"x"}}`))
	})
	_, err := anthProvider(t).Stream(context.Background(), &ChatRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "x"}}}, "k")
	if pe, ok := err.(*ProviderError); !ok || pe.Class != ErrorClassRateLimit {
		t.Fatalf("%v", err)
	}
}

func TestAnthropicStreamCancel(t *testing.T) {
	anthServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"x\",\"usage\":{\"input_tokens\":1}}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := anthProvider(t).Stream(ctx, &ChatRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "x"}}}, "k")
	if err != nil {
		t.Fatal(err)
	}
	<-ch
	cancel()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("channel not closed after cancel")
		}
	}
}
