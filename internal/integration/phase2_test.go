//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zer0-router/zer0/internal/testutil/agentloop"
	"github.com/zer0-router/zer0/internal/testutil/mockprovider"
)

// ---- fixtures ----

type keySpec struct{ provider, key, label string }

// p2fixture registers a tenant with the given keys (in order, so the default
// provider priority follows it), waits until each provider's catalog is warm
// and returns a service token.
func p2fixture(t *testing.T, specs ...keySpec) (*env, *tenant, string) {
	t.Helper()
	e := newEnv(t)
	tn := e.newTenant("p2")
	want := map[string][]string{}
	for _, s := range specs {
		tn.addKey(s.provider, s.key, s.label)
	}
	seen := map[string]bool{}
	for _, s := range specs {
		if seen[s.provider] {
			continue
		}
		seen[s.provider] = true
		c := map[string]string{"anthropic": "claude-sonnet-5-5", "google": "gemini-2.5-flash", "meshapi": "mistral-7b"}[s.provider]
		if c == "" {
			c = "llama-3.3-70b"
		}
		want[c] = append(want[c], s.provider)
	}
	for c, provs := range want {
		tn.waitCatalog(c, provs...)
	}
	tok, _ := tn.token("svc")
	return e, tn, tok
}

type flavour struct{ name, provider, key, model string }

var flavours = []flavour{
	{"groq", "groq", "mock-ok-g", "llama-3.3-70b"},
	{"openrouter", "openrouter", "mock-ok-or", "llama-3.3-70b"},
	{"anthropic", "anthropic", "mock-ok-anth", "claude-sonnet-5-5"},
	{"gemini", "google", "mock-ok-gem", "gemini-2.5-flash"},
	{"meshapi", "meshapi", "rsk_mock-ok-mesh", "mistral-7b"},
}

func (e *env) mockFor(provider string) *mockprovider.Server {
	switch provider {
	case "groq":
		return e.groq
	case "openrouter":
		return e.or
	case "anthropic":
		return e.anth
	case "google":
		return e.gem
	case "meshapi":
		return e.mesh
	}
	e.t.Fatalf("no mock for %s", provider)
	return nil
}

func (f flavour) fixture(t *testing.T) (*env, *tenant, string) {
	return p2fixture(t, keySpec{f.provider, f.key, f.provider + "-a"})
}

func (e *env) loop(tok, model, scenario string, stream bool) *agentloop.Result {
	e.t.Helper()
	return agentloop.Run(e.t, agentloop.Config{BaseURL: e.ts.URL, Token: tok, Model: model,
		Marker: mockprovider.ScenarioMarker(scenario), Stream: stream})
}

var scenarios = []string{"single", "parallel", "chain"}

// roundsOf renders loop rounds as name:args (args re-marshalled with sorted keys).
func roundsOf(r *agentloop.Result) [][]string {
	var out [][]string
	for _, round := range r.Rounds {
		var cs []string
		for _, c := range round {
			b, _ := json.Marshal(c.Args)
			cs = append(cs, c.Name+":"+string(b))
		}
		out = append(out, cs)
	}
	return out
}

func wantRounds(scenario string) [][]string {
	var out [][]string
	for _, round := range mockprovider.ScenarioRounds(scenario) {
		var cs []string
		for _, c := range round {
			var a map[string]any
			_ = json.Unmarshal([]byte(c.Args), &a)
			b, _ := json.Marshal(a)
			cs = append(cs, c.Name+":"+string(b))
		}
		out = append(out, cs)
	}
	return out
}

func checkLoop(t *testing.T, label, scenario, provider string, r *agentloop.Result) {
	t.Helper()
	if got, want := roundsOf(r), wantRounds(scenario); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s/%s: tool rounds = %v, want %v", label, scenario, got, want)
	}
	if r.FinalText != mockprovider.ToolFinalText {
		t.Fatalf("%s/%s: final text = %q", label, scenario, r.FinalText)
	}
	if got := r.Header.Get("X-Router0-Provider"); got != provider {
		t.Fatalf("%s/%s: served by %q, want %q", label, scenario, got, provider)
	}
	ids := map[string]bool{}
	for _, round := range r.Rounds {
		for _, c := range round {
			if ids[c.ID] {
				t.Fatalf("%s/%s: duplicate tool call id %q", label, scenario, c.ID)
			}
			ids[c.ID] = true
		}
	}
}

// ---- raw tool-call requests ----

func toolsJSON() []J {
	return []J{{"type": "function", "function": J{"name": "get_weather", "description": "Get weather",
		"parameters": J{"type": "object", "properties": J{"city": J{"type": "string"}}, "required": []string{"city"}}}}}
}

