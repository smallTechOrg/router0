//go:build integration

package integration

import (
	"context"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zer0-router/zer0/internal/metadata"
)

// keyByLabel returns the GET /api/keys entry with the label (fails if absent).
func (tn *tenant) keyByLabel(label string) J {
	tn.e.t.Helper()
	r := tn.api("GET", "/api/keys", nil)
	assertStatus(tn.e.t, r, 200)
	for _, k := range list(r.json()["keys"]) {
		if str(dig(k, "label")) == label {
			return k.(J)
		}
	}
	tn.e.t.Fatalf("key %q not in GET /api/keys: %s", label, r)
	return nil
}

// orFirst puts OpenRouter ahead of Groq in the org's provider priority.
func (tn *tenant) orFirst() {
	tn.e.t.Helper()
	assertStatus(tn.e.t, tn.api("PUT", "/api/routing/priority", J{"providers": []string{"openrouter", "groq"}}), 200)
}

// priceOpenRouterModel makes each mock request (12 prompt + 7 completion tokens)
// on OpenRouter's llama-3.3-70b cost exactly $0.0006 via catalog pricing.
func (tn *tenant) priceOpenRouterModel() {
	tn.e.t.Helper()
	tag, err := tn.e.pool.Exec(context.Background(),
		`UPDATE org_model_catalog SET input_cost_per_mtok = 50, output_cost_per_mtok = 0
		  WHERE org_id = $1 AND provider = 'openrouter' AND canonical_model = 'llama-3.3-70b'`, tn.orgID)
	if err != nil || tag.RowsAffected() == 0 {
		tn.e.t.Fatalf("price catalog: %v (%d rows)", err, tag.RowsAffected())
	}
}

func TestPhase3_BalancePollMarksExhausted(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-ok-g"}, []string{"mock-ok-or"})
	tn.orFirst()

	// Before any poll: OpenRouter supports balances, Groq does not.
	if s := str(dig(tn.keyByLabel("groq-a"), "balance", "status")); s != "unsupported" {
		t.Fatalf("groq balance status = %q, want unsupported", s)
	}

	e.or.SetBalance(0)
	r := tn.api("POST", "/api/keys/poll-balances", nil)
	assertStatus(t, r, 200)
	if num(r.json()["polled"]) != 1 {
		t.Fatalf("poll: %s", r)
	}
	k := tn.keyByLabel("or-main")
	if str(dig(k, "state", "reason")) != "credits_exhausted" || str(dig(k, "balance", "status")) != "exhausted" ||
		num(dig(k, "balance", "remaining_usd")) != 0 {
		t.Fatalf("or-main after poll: %v", k)
	}

	r = tn.chat(tok, chatOpts{})
	assertStatus(t, r, 200)
	if r.header.Get("X-Router0-Key") != "groq-a" {
		t.Fatalf("served by %q, want groq-a", r.header.Get("X-Router0-Key"))
	}
	if n := e.or.ChatCalls("mock-ok-or"); n != 0 {
		t.Fatalf("exhausted key was called %d times", n)
	}
	row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 2)
	if str(dig(row, "trail", 0, "outcome")) != "skipped" || str(dig(row, "trail", 0, "skip_reason")) != "credits_exhausted" {
		t.Fatalf("trail: %v", trailOf(row))
	}
}

