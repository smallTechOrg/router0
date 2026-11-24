//go:build integration

package integration

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/zer0-router/zer0/internal/metadata"
)

// tokenByName returns the GET /api/tokens entry with the name (fails if absent).
func (tn *tenant) tokenByName(name string) J {
	tn.e.t.Helper()
	r := tn.api("GET", "/api/tokens", nil)
	assertStatus(tn.e.t, r, 200)
	for _, tk := range list(r.json()["tokens"]) {
		if str(dig(tk, "name")) == name && dig(tk, "revoked_at") == nil {
			return tk.(J)
		}
	}
	tn.e.t.Fatalf("token %q not in GET /api/tokens: %s", name, r)
	return nil
}

func TestPhase4_TokenBudget(t *testing.T) {
	e, tn, tok := fixture(t, nil, []string{"mock-ok-or"})
	tn.priceOpenRouterModel() // $0.0006 per mock request
	other, _ := tn.token("other-app")
	_, id := tn.tokenIDByName("svc")

	// validation and authorization
	assertErr(t, tn.api("PATCH", "/api/tokens/"+id, J{"monthly_budget_usd": -1}), 422, "validation_error", "invalid_budget")
	assertErr(t, tn.api("PATCH", "/api/tokens/"+id, J{"name": "x"}), 422, "validation_error", "unknown_field")
	assertErr(t, tn.api("PATCH", "/api/tokens/"+id, J{}), 422, "validation_error", "missing_fields")
	assertErr(t, tn.api("PATCH", "/api/tokens/00000000-0000-0000-0000-000000000000", J{"monthly_budget_usd": 1}), 404, "not_found", "token_not_found")
	if r := tn.newMember().api("PATCH", "/api/tokens/"+id, J{"monthly_budget_usd": 1}); r.status != 403 {
		t.Fatalf("member PATCH: %s", r)
	}
	assertErr(t, e.newTenant("other").api("PATCH", "/api/tokens/"+id, J{"monthly_budget_usd": 1}), 404, "not_found", "token_not_found")

	r := tn.api("PATCH", "/api/tokens/"+id, J{"monthly_budget_usd": 0.001})
	assertStatus(t, r, 200)
	if num(r.json()["monthly_budget_usd"]) != 0.001 || str(r.json()["name"]) != "svc" {
		t.Fatalf("PATCH response: %s", r)
	}
	if _, present := r.json()["token"]; present {
		t.Fatalf("PATCH response must never contain the secret: %s", r)
	}

	// $0.0006 per request: the 1st and 2nd are served, the 3rd is refused.
	for i := 0; i < 2; i++ {
		r := tn.chat(tok, chatOpts{})
		assertStatus(t, r, 200)
		tn.waitLog(r.header.Get("X-Router0-Request-Id"), 1)
	}
	if got := num(tn.tokenByName("svc")["month_spend_usd"]); math.Abs(got-0.0012) > 1e-9 {
		t.Fatalf("month_spend_usd = %v, want 0.0012", got)
	}
	calls := e.or.ChatCalls("mock-ok-or")
	r = tn.chat(tok, chatOpts{})
	j := assertErr(t, r, 402, "insufficient_credits", "token_budget_exceeded")
	if e.or.ChatCalls("mock-ok-or") != calls {
		t.Fatal("an over-budget token reached the provider")
	}
	if len(list(dig(j, "error", "router0", "attempts"))) != 0 {
		t.Fatalf("refused before routing, attempts must be empty: %s", r)
	}
	row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 0)
	if str(row["error_code"]) != "token_budget_exceeded" {
		t.Fatalf("request log: %v", row)
	}

	// Another token of the same org is not affected.
	assertStatus(t, tn.chat(other, chatOpts{}), 200)

	// Raising the budget takes effect immediately.
	assertStatus(t, tn.api("PATCH", "/api/tokens/"+id, J{"monthly_budget_usd": nil}), 200)
	assertStatus(t, tn.chat(tok, chatOpts{}), 200)
}

func TestPhase4_TokenBudgetZeroAndRevoked(t *testing.T) {
	_, tn, tok := fixture(t, []string{"mock-ok-a"}, nil)
	_, id := tn.tokenIDByName("svc")
	assertStatus(t, tn.api("PATCH", "/api/tokens/"+id, J{"monthly_budget_usd": 0}), 200)
	assertErr(t, tn.chat(tok, chatOpts{}), 402, "insufficient_credits", "token_budget_exceeded")

	assertStatus(t, tn.api("DELETE", "/api/tokens/"+id, nil), 204)
	assertErr(t, tn.api("PATCH", "/api/tokens/"+id, J{"monthly_budget_usd": 5}), 404, "not_found", "token_not_found")
}

func TestPhase4_TokenSpendReconcile(t *testing.T) {
	e, tn, tok := fixture(t, nil, []string{"mock-ok-or"})
	tn.priceOpenRouterModel()
	_, id := tn.tokenIDByName("svc")
	for i := 0; i < 3; i++ {
		r := tn.chat(tok, chatOpts{})
		assertStatus(t, r, 200)
		tn.waitLog(r.header.Get("X-Router0-Request-Id"), 1)
	}
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `DELETE FROM service_token_spend WHERE service_token_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if got := num(tn.tokenByName("svc")["month_spend_usd"]); got != 0 {
		t.Fatalf("spend after delete = %v", got)
	}
	if _, err := metadata.Reconcile(ctx, e.pool, time.Now(), tn.orgID); err != nil {
		t.Fatal(err)
	}
	if got := num(tn.tokenByName("svc")["month_spend_usd"]); math.Abs(got-0.0018) > 1e-9 {
		t.Fatalf("spend after reconcile = %v, want 0.0018", got)
	}
}

// tokenIDByName returns (name, id) of a live token.
func (tn *tenant) tokenIDByName(name string) (string, string) {
	tn.e.t.Helper()
	tk := tn.tokenByName(name)
	return name, str(tk["id"])
}