func (tn *tenant) toolChat(tok, model, scenario string, stream bool, extra J) resp {
	tn.e.t.Helper()
	body := J{"model": model, "tools": toolsJSON(), "tool_choice": "auto",
		"messages": []J{{"role": "user", "content": "weather? " + mockprovider.ScenarioMarker(scenario)}}}
	if stream {
		body["stream"] = true
		body["stream_options"] = J{"include_usage": true}
	}
	for k, v := range extra {
		body[k] = v
	}
	return tn.e.do("POST", "/v1/chat/completions", tok, body, nil)
}

type accCall struct {
	id, name, args string
	deltas         int
}

// accumulate folds the streamed tool_calls deltas by index and returns the
// finish reasons seen.
func accumulate(t *testing.T, s sseResult) (map[int]*accCall, []string) {
	t.Helper()
	calls := map[int]*accCall{}
	var finish []string
	for _, c := range s.chunks {
		for _, ch := range list(c["choices"]) {
			if f := str(dig(ch, "finish_reason")); f != "" {
				finish = append(finish, f)
			}
			for _, tc := range list(dig(ch, "delta", "tool_calls")) {
				idx, ok := dig(tc, "index").(float64)
				if !ok {
					t.Fatalf("tool_calls delta without index: %v", tc)
				}
				a := calls[int(idx)]
				if a == nil {
					a = &accCall{}
					calls[int(idx)] = a
					if str(dig(tc, "id")) == "" || str(dig(tc, "function", "name")) == "" || str(dig(tc, "type")) != "function" {
						t.Fatalf("first delta of call %d lacks id/name/type: %v", int(idx), tc)
					}
				}
				if id := str(dig(tc, "id")); id != "" {
					a.id = id
				}
				if n := str(dig(tc, "function", "name")); n != "" {
					a.name = n
				}
				a.args += str(dig(tc, "function", "arguments"))
				a.deltas++
			}
		}
	}
	return calls, finish
}

func assertStreamCalls(t *testing.T, r resp, scenario string) map[int]*accCall {
	t.Helper()
	assertStatus(t, r, 200)
	s := parseSSE(r.body)
	if !s.done || len(s.errors) != 0 {
		t.Fatalf("stream not clean: %s", r.body)
	}
	calls, finish := accumulate(t, s)
	want := mockprovider.ScenarioRounds(scenario)[0]
	if len(calls) != len(want) {
		t.Fatalf("accumulated %d tool calls, want %d: %s", len(calls), len(want), r.body)
	}
	ids := map[string]bool{}
	for i, w := range want {
		a := calls[i]
		if a == nil || a.name != w.Name || a.id == "" || ids[a.id] {
			t.Fatalf("call %d = %+v, want name %s with a unique id", i, a, w.Name)
		}
		ids[a.id] = true
		var got, exp map[string]any
		if json.Unmarshal([]byte(a.args), &got) != nil {
			t.Fatalf("call %d arguments are not JSON: %q", i, a.args)
		}
		_ = json.Unmarshal([]byte(w.Args), &exp)
		if !reflect.DeepEqual(got, exp) {
			t.Fatalf("call %d arguments %v, want %v", i, got, exp)
		}
	}
	if len(finish) == 0 || finish[len(finish)-1] != "tool_calls" {
		t.Fatalf("finish reasons %v, want final tool_calls", finish)
	}
	return calls
}

func assertToolResponse(t *testing.T, r resp, scenario string) []any {
	t.Helper()
	assertStatus(t, r, 200)
	j := r.json()
	if str(dig(j, "choices", 0, "finish_reason")) != "tool_calls" {
		t.Fatalf("finish_reason: %s", r)
	}
	tcs := list(dig(j, "choices", 0, "message", "tool_calls"))
	want := mockprovider.ScenarioRounds(scenario)[0]
	if len(tcs) != len(want) {
		t.Fatalf("tool_calls: %s", r)
	}
	ids := map[string]bool{}
	for i, w := range want {
		id := str(dig(tcs[i], "id"))
		if id == "" || ids[id] || str(dig(tcs[i], "type")) != "function" || str(dig(tcs[i], "function", "name")) != w.Name {
			t.Fatalf("tool_calls[%d]: %v", i, tcs[i])
		}
		ids[id] = true
		var got, exp map[string]any
		if json.Unmarshal([]byte(str(dig(tcs[i], "function", "arguments"))), &got) != nil {
			t.Fatalf("arguments not JSON: %v", tcs[i])
		}
		_ = json.Unmarshal([]byte(w.Args), &exp)
		if !reflect.DeepEqual(got, exp) {
			t.Fatalf("arguments %v, want %v", got, exp)
		}
	}
	if dig(j, "choices", 0, "message", "content") != nil {
		t.Fatalf("message.content should be null for a pure tool-call turn: %s", r)
	}
	return tcs
}

