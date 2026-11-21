package routing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zer0-router/zer0/internal/catalog"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/keystate"
	"github.com/zer0-router/zer0/internal/providers"
)

// ---- fakes ----

type fakeStates struct {
	mu     sync.Mutex
	states map[keystate.KeyModel]keystate.State
	marks  []string // "keyID:class"
	checks int
	err    error
}

func (f *fakeStates) Check(ctx context.Context, k, m string) (keystate.State, error) {
	return f.states[keystate.KeyModel{KeyID: k, ProviderModelID: m}], nil
}
func (f *fakeStates) CheckMany(ctx context.Context, p []keystate.KeyModel) (map[keystate.KeyModel]keystate.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if f.err != nil {
		return nil, f.err
	}
	out := map[keystate.KeyModel]keystate.State{}
	for _, km := range p {
		out[km] = f.states[km]
	}
	return out, nil
}
func (f *fakeStates) Mark(ctx context.Context, k, m string, e *providers.ProviderError) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marks = append(f.marks, k+":"+string(e.Class))
	return nil
}

type fakeSide struct{ auth, unavail []string }

func (f *fakeSide) MarkAuthError(ctx context.Context, id string) error {
	f.auth = append(f.auth, id)
	return nil
}
func (f *fakeSide) MarkUnavailable(ctx context.Context, org, prov, m string) error {
	f.unavail = append(f.unavail, org+"/"+prov+"/"+m)
	return nil
}

func pe(class providers.ErrorClass, retry time.Duration) *providers.ProviderError {
	return &providers.ProviderError{Class: class, HTTP: 400, Code: "c-" + string(class), RetryAfter: retry, Message: "msg-" + string(class)}
}

func cand(p, id string) Candidate {
	return Candidate{Provider: p, ProviderModelID: "m-" + p, KeyID: id, KeyLabel: "label-" + id}
}

func newEx(fs *fakeStates, side *fakeSide, max int) *Executor {
	cfg := Config{States: fs, MaxAttempts: max, AttemptTimeout: time.Second}
	if side != nil {
		cfg.Auth, cfg.Catalog = side, side
	}
	return NewExecutor(cfg).ForRequest("org1", "llama", nil)
}

// scripted returns an AttemptFunc whose result per key id comes from m; unknown = success.
func scripted(m map[string]error, calls *[]string) AttemptFunc {
	return func(ctx context.Context, c Candidate, key string) error {
		*calls = append(*calls, c.KeyID)
		return m[c.KeyID]
	}
}

// ---- plan ----

func entries(provs ...string) []catalog.Entry {
	var out []catalog.Entry
	for _, p := range provs {
		out = append(out, catalog.Entry{Canonical: "llama", Provider: p, ProviderModelID: "m-" + p, Kind: "chat"})
	}
	return out
}

func pk(id, label string) *keys.ProviderKey {
	return &keys.ProviderKey{ID: id, Label: label, Status: "active"}
}

func ids(plan []Candidate) string {
	var s []string
	for _, c := range plan {
		s = append(s, c.KeyID)
	}
	return strings.Join(s, ",")
}