func TestPhase3_BalancePollClears(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-ok-g"}, []string{"mock-ok-or"})
	tn.orFirst()
	e.or.SetBalance(0)
	assertStatus(t, tn.api("POST", "/api/keys/poll-balances", nil), 200)
	if str(dig(tn.keyByLabel("or-main"), "state", "reason")) != "credits_exhausted" {
		t.Fatal("poll did not mark or-main exhausted")
	}

	e.or.SetBalance(5)
	// The background loop's code path (all orgs), not the HTTP endpoint.
	if _, err := e.app.Balance.PollNow(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	k := tn.keyByLabel("or-main")
	if dig(k, "state", "blocked") == true || str(dig(k, "balance", "status")) != "ok" || num(dig(k, "balance", "remaining_usd")) != 5 {
		t.Fatalf("or-main after second poll: %v", k)
	}
	r := tn.chat(tok, chatOpts{})
	assertStatus(t, r, 200)
	if r.header.Get("X-Router0-Key") != "or-main" {
		t.Fatalf("served by %q, want or-main", r.header.Get("X-Router0-Key"))
	}
}

func TestPhase3_MonthlyBudget(t *testing.T) {
	e, tn, tok := fixture(t, nil, []string{"mock-ok-or"})
	tn.priceOpenRouterModel()
	id := str(tn.keyByLabel("or-main")["id"])

	// validation
	assertErr(t, tn.api("PATCH", "/api/keys/"+id, J{"monthly_budget_usd": -1}), 422, "validation_error", "invalid_budget")
	assertErr(t, tn.api("PATCH", "/api/keys/"+id, J{"bogus": 1}), 422, "validation_error", "unknown_field")
	assertErr(t, tn.api("PATCH", "/api/keys/"+id, J{"monthly_budget_usd": 0.00003}), 422, "validation_error", "invalid_budget")
	assertErr(t, tn.api("PATCH", "/api/keys/00000000-0000-0000-0000-000000000000", J{"monthly_budget_usd": 1}), 404, "not_found", "key_not_found")
	member := tn.newMember()
	if r := member.api("PATCH", "/api/keys/"+id, J{"monthly_budget_usd": 1}); r.status != 403 {
		t.Fatalf("member PATCH: %s", r)
	}
	other := e.newTenant("other")
	assertErr(t, other.api("PATCH", "/api/keys/"+id, J{"monthly_budget_usd": 1}), 404, "not_found", "key_not_found")

	r := tn.api("PATCH", "/api/keys/"+id, J{"monthly_budget_usd": 0.001})
	assertStatus(t, r, 200)
	if num(r.json()["monthly_budget_usd"]) != 0.001 {
		t.Fatalf("PATCH response: %s", r)
	}

	for i := 1; i <= 2; i++ {
		r := tn.chat(tok, chatOpts{})
		assertStatus(t, r, 200)
		tn.waitLog(r.header.Get("X-Router0-Request-Id"), 1)
	}
	if n := e.or.ChatCalls("mock-ok-or"); n != 2 {
		t.Fatalf("chat calls = %d, want 2", n)
	}
	k := tn.keyByLabel("or-main")
	if math.Abs(num(k["month_spend_usd"])-0.0012) > 1e-9 || str(dig(k, "state", "reason")) != "budget_exhausted" {
		t.Fatalf("or-main after two requests: spend=%v state=%v", k["month_spend_usd"], k["state"])
	}

	r = tn.chat(tok, chatOpts{})
	j := assertErr(t, r, 402, "insufficient_credits", "insufficient_credits")
	if n := e.or.ChatCalls("mock-ok-or"); n != 2 {
		t.Fatalf("over-budget key was called (calls=%d)", n)
	}
	if str(dig(j, "error", "router0", "attempts", 0, "outcome")) != "skipped" {
		t.Fatalf("attempts: %s", r)
	}
	row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 1)
	if str(dig(row, "trail", 0, "skip_reason")) != "budget_exhausted" {
		t.Fatalf("trail: %v", row["trail"])
	}
}

func TestPhase3_BudgetCleared(t *testing.T) {
	e, tn, tok := fixture(t, nil, []string{"mock-ok-or"})
	tn.priceOpenRouterModel()
	id := str(tn.keyByLabel("or-main")["id"])
	assertStatus(t, tn.api("PATCH", "/api/keys/"+id, J{"monthly_budget_usd": 0}), 200)
	assertErr(t, tn.chat(tok, chatOpts{}), 402, "insufficient_credits", "insufficient_credits")

	r := tn.api("PATCH", "/api/keys/"+id, J{"monthly_budget_usd": nil})
	assertStatus(t, r, 200)
	if v, present := r.json()["monthly_budget_usd"]; !present || v != nil {
		t.Fatalf("budget not cleared: %s", r)
	}
	assertStatus(t, tn.chat(tok, chatOpts{}), 200)
	if n := e.or.ChatCalls("mock-ok-or"); n != 1 {
		t.Fatalf("chat calls = %d, want 1", n)
	}
	// label-only PATCH keeps the (cleared) budget and renames the key
	r = tn.api("PATCH", "/api/keys/"+id, J{"label": "or-renamed"})
	assertStatus(t, r, 200)
	if str(r.json()["label"]) != "or-renamed" || r.json()["monthly_budget_usd"] != nil {
		t.Fatalf("label PATCH: %s", r)
	}
}