// lastBody returns the JSON body of the last chat request the mock received.
func lastBody(t *testing.T, m *mockprovider.Server) J {
	t.Helper()
	calls := m.Calls()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Path == "/chat/completions" {
			var j J
			if err := json.Unmarshal(calls[i].Body, &j); err != nil {
				t.Fatal(err)
			}
			return j
		}
	}
	t.Fatal("mock received no chat request")
	return nil
}

// ---- tool calling ----

func TestPhase2_AgentLoopToolCalling(t *testing.T) {
	for _, f := range flavours {
		t.Run(f.name, func(t *testing.T) {
			e, _, tok := f.fixture(t)
			for _, sc := range scenarios {
				checkLoop(t, f.name, sc, f.provider, e.loop(tok, f.model, sc, false))
			}
		})
	}
}

func TestPhase2_AgentLoopToolCallingStream(t *testing.T) {
	for _, f := range flavours {
		t.Run(f.name, func(t *testing.T) {
			e, _, tok := f.fixture(t)
			for _, sc := range scenarios {
				stream := e.loop(tok, f.model, sc, true)
				checkLoop(t, f.name+"-stream", sc, f.provider, stream)
				// the accumulated stream equals the non-stream run
				plain := e.loop(tok, f.model, sc, false)
				if !reflect.DeepEqual(roundsOf(stream), roundsOf(plain)) || stream.FinalText != plain.FinalText {
					t.Fatalf("%s/%s: stream %v %q != non-stream %v %q", f.name, sc, roundsOf(stream), stream.FinalText, roundsOf(plain), plain.FinalText)
				}
			}
		})
	}
}

func TestPhase2_AnthropicToolCalling(t *testing.T) {
	e, tn, tok := p2fixture(t, keySpec{"anthropic", "mock-ok-anth", "anth-a"})
	for _, sc := range scenarios {
		assertToolResponse(t, tn.toolChat(tok, "claude-sonnet-5-5", sc, false, nil), sc)
	}
	// forced tool_choice and tools are translated to the Messages API shape
	r := tn.toolChat(tok, "claude-sonnet-5-5", "single", false, J{"tool_choice": J{"type": "function", "function": J{"name": "get_weather"}}})
	assertToolResponse(t, r, "single")
	b := lastBody(t, e.anth)
	if str(dig(b, "tool_choice", "type")) != "tool" || str(dig(b, "tool_choice", "name")) != "get_weather" ||
		str(dig(b, "tools", 0, "name")) != "get_weather" || str(dig(b, "tools", 0, "input_schema", "type")) != "object" {
		t.Fatalf("anthropic request tools/tool_choice: %v", b)
	}
	// a full chain: the final request carries tool_use / tool_result blocks with matching ids
	res := e.loop(tok, "claude-sonnet-5-5", "chain", false)
	checkLoop(t, "anthropic", "chain", "anthropic", res)
	b = lastBody(t, e.anth)
	msgs := list(b["messages"])
	n := len(msgs)
	if n != 5 || str(dig(msgs[n-1], "role")) != "user" || str(dig(msgs[n-1], "content", 0, "type")) != "tool_result" ||
		str(dig(msgs[n-2], "role")) != "assistant" || str(dig(msgs[n-2], "content", 0, "type")) != "tool_use" ||
		str(dig(msgs[n-1], "content", 0, "tool_use_id")) != str(dig(msgs[n-2], "content", 0, "id")) ||
		str(dig(msgs[n-2], "content", 0, "input", "city")) != "Berlin" {
		t.Fatalf("final anthropic request messages: %v", msgs)
	}
	// parallel results arrive together in ONE user message
	res = e.loop(tok, "claude-sonnet-5-5", "parallel", false)
	checkLoop(t, "anthropic", "parallel", "anthropic", res)
	msgs = list(lastBody(t, e.anth)["messages"])
	if len(msgs) != 3 || len(list(dig(msgs[2], "content"))) != 2 {
		t.Fatalf("parallel tool_results not merged into one user message: %v", msgs)
	}
	// usage is mapped
	if num(dig(tn.toolChat(tok, "claude-sonnet-5-5", "single", false, nil).json(), "usage", "total_tokens")) == 0 {
		t.Fatal("usage missing")
	}
}

func TestPhase2_AnthropicStreamToolCalls(t *testing.T) {
	e, tn, tok := p2fixture(t, keySpec{"anthropic", "mock-ok-anth", "anth-a"})
	for _, sc := range scenarios {
		calls := assertStreamCalls(t, tn.toolChat(tok, "claude-sonnet-5-5", sc, true, nil), sc)
		for i, c := range calls {
			if c.deltas < 3 {
				t.Fatalf("%s call %d: arguments should stream in fragments, got %d deltas", sc, i, c.deltas)
			}
		}
	}
	// usage chunk and [DONE] with include_usage
	r := tn.toolChat(tok, "claude-sonnet-5-5", "single", true, nil)
	s := parseSSE(r.body)
	hasUsage := false
	for _, c := range s.chunks {
		if c["usage"] != nil {
			hasUsage = true
		}
	}
	if !hasUsage || !s.done {
		t.Fatalf("stream usage/done missing: %s", r.body)
	}
	if !lastBodyStream(t, e) {
		t.Fatal("upstream request was not a stream")
	}
}