func TestBuildPlan(t *testing.T) {
	kb := map[string][]*keys.ProviderKey{
		"groq":       {pk("g1", "a"), pk("g2", "b"), {ID: "gx", Status: "error"}},
		"openrouter": {pk("o1", "or")},
		"google":     {pk("x1", "x")},
		"together":   nil,
	}
	tests := []struct {
		name     string
		entries  []catalog.Entry
		order    []string
		override []string
		want     string
	}{
		{"priority order", entries("groq", "openrouter"), []string{"openrouter", "groq"}, nil, "o1,g1,g2"},
		{"remaining alphabetical", entries("openrouter", "groq", "google"), []string{"openrouter"}, nil, "o1,x1,g1,g2"},
		{"override restricts and reorders", entries("groq", "openrouter"), []string{"groq", "openrouter"}, []string{"openrouter", "groq"}, "o1,g1,g2"},
		{"override restricts", entries("groq", "openrouter"), []string{"groq"}, []string{"openrouter"}, "o1"},
		{"override alias gemini", entries("groq", "google"), []string{"groq"}, []string{"gemini"}, "x1"},
		{"override unknown/absent ignored", entries("groq"), nil, []string{"nosuch", "openrouter"}, ""},
		{"provider with no keys skipped", entries("together", "groq"), []string{"together", "groq"}, nil, "g1,g2"},
		{"provider without entry skipped", entries("groq"), []string{"openrouter", "groq"}, nil, "g1,g2"},
		{"duplicate order entries", entries("groq"), []string{"groq", "groq"}, nil, "g1,g2"},
		{"no entries", nil, []string{"groq"}, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := BuildPlan(tc.entries, tc.order, tc.override, kb)
			if got := ids(plan); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			for _, c := range plan {
				if c.ProviderModelID != "m-"+c.Provider {
					t.Fatalf("bad model id %+v", c)
				}
			}
		})
	}
}

func TestParseProviderOverride(t *testing.T) {
	// groq/openrouter/google adapters are registered by the providers package init.
	o, u := ParseProviderOverride(" Groq , gemini,groq,, OPENROUTER ")
	if u != "" || strings.Join(o, ",") != "groq,google,openrouter" {
		t.Fatalf("got %v unknown=%q", o, u)
	}
	if o, u := ParseProviderOverride("groq,nosuch"); u != "nosuch" || len(o) != 1 {
		t.Fatalf("got %v %q", o, u)
	}
	if o, u := ParseProviderOverride("   "); o != nil || u != "" {
		t.Fatalf("empty header: %v %q", o, u)
	}
}

// ---- FinalError precedence: every combination of classes ----

func TestFinalErrorAllCombinations(t *testing.T) {
	classes := []providers.ErrorClass{
		providers.ErrorClassRateLimit, providers.ErrorClassInsufficientCredit, providers.ErrorClassModelUnavailable,
		providers.ErrorClassBadRequest, providers.ErrorClassTransient, providers.ErrorClassAuth,
	}
	// every non-empty subset of classes, one attempt per member
	for mask := 1; mask < 1<<len(classes); mask++ {
		has := map[providers.ErrorClass]bool{}
		var res Result
		for i, c := range classes {
			if mask&(1<<i) != 0 {
				has[c] = true
				ra := time.Duration(0)
				if c == providers.ErrorClassRateLimit {
					ra = 12 * time.Second
				}
				res.Attempts = append(res.Attempts, AttemptRecord{Candidate: cand("groq", "k"), Order: len(res.Attempts),
					Outcome: OutcomeError, Class: string(c), RetryAfter: ra, Message: "bad-msg"})
			}
		}
		var wantHTTP int
		var wantCode string
		onlyBU := !has[providers.ErrorClassRateLimit] && !has[providers.ErrorClassInsufficientCredit] &&
			!has[providers.ErrorClassTransient] && !has[providers.ErrorClassAuth]
		switch {
		case has[providers.ErrorClassRateLimit]:
			wantHTTP, wantCode = 429, "rate_limited"
		case has[providers.ErrorClassInsufficientCredit]:
			wantHTTP, wantCode = 402, "insufficient_credits"
		case onlyBU && !has[providers.ErrorClassBadRequest]:
			wantHTTP, wantCode = 404, "model_not_found"
		case onlyBU:
			wantHTTP, wantCode = 400, "upstream_rejected_request"
		default:
			wantHTTP, wantCode = 503, "all_providers_failed"
		}
		ge := FinalError(res)
		if ge.HTTP != wantHTTP || ge.Code != wantCode {
			t.Fatalf("mask %b: got %d/%s want %d/%s", mask, ge.HTTP, ge.Code, wantHTTP, wantCode)
		}
		if wantHTTP == 429 && ge.RetryAfterSeconds != 12 {
			t.Fatalf("mask %b: retry-after %d", mask, ge.RetryAfterSeconds)
		}
		if wantHTTP != 429 && ge.RetryAfterSeconds != 0 {
			t.Fatalf("mask %b: unexpected retry-after", mask)
		}
	}
}