func TestPhase3_SpendReconcile(t *testing.T) {
	e, tn, tok := fixture(t, nil, []string{"mock-ok-or"})
	tn.priceOpenRouterModel()
	id := str(tn.keyByLabel("or-main")["id"])
	for i := 0; i < 3; i++ {
		r := tn.chat(tok, chatOpts{})
		assertStatus(t, r, 200)
		tn.waitLog(r.header.Get("X-Router0-Request-Id"), 1)
	}
	ctx := context.Background()
	spend := func() int64 {
		var v int64
		if err := e.pool.QueryRow(ctx, `SELECT COALESCE(SUM(spend_micro_usd),0) FROM provider_key_spend WHERE provider_key_id=$1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if got := spend(); got != 1800 {
		t.Fatalf("spend = %d µUSD, want 1800", got)
	}
	if _, err := e.pool.Exec(ctx, `DELETE FROM provider_key_spend WHERE provider_key_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.Reconcile(ctx, e.pool, time.Now(), tn.orgID); err != nil {
		t.Fatal(err)
	}
	if got := spend(); got < 1799 || got > 1801 {
		t.Fatalf("after reconcile spend = %d µUSD, want 1800±1", got)
	}
}

func TestPhase3_ResetKeyState(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-429-30-a", "mock-ok-b"}, nil)
	assertStatus(t, tn.chat(tok, chatOpts{}), 200)
	k := tn.keyByLabel("groq-a")
	if str(dig(k, "state", "reason")) != "rate_limited" {
		t.Fatalf("groq-a not rate-limited: %v", k["state"])
	}
	id := str(k["id"])

	if r := tn.newMember().api("POST", "/api/keys/"+id+"/reset-state", nil); r.status != 403 {
		t.Fatalf("member reset: %s", r)
	}
	assertErr(t, e.newTenant("other").api("POST", "/api/keys/"+id+"/reset-state", nil), 404, "not_found", "key_not_found")

	r := tn.api("POST", "/api/keys/"+id+"/reset-state", nil)
	assertStatus(t, r, 204)
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM provider_key_state WHERE provider_key_id=$1`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("provider_key_state rows after reset: %d (%v)", n, err)
	}
	if dig(tn.keyByLabel("groq-a"), "state", "blocked") == true {
		t.Fatal("groq-a still shows a state after reset")
	}
	// The reset key is attempted again immediately (it 429s again, b serves).
	assertStatus(t, tn.chat(tok, chatOpts{}), 200)
	if c := e.groq.ChatCalls("mock-429-30-a"); c != 2 {
		t.Fatalf("groq-a calls = %d, want 2 (reset must evict the cache)", c)
	}
}

func TestPhase3_LegacyTablesDropped(t *testing.T) {
	_, tn, tok := fixture(t, []string{"mock-ok-a"}, nil)
	for _, tbl := range []string{"pool_scores", "model_perf_stats", "rate_limit_observations"} {
		var reg *string
		if err := tn.e.pool.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, tbl).Scan(&reg); err != nil {
			t.Fatal(err)
		}
		if reg != nil {
			t.Fatalf("legacy table %s still exists", tbl)
		}
	}
	assertStatus(t, tn.chat(tok, chatOpts{}), 200)
	// The platform admin model registry no longer depends on model_perf_stats.
	assertStatus(t, tn.e.do("GET", "/admin/models", tn.e.admin, nil, nil), 200)
}

// metricValue returns the value of the first /metrics sample whose line starts
// with prefix (metric name plus label set), or -1 when absent.
func metricValue(t *testing.T, e *env, prefix string) float64 {
	t.Helper()
	m := e.do("GET", "/metrics", "", nil, nil)
	assertStatus(t, m, 200)
	for _, line := range strings.Split(string(m.body), "\n") {
		if strings.HasPrefix(line, prefix+" ") {
			v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, prefix)), 64)
			if err != nil {
				t.Fatalf("metric line %q: %v", line, err)
			}
			return v
		}
	}
	return -1
}

func TestPhase3_Metrics(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-ok-g"}, []string{"mock-ok-or"})
	tn.orFirst()
	tn.priceOpenRouterModel()
	const (
		budgetSkips = `router0_key_skips_total{provider="openrouter",reason="budget_exhausted"}`
		creditSkips = `router0_key_skips_total{provider="openrouter",reason="credits_exhausted"}`
		spend       = `router0_spend_usd_total{provider="openrouter"}`
		polls       = `router0_balance_polls_total{provider="openrouter",result="exhausted"}`
		reconciles  = `router0_spend_reconcile_total{status="ok"}`
	)
	base := map[string]float64{}
	for _, m := range []string{budgetSkips, creditSkips, spend, polls, reconciles} {
		base[m] = math.Max(0, metricValue(t, e, m))
	}

	// One paid request on OpenRouter, then a $0 budget makes it skip.
	r := tn.chat(tok, chatOpts{})
	assertStatus(t, r, 200)
	tn.waitLog(r.header.Get("X-Router0-Request-Id"), 1)
	id := str(tn.keyByLabel("or-main")["id"])
	assertStatus(t, tn.api("PATCH", "/api/keys/"+id, J{"monthly_budget_usd": 0}), 200)
	assertStatus(t, tn.chat(tok, chatOpts{}), 200) // groq serves
	// Remove the budget; a $0 balance poll makes it skip for credits instead.
	assertStatus(t, tn.api("PATCH", "/api/keys/"+id, J{"monthly_budget_usd": nil}), 200)
	e.or.SetBalance(0)
	assertStatus(t, tn.api("POST", "/api/keys/poll-balances", nil), 200)
	assertStatus(t, tn.chat(tok, chatOpts{}), 200)
	if _, err := metadata.Reconcile(context.Background(), e.pool, time.Now(), tn.orgID); err != nil {
		t.Fatal(err)
	}

	for m, want := range map[string]float64{budgetSkips: 1, creditSkips: 1, polls: 1, reconciles: 1} {
		if got := metricValue(t, e, m) - base[m]; got < want {
			t.Errorf("%s increased by %v, want >= %v", m, got, want)
		}
	}
	// Other tests in this binary may add OpenRouter spend too, so this is a lower bound.
	if got := metricValue(t, e, spend) - base[spend]; got < 0.0006-1e-9 {
		t.Errorf("%s increased by %v, want >= 0.0006", spend, got)
	}
}