func lastBodyStream(t *testing.T, e *env) bool { return lastBody(t, e.anth)["stream"] == true }

func TestPhase2_GeminiToolCalling(t *testing.T) {
	e, tn, tok := p2fixture(t, keySpec{"google", "mock-ok-gem", "gem-a"})
	for _, sc := range scenarios {
		// the mock sends no tool-call ids and finish_reason "stop"; the router must normalise both
		assertToolResponse(t, tn.toolChat(tok, "gemini-2.5-flash", sc, false, nil), sc)
	}
	res := e.loop(tok, "gemini-2.5-flash", "chain", false)
	checkLoop(t, "gemini", "chain", "google", res)
	// the assistant turn sent back upstream carries the synthesised ids and the tool message echoes them
	msgs := list(lastBody(t, e.gem)["messages"])
	n := len(msgs)
	id := str(dig(msgs[n-1], "tool_call_id"))
	if id == "" || id != str(dig(msgs[n-2], "tool_calls", 0, "id")) || str(dig(msgs[n-1], "role")) != "tool" {
		t.Fatalf("gemini round-2 messages: %v", msgs)
	}
}

func TestPhase2_GeminiStreamToolCalls(t *testing.T) {
	_, tn, tok := p2fixture(t, keySpec{"google", "mock-ok-gem", "gem-a"})
	for _, sc := range scenarios {
		// whole-argument chunks with a repeated index 0 upstream; the client must see distinct indexes
		assertStreamCalls(t, tn.toolChat(tok, "gemini-2.5-flash", sc, true, nil), sc)
	}
}

func TestPhase2_MeshAPIToolCalling(t *testing.T) {
	e, tn, tok := p2fixture(t, keySpec{"meshapi", "rsk_mock-ok-mesh", "mesh-a"})
	for _, sc := range scenarios {
		assertToolResponse(t, tn.toolChat(tok, "mistral-7b", sc, false, nil), sc)
		assertStreamCalls(t, tn.toolChat(tok, "mistral-7b", sc, true, nil), sc)
		checkLoop(t, "meshapi", sc, "meshapi", e.loop(tok, "mistral-7b", sc, false))
	}
	if m := str(lastBody(t, e.mesh)["model"]); m != "mistral/mistral-7b-instruct-v0" {
		t.Fatalf("upstream model = %q", m)
	}
	if e.mesh.ChatCalls("rsk_mock-ok-mesh") == 0 {
		t.Fatal("Mesh mock did not see the rsk_ bearer key")
	}
}

func TestPhase2_CrossProviderToolParity(t *testing.T) {
	type outcome struct {
		rounds [][]string
		final  string
	}
	for _, sc := range scenarios {
		var ref *outcome
		refName := ""
		for _, f := range flavours {
			e, _, tok := f.fixture(t)
			for _, stream := range []bool{false, true} {
				r := e.loop(tok, f.model, sc, stream)
				o := &outcome{roundsOf(r), r.FinalText}
				if ref == nil {
					ref, refName = o, fmt.Sprintf("%s(stream=%v)", f.name, stream)
				}
				if !reflect.DeepEqual(o, ref) {
					t.Fatalf("%s: %s(stream=%v) = %+v differs from %s = %+v", sc, f.name, stream, o, refName, ref)
				}
			}
		}
	}
}

// ---- Anthropic chat ----

func TestPhase2_AnthropicChat(t *testing.T) {
	e, tn, tok := p2fixture(t, keySpec{"anthropic", "mock-ok-anth", "anth-a"})
	r := tn.chat(tok, chatOpts{model: "claude-sonnet-5-5"})
	assertStatus(t, r, 200)
	j := r.json()
	if str(dig(j, "choices", 0, "message", "content")) != mockprovider.ReplyText || str(dig(j, "choices", 0, "finish_reason")) != "stop" ||
		str(j["model"]) != "claude-sonnet-5-5" || num(dig(j, "usage", "total_tokens")) != 19 {
		t.Fatalf("chat body: %s", r)
	}
	if r.header.Get("X-Router0-Provider") != "anthropic" || r.header.Get("X-Router0-Canonical-Model") != "claude-sonnet-5-5" {
		t.Fatalf("headers: %v", r.header)
	}
	b := lastBody(t, e.anth)
	if num(b["max_tokens"]) <= 0 || str(b["model"]) != "claude-sonnet-5-5" {
		t.Fatalf("upstream messages request: %v", b)
	}
	if e.anth.ModelCalls("mock-ok-anth") == 0 {
		t.Fatal("anthropic /models was never listed")
	}
	row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 1)
	if str(dig(row, "trail", 0, "provider")) != "anthropic" {
		t.Fatalf("trail: %v", row["trail"])
	}
}