func TestFinalErrorDetails(t *testing.T) {
	skip := func(reason string, ra time.Duration) AttemptRecord {
		return AttemptRecord{Candidate: cand("groq", "s"), Outcome: OutcomeSkipped, SkipReason: reason, RetryAfter: ra}
	}
	errRec := func(c providers.ErrorClass, p string, ra time.Duration, msg string) AttemptRecord {
		return AttemptRecord{Candidate: cand(p, "e"), Outcome: OutcomeError, Class: string(c), RetryAfter: ra, Message: msg}
	}
	t.Run("empty plan -> 404 with param", func(t *testing.T) {
		ge := FinalError(Result{})
		if ge.HTTP != 404 || ge.Code != "model_not_found" || ge.Param != "model" || ge.Type != "invalid_request_error" {
			t.Fatalf("%+v", ge)
		}
	})
	t.Run("skipped rate_limited counts as 429, min retry-after, ceil", func(t *testing.T) {
		ge := FinalError(Result{Attempts: []AttemptRecord{
			skip(keystate.ReasonRateLimited, 9500*time.Millisecond),
			errRec(providers.ErrorClassRateLimit, "openrouter", 5200*time.Millisecond, ""),
			skip(keystate.ReasonCreditsExhausted, time.Hour),
		}})
		if ge.HTTP != 429 || ge.Type != "rate_limit_error" || ge.RetryAfterSeconds != 6 {
			t.Fatalf("%+v", ge)
		}
	})
	t.Run("retry-after floor of 1", func(t *testing.T) {
		ge := FinalError(Result{Attempts: []AttemptRecord{skip(keystate.ReasonRateLimited, 10*time.Millisecond)}})
		if ge.RetryAfterSeconds != 1 {
			t.Fatalf("%+v", ge)
		}
		ge = FinalError(Result{Attempts: []AttemptRecord{errRec(providers.ErrorClassRateLimit, "groq", 0, "")}})
		if ge.RetryAfterSeconds != 1 {
			t.Fatalf("no hint: %+v", ge)
		}
	})
	t.Run("skipped credits/budget -> 402", func(t *testing.T) {
		for _, r := range []string{keystate.ReasonCreditsExhausted, keystate.ReasonBudgetExhausted} {
			ge := FinalError(Result{Attempts: []AttemptRecord{skip(r, time.Hour), errRec(providers.ErrorClassTransient, "groq", 0, "")}})
			if ge.HTTP != 402 || ge.Type != "insufficient_credits" {
				t.Fatalf("%s: %+v", r, ge)
			}
		}
	})
	t.Run("400 message is first bad_request prefixed with provider", func(t *testing.T) {
		ge := FinalError(Result{Attempts: []AttemptRecord{
			errRec(providers.ErrorClassModelUnavailable, "groq", 0, "nf"),
			errRec(providers.ErrorClassBadRequest, "openrouter", 0, "first bad"),
			errRec(providers.ErrorClassBadRequest, "google", 0, "second bad"),
		}})
		if ge.HTTP != 400 || ge.Message != "openrouter: first bad" || ge.Type != "invalid_request_error" {
			t.Fatalf("%+v", ge)
		}
	})
	t.Run("400+404 mix -> 400, 404+503 -> 503, 429+402 -> 429, 402+503 -> 402", func(t *testing.T) {
		cases := []struct {
			a, b providers.ErrorClass
			want int
		}{
			{providers.ErrorClassBadRequest, providers.ErrorClassModelUnavailable, 400},
			{providers.ErrorClassModelUnavailable, providers.ErrorClassTransient, 503},
			{providers.ErrorClassRateLimit, providers.ErrorClassInsufficientCredit, 429},
			{providers.ErrorClassInsufficientCredit, providers.ErrorClassTransient, 402},
			{providers.ErrorClassAuth, providers.ErrorClassBadRequest, 503},
		}
		for _, c := range cases {
			ge := FinalError(Result{Attempts: []AttemptRecord{errRec(c.a, "groq", time.Second, "x"), errRec(c.b, "groq", 0, "x")}})
			if ge.HTTP != c.want {
				t.Fatalf("%s+%s: got %d want %d", c.a, c.b, ge.HTTP, c.want)
			}
		}
	})
	t.Run("cancelled attempt takes no part", func(t *testing.T) {
		ge := FinalError(Result{Attempts: []AttemptRecord{{Candidate: cand("groq", "k"), Outcome: OutcomeError, Code: CodeClientCancelled}}})
		if ge.HTTP != 503 {
			t.Fatalf("%+v", ge)
		}
	})
	t.Run("no raw bodies in messages", func(t *testing.T) {
		ge := FinalError(Result{Attempts: []AttemptRecord{errRec(providers.ErrorClassTransient, "groq", 0, "SENTINEL-BODY")}})
		if strings.Contains(ge.Message, "SENTINEL-BODY") {
			t.Fatal("leaked provider message in 503")
		}
	})
}

// ---- executor ----

func TestExecutorFailoverAndTrail(t *testing.T) {
	fs := &fakeStates{}
	side := &fakeSide{}
	ex := newEx(fs, side, 8)
	plan := []Candidate{cand("groq", "A"), cand("groq", "B"), cand("openrouter", "C")}
	var calls []string
	res := ex.Run(context.Background(), plan, scripted(map[string]error{
		"A": pe(providers.ErrorClassTransient, 0),
		"B": pe(providers.ErrorClassRateLimit, 40*time.Second),
	}, &calls))
	if res.Winner == nil || res.Winner.KeyID != "C" {
		t.Fatalf("winner %+v", res.Winner)
	}
	if strings.Join(calls, ",") != "A,B,C" || len(res.Attempts) != 3 {
		t.Fatalf("calls %v attempts %d", calls, len(res.Attempts))
	}
	a := res.Attempts
	if a[0].Outcome != "error" || a[0].Class != "transient" || a[0].Code != "c-transient" || a[0].Order != 0 {
		t.Fatalf("%+v", a[0])
	}
	if a[1].Class != "rate_limit" || a[1].RetryAfter != 40*time.Second {
		t.Fatalf("%+v", a[1])
	}
	if a[2].Outcome != "success" || a[2].Order != 2 {
		t.Fatalf("%+v", a[2])
	}
	if strings.Join(fs.marks, ",") != "A:transient,B:rate_limit" {
		t.Fatalf("marks %v", fs.marks)
	}
	if fs.checks != 1 {
		t.Fatalf("CheckMany called %d times", fs.checks)
	}
}

func TestExecutorRateLimitDefaultRetryAfterFilled(t *testing.T) {
	var calls []string
	res := newEx(&fakeStates{}, nil, 8).Run(context.Background(), []Candidate{cand("groq", "A")},
		scripted(map[string]error{"A": pe(providers.ErrorClassRateLimit, 0)}, &calls))
	if res.Attempts[0].RetryAfter != 30*time.Second {
		t.Fatalf("got %v", res.Attempts[0].RetryAfter)
	}
}

func TestExecutorSkipsBlockedAndDoesNotCallThem(t *testing.T) {
	km := func(id string) keystate.KeyModel { return keystate.KeyModel{KeyID: id, ProviderModelID: "m-groq"} }
	fs := &fakeStates{states: map[keystate.KeyModel]keystate.State{
		km("A"): {Blocked: true, Reason: keystate.ReasonRateLimited, RetryAfter: 12 * time.Second},
		km("B"): {Blocked: true, Reason: keystate.ReasonCreditsExhausted, RetryAfter: time.Hour},
		km("C"): {Blocked: true, Reason: keystate.ReasonBudgetExhausted, RetryAfter: time.Hour},
	}}
	var calls []string
	res := newEx(fs, nil, 8).Run(context.Background(),
		[]Candidate{cand("groq", "A"), cand("groq", "B"), cand("groq", "C"), cand("groq", "D")}, scripted(nil, &calls))
	if strings.Join(calls, ",") != "D" || res.Winner.KeyID != "D" {
		t.Fatalf("calls %v", calls)
	}
	want := []struct{ reason, class string }{{"rate_limited", "rate_limit"}, {"credits_exhausted", "insufficient_credits"}, {"budget_exhausted", "insufficient_credits"}}
	for i, w := range want {
		a := res.Attempts[i]
		if a.Outcome != "skipped" || a.SkipReason != w.reason || a.Class != w.class {
			t.Fatalf("attempt %d: %+v", i, a)
		}
	}
	if res.Attempts[0].RetryAfter != 12*time.Second {
		t.Fatalf("retry after %v", res.Attempts[0].RetryAfter)
	}
	if len(fs.marks) != 0 {
		t.Fatalf("marks %v", fs.marks)
	}
}