func TestPhase2_AnthropicStream(t *testing.T) {
	_, tn, tok := p2fixture(t, keySpec{"anthropic", "mock-ok-anth", "anth-a"})
	r := tn.chat(tok, chatOpts{model: "claude-sonnet-5-5", stream: true, usage: true})
	assertStatus(t, r, 200)
	s := parseSSE(r.body)
	var text strings.Builder
	usage := false
	for _, c := range s.chunks {
		text.WriteString(str(dig(c, "choices", 0, "delta", "content")))
		if c["usage"] != nil {
			usage = true
		}
	}
	if text.String() != mockprovider.ReplyText || !s.done || !usage || len(s.errors) != 0 {
		t.Fatalf("stream text=%q done=%v usage=%v: %s", text.String(), s.done, usage, r.body)
	}
	finish := ""
	for _, c := range s.chunks {
		if f := str(dig(c, "choices", 0, "finish_reason")); f != "" {
			finish = f
		}
	}
	if finish != "stop" {
		t.Fatalf("finish_reason = %q", finish)
	}
}

func TestPhase2_AnthropicCreditsIs402(t *testing.T) {
	e, tn, tok := p2fixture(t, keySpec{"anthropic", "mock-credits-a", "anth-a"})
	r := tn.chat(tok, chatOpts{model: "claude-sonnet-5-5"})
	j := assertErr(t, r, 402, "insufficient_credits", "insufficient_credits")
	if strings.Contains(string(r.body), mockprovider.SentinelBody) || strings.Contains(string(r.body), "credit balance") {
		t.Fatalf("provider body echoed: %s", j)
	}
	// the key is parked: the next request does not call upstream
	before := e.anth.ChatCalls("mock-credits-a")
	assertErr(t, tn.chat(tok, chatOpts{model: "claude-sonnet-5-5"}), 402, "insufficient_credits", "insufficient_credits")
	if e.anth.ChatCalls("mock-credits-a") != before {
		t.Fatal("second request called upstream")
	}
}

// ---- Mesh API ----

func TestPhase2_MeshAPIChat(t *testing.T) {
	e, tn, tok := p2fixture(t, keySpec{"meshapi", "rsk_mock-ok-a", "mesh-a"})
	r := tn.chat(tok, chatOpts{model: "mistral-7b"})
	assertStatus(t, r, 200)
	if str(dig(r.json(), "choices", 0, "message", "content")) != mockprovider.ReplyText || str(r.json()["model"]) != "mistral-7b" {
		t.Fatalf("body: %s", r)
	}
	if r.header.Get("X-Router0-Provider") != "meshapi" || r.header.Get("X-Router0-Model") != "mistral/mistral-7b-instruct-v0" {
		t.Fatalf("headers: %v", r.header)
	}
	if str(lastBody(t, e.mesh)["model"]) != "mistral/mistral-7b-instruct-v0" {
		t.Fatalf("upstream model: %v", lastBody(t, e.mesh))
	}
}

func TestPhase2_MeshAPIStream(t *testing.T) {
	_, tn, tok := p2fixture(t, keySpec{"meshapi", "rsk_mock-ok-a", "mesh-a"})
	r := tn.chat(tok, chatOpts{model: "mistral-7b", stream: true, usage: true})
	assertStatus(t, r, 200)
	s := parseSSE(r.body)
	var text strings.Builder
	for _, c := range s.chunks {
		text.WriteString(str(dig(c, "choices", 0, "delta", "content")))
	}
	if text.String() != mockprovider.ReplyText || !s.done || r.header.Get("X-Router0-Provider") != "meshapi" {
		t.Fatalf("stream: %s (%v)", r.body, r.header)
	}
}