func TestExecutorAllSkippedFinalError(t *testing.T) {
	km := keystate.KeyModel{KeyID: "A", ProviderModelID: "m-groq"}
	fs := &fakeStates{states: map[keystate.KeyModel]keystate.State{km: {Blocked: true, Reason: keystate.ReasonRateLimited, RetryAfter: 7 * time.Second}}}
	var calls []string
	res := newEx(fs, nil, 8).Run(context.Background(), []Candidate{cand("groq", "A")}, scripted(nil, &calls))
	if len(calls) != 0 || res.Winner != nil {
		t.Fatal("must not call")
	}
	ge := FinalError(res)
	if ge.HTTP != 429 || ge.RetryAfterSeconds != 7 {
		t.Fatalf("%+v", ge)
	}
}

func TestExecutorTransientCooldownMovedToEndNotSkipped(t *testing.T) {
	km := func(id string) keystate.KeyModel { return keystate.KeyModel{KeyID: id, ProviderModelID: "m-groq"} }
	fs := &fakeStates{states: map[keystate.KeyModel]keystate.State{
		km("A"): {Blocked: true, Reason: keystate.ReasonTransient, RetryAfter: 5 * time.Second},
	}}
	var calls []string
	res := newEx(fs, nil, 8).Run(context.Background(), []Candidate{cand("groq", "A"), cand("groq", "B"), cand("groq", "C")},
		scripted(map[string]error{"B": pe(providers.ErrorClassTransient, 0), "C": pe(providers.ErrorClassTransient, 0)}, &calls))
	if strings.Join(calls, ",") != "B,C,A" || res.Winner == nil || res.Winner.KeyID != "A" {
		t.Fatalf("calls %v winner %+v", calls, res.Winner)
	}
	// single-key org: still attempted
	calls = nil
	res = newEx(fs, nil, 8).Run(context.Background(), []Candidate{cand("groq", "A")}, scripted(nil, &calls))
	if len(calls) != 1 || res.Winner == nil {
		t.Fatalf("single key in cooldown must still be attempted: %v", calls)
	}
}

func TestExecutorCheckFailsOpen(t *testing.T) {
	fs := &fakeStates{err: errors.New("db down")}
	var calls []string
	res := newEx(fs, nil, 8).Run(context.Background(), []Candidate{cand("groq", "A")}, scripted(nil, &calls))
	if res.Winner == nil || len(calls) != 1 {
		t.Fatal("expected fail open")
	}
}

func TestExecutorSideEffectsPerClass(t *testing.T) {
	tests := []struct {
		class                    providers.ErrorClass
		marks, auth, unavailable int
	}{
		{providers.ErrorClassRateLimit, 1, 0, 0},
		{providers.ErrorClassInsufficientCredit, 1, 0, 0},
		{providers.ErrorClassTransient, 1, 0, 0},
		{providers.ErrorClassAuth, 0, 1, 0},
		{providers.ErrorClassModelUnavailable, 0, 0, 1},
		{providers.ErrorClassBadRequest, 0, 0, 0},
	}
	for _, tc := range tests {
		t.Run(string(tc.class), func(t *testing.T) {
			fs, side := &fakeStates{}, &fakeSide{}
			var calls []string
			// failover must continue on ANY class
			res := newEx(fs, side, 8).Run(context.Background(), []Candidate{cand("groq", "A"), cand("groq", "B")},
				scripted(map[string]error{"A": pe(tc.class, time.Second)}, &calls))
			if res.Winner == nil || res.Winner.KeyID != "B" {
				t.Fatalf("no failover: %+v", res)
			}
			if len(fs.marks) != tc.marks || len(side.auth) != tc.auth || len(side.unavail) != tc.unavailable {
				t.Fatalf("marks=%v auth=%v unavail=%v", fs.marks, side.auth, side.unavail)
			}
			if tc.class == providers.ErrorClassAuth && side.auth[0] != "A" {
				t.Fatalf("auth key %v", side.auth)
			}
			if tc.class == providers.ErrorClassModelUnavailable && side.unavail[0] != "org1/groq/m-groq" {
				t.Fatalf("unavail %v", side.unavail)
			}
		})
	}
}

func TestExecutorMaxAttempts(t *testing.T) {
	fs := &fakeStates{}
	var plan []Candidate
	failing := map[string]error{}
	for _, id := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"} {
		plan = append(plan, cand("groq", id))
		failing[id] = pe(providers.ErrorClassTransient, 0)
	}
	var calls []string
	res := newEx(fs, nil, 8).Run(context.Background(), plan, scripted(failing, &calls))
	if len(calls) != 8 || len(res.Attempts) != 8 || res.Winner != nil {
		t.Fatalf("calls=%d attempts=%d", len(calls), len(res.Attempts))
	}
	if ge := FinalError(res); ge.HTTP != 503 || ge.Code != "all_providers_failed" {
		t.Fatalf("%+v", ge)
	}
}

func TestExecutorSkippedDoNotCountTowardMax(t *testing.T) {
	fs := &fakeStates{states: map[keystate.KeyModel]keystate.State{}}
	var plan []Candidate
	for _, id := range []string{"s1", "s2", "s3"} {
		plan = append(plan, cand("groq", id))
		fs.states[keystate.KeyModel{KeyID: id, ProviderModelID: "m-groq"}] = keystate.State{Blocked: true, Reason: keystate.ReasonRateLimited, RetryAfter: time.Second}
	}
	plan = append(plan, cand("groq", "ok"))
	var calls []string
	res := newEx(fs, nil, 1).Run(context.Background(), plan, scripted(nil, &calls))
	if res.Winner == nil || len(res.Attempts) != 4 {
		t.Fatalf("%+v", res)
	}
}

func TestExecutorNoRetryOnCancelledContext(t *testing.T) {
	fs, side := &fakeStates{}, &fakeSide{}
	ctx, cancel := context.WithCancel(context.Background())
	var calls []string
	res := newEx(fs, side, 8).Run(ctx, []Candidate{cand("groq", "A"), cand("groq", "B")}, func(ctx context.Context, c Candidate, k string) error {
		calls = append(calls, c.KeyID)
		cancel() // client disconnects during the attempt
		return context.Canceled
	})
	if len(calls) != 1 || !res.Cancelled || res.Winner != nil {
		t.Fatalf("calls=%v %+v", calls, res)
	}
	if len(fs.marks) != 0 || len(side.auth) != 0 || len(side.unavail) != 0 {
		t.Fatalf("cancelled attempt must not mark: %v %v %v", fs.marks, side.auth, side.unavail)
	}
	a := res.Attempts[0]
	if a.Outcome != "error" || a.Code != CodeClientCancelled || a.Class != "" {
		t.Fatalf("%+v", a)
	}
	// even a ProviderError returned after cancellation is not counted against the key
	ctx2, cancel2 := context.WithCancel(context.Background())
	res = newEx(fs, side, 8).Run(ctx2, []Candidate{cand("groq", "A")}, func(ctx context.Context, c Candidate, k string) error {
		cancel2()
		return pe(providers.ErrorClassRateLimit, time.Second)
	})
	if !res.Cancelled || len(fs.marks) != 0 {
		t.Fatalf("marks %v", fs.marks)
	}
	// already-cancelled ctx: nothing is called
	calls = nil
	res = newEx(fs, side, 8).Run(ctx2, []Candidate{cand("groq", "A")}, scripted(nil, &calls))
	if len(calls) != 0 || !res.Cancelled {
		t.Fatal("must not call with cancelled ctx")
	}
}