func TestPhase2_MeshAPIFailover(t *testing.T) {
	t.Run("groq 429 to mesh", func(t *testing.T) {
		e, tn, tok := p2fixture(t,
			keySpec{"groq", "mock-429-30-a", "groq-a"},
			keySpec{"meshapi", "rsk_mock-ok-m", "mesh-a"})
		r := tn.chat(tok, chatOpts{model: "llama-3.3-70b"})
		assertStatus(t, r, 200)
		if r.header.Get("X-Router0-Provider") != "meshapi" || r.header.Get("X-Router0-Attempts") != "2" ||
			r.header.Get("X-Router0-Model") != "meta-llama/llama-3.3-70b-instruct" {
			t.Fatalf("headers: %v", r.header)
		}
		row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 2)
		if want := []string{"groq/groq-a:error/rate_limit", "meshapi/mesh-a:success"}; !eqStrings(trailOf(row), want) {
			t.Fatalf("trail: %v", trailOf(row))
		}
		if e.mesh.ChatCalls("rsk_mock-ok-m") != 1 {
			t.Fatal("mesh not called once")
		}
	})
	t.Run("mesh 429 to openrouter", func(t *testing.T) {
		_, tn, tok := p2fixture(t,
			keySpec{"meshapi", "rsk_mock-429-15-a", "mesh-a"},
			keySpec{"openrouter", "mock-ok-or", "or-main"})
		r := tn.chat(tok, chatOpts{model: "llama-3.3-70b"})
		assertStatus(t, r, 200)
		if r.header.Get("X-Router0-Provider") != "openrouter" || r.header.Get("X-Router0-Attempts") != "2" {
			t.Fatalf("headers: %v", r.header)
		}
		row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 2)
		if want := []string{"meshapi/mesh-a:error/rate_limit", "openrouter/or-main:success"}; !eqStrings(trailOf(row), want) {
			t.Fatalf("trail: %v", trailOf(row))
		}
	})
}

func TestPhase2_MeshAPIProviderAlias(t *testing.T) {
	e, tn, tok := p2fixture(t,
		keySpec{"groq", "mock-ok-g", "groq-a"},
		keySpec{"meshapi", "rsk_mock-ok-m", "mesh-a"})
	for _, alias := range []string{"aifiesta", "mesh", "meshapi"} {
		before := e.groq.TotalChatCalls()
		r := tn.chat(tok, chatOpts{model: "llama-3.3-70b", headers: map[string]string{"X-Router0-Providers": alias}})
		assertStatus(t, r, 200)
		if r.header.Get("X-Router0-Provider") != "meshapi" || e.groq.TotalChatCalls() != before {
			t.Fatalf("%s: headers %v groq calls %d->%d", alias, r.header, before, e.groq.TotalChatCalls())
		}
	}
}

func TestPhase2_ClaudeFailoverToOpenRouter(t *testing.T) {
	e, tn, tok := p2fixture(t,
		keySpec{"anthropic", "mock-529-a", "anth-a"},
		keySpec{"openrouter", "mock-ok-or", "or-main"})
	tn.waitCatalog("claude-sonnet-5-5", "anthropic", "openrouter")
	r := tn.chat(tok, chatOpts{model: "claude-sonnet-5-5"})
	assertStatus(t, r, 200)
	if r.header.Get("X-Router0-Provider") != "openrouter" || r.header.Get("X-Router0-Model") != "anthropic/claude-sonnet-5.5" ||
		r.header.Get("X-Router0-Canonical-Model") != "claude-sonnet-5-5" || r.header.Get("X-Router0-Attempts") != "2" {
		t.Fatalf("headers: %v", r.header)
	}
	if str(r.json()["model"]) != "claude-sonnet-5-5" {
		t.Fatalf("response model: %s", r)
	}
	row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 2)
	if want := []string{"anthropic/anth-a:error/transient", "openrouter/or-main:success"}; !eqStrings(trailOf(row), want) {
		t.Fatalf("trail: %v", trailOf(row))
	}
	if e.anth.ChatCalls("mock-529-a") != 1 {
		t.Fatal("anthropic not attempted first")
	}
}

// ---- catalog kinds ----

func catalogModels(t *testing.T, tn *tenant, query string) map[string][]string {
	t.Helper()
	r := tn.api("GET", "/api/catalog"+query, nil)
	assertStatus(t, r, 200)
	out := map[string][]string{}
	for _, m := range list(r.json()["models"]) {
		for _, p := range list(dig(m, "providers")) {
			out[str(dig(m, "canonical"))] = append(out[str(dig(m, "canonical"))], str(dig(p, "provider")))
		}
	}
	return out
}

func TestPhase2_ModelsChatOnly(t *testing.T) {
	_, tn, tok := p2fixture(t,
		keySpec{"google", "mock-ok-gem", "gem-a"},
		keySpec{"meshapi", "rsk_mock-ok-m", "mesh-a"})
	// wait for the last (non-chat) models of both listings to be stored
	waitFor(t, 5*time.Second, "non-chat models discovered", func() bool {
		c := catalogModels(t, tn, "")
		return c["dall-e-3"] != nil && c["text-embedding"] != nil
	})
	m := tn.e.do("GET", "/v1/models", tok, nil, nil)
	assertStatus(t, m, 200)
	have := map[string]bool{}
	for _, d := range list(m.json()["data"]) {
		id := str(dig(d, "id"))
		have[id] = true
		for _, bad := range []string{"image", "tts", "lyria", "computer-use", "robotics", "embed", "dall-e"} {
			if strings.Contains(id, bad) {
				t.Fatalf("/v1/models lists non-chat model %q", id)
			}
		}
	}
	if !have["gemini-2.5-flash"] || !have["mistral-7b"] {
		t.Fatalf("/v1/models missing chat models: %s", m)
	}
	img := catalogModels(t, tn, "?kind=image")
	if img["gemini-2.5-flash-image"] == nil || img["dall-e-3"] == nil || img["gemini-2.5-flash"] != nil {
		t.Fatalf("kind=image: %v", img)
	}
	aud := catalogModels(t, tn, "?kind=audio")
	if aud["gemini-2.5-flash-preview-tts"] == nil || aud["lyria-realtime-exp"] == nil || len(aud) != 2 {
		t.Fatalf("kind=audio: %v", aud)
	}
	oth := catalogModels(t, tn, "?kind=other")
	if oth["gemini-2.5-computer-use-preview"] == nil || oth["gemini-robotics-er-1.5-preview"] == nil {
		t.Fatalf("kind=other: %v", oth)
	}
	if emb := catalogModels(t, tn, "?kind=embedding"); emb["text-embedding"] == nil {
		t.Fatalf("kind=embedding: %v", emb)
	}
	assertStatus(t, tn.api("GET", "/api/catalog?kind=nope", nil), 422)
}

func TestPhase2_NonChatModelNotServed(t *testing.T) {
	e, tn, tok := p2fixture(t, keySpec{"google", "mock-ok-gem", "gem-a"})
	waitFor(t, 5*time.Second, "image model discovered", func() bool {
		return catalogModels(t, tn, "?kind=image")["gemini-2.5-flash-image"] != nil
	})
	before := e.gem.TotalChatCalls()
	assertErr(t, tn.chat(tok, chatOpts{model: "gemini-2.5-flash-image"}), 404, "invalid_request_error", "model_not_found")
	assertErr(t, tn.chat(tok, chatOpts{model: "gemini-2.5-flash-preview-tts"}), 404, "invalid_request_error", "model_not_found")
	if e.gem.TotalChatCalls() != before {
		t.Fatal("a non-chat model request reached the provider")
	}
	assertStatus(t, tn.chat(tok, chatOpts{model: "gemini-2.5-flash"}), 200)
}

// ---- aliases ----

func TestPhase2_Aliases(t *testing.T) {
	_, tn, tok := p2fixture(t, keySpec{"groq", "mock-ok-g", "groq-a"})
	member := tn.newMember()

	assertErr(t, member.api("POST", "/api/aliases", J{"alias": "smart", "canonical_model": "llama-3.3-70b"}), 403, "forbidden", "org_role_required")
	r := tn.api("POST", "/api/aliases", J{"alias": "Smart", "canonical_model": "llama-3.3-70b"})
	assertStatus(t, r, 201)
	if str(r.json()["alias"]) != "smart" || str(r.json()["canonical_model"]) != "llama-3.3-70b" {
		t.Fatalf("create: %s", r)
	}
	l := member.api("GET", "/api/aliases", nil) // any org member may list
	assertStatus(t, l, 200)
	if len(list(l.json()["aliases"])) != 1 || str(dig(l.json(), "aliases", 0, "alias")) != "smart" {
		t.Fatalf("list: %s", l)
	}
	// /v1/models shows the alias
	found := false
	for _, d := range list(tn.e.do("GET", "/v1/models", tok, nil, nil).json()["data"]) {
		if str(dig(d, "id")) == "smart" && str(dig(d, "router0", "alias_of")) == "llama-3.3-70b" {
			found = true
		}
	}
	if !found {
		t.Fatal("alias missing from /v1/models")
	}
	if len(list(tn.api("GET", "/api/catalog", nil).json()["aliases"])) != 1 {
		t.Fatal("alias missing from /api/catalog")
	}
	// serving through the alias reports the canonical model
	c := tn.chat(tok, chatOpts{model: "smart"})
	assertStatus(t, c, 200)
	if str(c.json()["model"]) != "llama-3.3-70b" || c.header.Get("X-Router0-Canonical-Model") != "llama-3.3-70b" {
		t.Fatalf("alias chat: %s %v", c, c.header)
	}
	// aliases are org-scoped
	other := tn.e.newTenant("p2-other")
	if len(list(other.api("GET", "/api/aliases", nil).json()["aliases"])) != 0 {
		t.Fatal("alias leaked to another org")
	}
	// delete
	assertErr(t, member.api("DELETE", "/api/aliases/smart", nil), 403, "forbidden", "org_role_required")
	assertStatus(t, tn.api("DELETE", "/api/aliases/smart", nil), 204)
	assertErr(t, tn.api("DELETE", "/api/aliases/smart", nil), 404, "invalid_request_error", "alias_not_found")
	assertErr(t, tn.chat(tok, chatOpts{model: "smart"}), 404, "invalid_request_error", "model_not_found")
}