func TestExecutorNonProviderErrorsAreTransient(t *testing.T) {
	fs := &fakeStates{}
	var calls []string
	res := newEx(fs, nil, 8).Run(context.Background(), []Candidate{cand("groq", "A"), cand("groq", "B")},
		scripted(map[string]error{"A": errors.New("boom SENTINEL-BODY"), "B": context.DeadlineExceeded}, &calls))
	for i, code := range []string{CodeAttemptError, CodeAttemptTimeout} {
		a := res.Attempts[i]
		if a.Class != "transient" || a.Code != code || strings.Contains(a.Message, "SENTINEL-BODY") {
			t.Fatalf("%+v", a)
		}
	}
}

func TestExecutorAttemptTimeout(t *testing.T) {
	fs := &fakeStates{}
	ex := NewExecutor(Config{States: fs, MaxAttempts: 2, AttemptTimeout: 30 * time.Millisecond}).ForRequest("o", "m", nil)
	res := ex.Run(context.Background(), []Candidate{cand("groq", "A")}, func(ctx context.Context, c Candidate, k string) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if res.Cancelled || len(res.Attempts) != 1 || res.Attempts[0].Class != "transient" || res.Attempts[0].Code != CodeAttemptTimeout {
		t.Fatalf("%+v", res)
	}
	// disabled timeout leaves ctx without deadline
	ex = ex.WithAttemptTimeout(0)
	ex.Run(context.Background(), []Candidate{cand("groq", "A")}, func(ctx context.Context, c Candidate, k string) error {
		if _, ok := ctx.Deadline(); ok {
			t.Error("deadline present")
		}
		return nil
	})
}

func TestExecutorSecretResolution(t *testing.T) {
	fs := &fakeStates{}
	ex := NewExecutor(Config{States: fs, MaxAttempts: 8, AttemptTimeout: time.Second}).ForRequest("o", "m", func(c Candidate) (string, error) {
		if c.KeyID == "bad" {
			return "", errors.New("decrypt: SENTINEL-BODY")
		}
		return "plain-" + c.KeyID, nil
	})
	var got string
	res := ex.Run(context.Background(), []Candidate{cand("groq", "bad"), cand("groq", "good")}, func(ctx context.Context, c Candidate, k string) error {
		got = k
		return nil
	})
	if got != "plain-good" || res.Winner.KeyID != "good" {
		t.Fatalf("got %q", got)
	}
	a := res.Attempts[0]
	if a.Outcome != "error" || a.Class != "transient" || a.Code != CodeKeyDecryptFailed || strings.Contains(a.Message, "SENTINEL") {
		t.Fatalf("%+v", a)
	}
	if strings.Join(fs.marks, ",") != "bad:transient" {
		t.Fatalf("%v", fs.marks)
	}
}

func TestExecutorEmptyPlan(t *testing.T) {
	res := newEx(&fakeStates{}, nil, 8).Run(context.Background(), nil, nil)
	if res.Winner != nil || len(res.Attempts) != 0 || FinalError(res).HTTP != 404 {
		t.Fatalf("%+v", res)
	}
}

func TestExecutorTrailHoldsNoBodies(t *testing.T) {
	var calls []string
	e := pe(providers.ErrorClassBadRequest, 0)
	e.Code = strings.Repeat("x", 500)
	res := newEx(&fakeStates{}, nil, 8).Run(context.Background(), []Candidate{cand("groq", "A")}, scripted(map[string]error{"A": e}, &calls))
	if len(res.Attempts[0].Code) != 200 {
		t.Fatalf("code length %d", len(res.Attempts[0].Code))
	}
}