func TestPhase2_AliasCannotShadow(t *testing.T) {
	_, tn, _ := p2fixture(t, keySpec{"groq", "mock-ok-g", "groq-a"})
	// an alias equal to an existing chat model is rejected: 422 alias_shadows_model
	// (slice 2E handler; the roadmap text said 409, the handler/api.md use 422)
	assertErr(t, tn.api("POST", "/api/aliases", J{"alias": "llama-3.3-70b", "canonical_model": "llama-3.1-8b"}), 422, "invalid_request_error", "alias_shadows_model")
	assertErr(t, tn.api("POST", "/api/aliases", J{"alias": "Llama-3.3-70B-Versatile", "canonical_model": "llama-3.1-8b"}), 422, "invalid_request_error", "alias_shadows_model")
	// a duplicate alias is 409 alias_conflict
	assertStatus(t, tn.api("POST", "/api/aliases", J{"alias": "fast", "canonical_model": "llama-3.1-8b"}), 201)
	assertErr(t, tn.api("POST", "/api/aliases", J{"alias": "fast", "canonical_model": "llama-3.3-70b"}), 409, "invalid_request_error", "alias_conflict")
	// invalid aliases
	assertErr(t, tn.api("POST", "/api/aliases", J{"alias": "bad alias!", "canonical_model": "llama-3.3-70b"}), 422, "invalid_request_error", "invalid_alias")
	assertErr(t, tn.api("POST", "/api/aliases", J{"alias": "loop", "canonical_model": "loop"}), 422, "invalid_request_error", "invalid_alias")
	assertErr(t, tn.api("POST", "/api/aliases", J{"alias": "chain", "canonical_model": "fast"}), 422, "invalid_request_error", "invalid_alias")
	if len(list(tn.api("GET", "/api/aliases", nil).json()["aliases"])) != 1 {
		t.Fatal("rejected aliases were stored")
	}
}

// ---- on-demand discovery ----

func ageRefreshes(t *testing.T, e *env, orgID string) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE org_catalog_refreshes SET last_refreshed_at = NOW() - INTERVAL '10 minutes' WHERE org_id = $1`, orgID); err != nil {
		t.Fatal(err)
	}
}

func TestPhase2_OnDemandDiscovery(t *testing.T) {
	e, tn, tok := p2fixture(t, keySpec{"openrouter", "mock-ok-or", "or-main"})
	assertErr(t, tn.chat(tok, chatOpts{model: "brand-other-model"}), 404, "invalid_request_error", "model_not_found")
	// the provider starts offering a model after the org's last refresh
	e.or.SetModels("meta-llama/llama-3.3-70b-instruct", "acme/new-model-x")
	ageRefreshes(t, e, tn.orgID)
	r := tn.chat(tok, chatOpts{model: "new-model-x"})
	assertStatus(t, r, 200)
	if r.header.Get("X-Router0-Provider") != "openrouter" || r.header.Get("X-Router0-Model") != "acme/new-model-x" ||
		r.header.Get("X-Router0-Canonical-Model") != "new-model-x" {
		t.Fatalf("headers: %v", r.header)
	}
	var src string
	if err := e.pool.QueryRow(context.Background(),
		`SELECT source FROM org_model_catalog WHERE org_id=$1 AND provider='openrouter' AND provider_model_id='acme/new-model-x'`, tn.orgID).Scan(&src); err != nil || src != "on_demand" {
		t.Fatalf("catalog row source = %q err=%v", src, err)
	}
	cat := tn.api("GET", "/api/catalog", nil)
	ok := false
	for _, m := range list(cat.json()["models"]) {
		if str(dig(m, "canonical")) == "new-model-x" && str(dig(m, "providers", 0, "source")) == "on_demand" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("catalog does not show new-model-x as on_demand: %s", cat)
	}
}

func TestPhase2_OnDemandDebounce(t *testing.T) {
	e, tn, tok := p2fixture(t, keySpec{"openrouter", "mock-ok-or", "or-main"})
	ageRefreshes(t, e, tn.orgID)
	base := e.or.TotalModelCalls()
	assertErr(t, tn.chat(tok, chatOpts{model: "ghost-model-y"}), 404, "invalid_request_error", "model_not_found")
	afterFirst := e.or.TotalModelCalls()
	if afterFirst != base+1 {
		t.Fatalf("first miss should re-list once: /models calls %d -> %d", base, afterFirst)
	}
	// a second miss within 60 s calls no provider, even with a stale refresh
	ageRefreshes(t, e, tn.orgID)
	assertErr(t, tn.chat(tok, chatOpts{model: "ghost-model-y"}), 404, "invalid_request_error", "model_not_found")
	assertErr(t, tn.chat(tok, chatOpts{model: "Ghost-Model-Y"}), 404, "invalid_request_error", "model_not_found")
	if n := e.or.TotalModelCalls(); n != afterFirst {
		t.Fatalf("debounced misses re-listed the provider: /models calls %d -> %d", afterFirst, n)
	}
}
