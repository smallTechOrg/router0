//go:build integration

package integration

import (
	"sort"
	"testing"
)

// v1ModelIDs lists the ids GET /v1/models returns for a service token.
func (tn *tenant) v1ModelIDs(token string) []string {
	tn.e.t.Helper()
	r := tn.e.do("GET", "/v1/models", token, nil, nil)
	assertStatus(tn.e.t, r, 200)
	var ids []string
	for _, m := range list(r.json()["data"]) {
		ids = append(ids, str(dig(m, "id")))
	}
	sort.Strings(ids)
	return ids
}

func TestPhase5_TokenModelAllowlist(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-ok-a"}, nil)
	tn.waitCatalog("llama-3.1-8b", "groq")
	_, id := tn.tokenIDByName("svc")
	if got := tn.v1ModelIDs(tok); !eqStrings(got, []string{"llama-3.1-8b", "llama-3.3-70b"}) {
		t.Fatalf("unrestricted /v1/models = %v", got)
	}

	// validation
	assertErr(t, tn.api("PATCH", "/api/tokens/"+id, J{"allowed_models": []string{}}), 422, "validation_error", "invalid_allowed_models")
	assertErr(t, tn.api("PATCH", "/api/tokens/"+id, J{"allowed_models": "llama"}), 422, "validation_error", "invalid_allowed_models")
	assertErr(t, tn.api("PATCH", "/api/tokens/"+id, J{"allowed_models": []string{" "}}), 422, "validation_error", "invalid_allowed_models")

	// A provider-specific id is stored canonicalised and de-duplicated.
	r := tn.api("PATCH", "/api/tokens/"+id, J{"allowed_models": []string{"llama-3.3-70b-versatile", "Llama-3.3-70B"}})
	assertStatus(t, r, 200)
	if got := list(r.json()["allowed_models"]); len(got) != 1 || str(got[0]) != "llama-3.3-70b" {
		t.Fatalf("allowed_models = %v", r.json()["allowed_models"])
	}
	assertStatus(t, tn.chat(tok, chatOpts{model: "llama-3.3-70b-versatile"}), 200)
	calls := e.groq.TotalChatCalls()
	j := assertErr(t, tn.chat(tok, chatOpts{model: "llama-3.1-8b"}), 403, "invalid_request_error", "model_not_allowed")
	if e.groq.TotalChatCalls() != calls {
		t.Fatal("a disallowed model reached the provider")
	}
	if str(dig(j, "error", "param")) != "model" {
		t.Fatalf("error param: %v", j)
	}
	if got := tn.v1ModelIDs(tok); !eqStrings(got, []string{"llama-3.3-70b"}) {
		t.Fatalf("restricted /v1/models = %v", got)
	}

	// Other tokens of the org are unaffected.
	other, _ := tn.token("other")
	assertStatus(t, tn.chat(other, chatOpts{model: "llama-3.1-8b"}), 200)

	// A budget-only PATCH leaves the allowlist alone.
	r = tn.api("PATCH", "/api/tokens/"+id, J{"monthly_budget_usd": 5})
	assertStatus(t, r, 200)
	if len(list(r.json()["allowed_models"])) != 1 {
		t.Fatalf("budget PATCH changed the allowlist: %s", r)
	}

	// null removes the restriction.
	r = tn.api("PATCH", "/api/tokens/"+id, J{"allowed_models": nil})
	assertStatus(t, r, 200)
	if v, present := r.json()["allowed_models"]; !present || v != nil {
		t.Fatalf("allowlist not cleared: %s", r)
	}
	assertStatus(t, tn.chat(tok, chatOpts{model: "llama-3.1-8b"}), 200)
}

func TestPhase5_TokenAllowlistAliases(t *testing.T) {
	_, tn, tok := fixture(t, []string{"mock-ok-a"}, nil)
	tn.waitCatalog("llama-3.1-8b", "groq")
	_, id := tn.tokenIDByName("svc")
	assertStatus(t, tn.api("POST", "/api/aliases", J{"alias": "fast", "canonical_model": "llama-3.1-8b"}), 201)

	// Allowing only the alias: the alias works, the model's own name does not,
	// and /v1/models shows just the alias.
	assertStatus(t, tn.api("PATCH", "/api/tokens/"+id, J{"allowed_models": []string{"fast"}}), 200)
	assertStatus(t, tn.chat(tok, chatOpts{model: "fast"}), 200)
	assertErr(t, tn.chat(tok, chatOpts{model: "llama-3.1-8b"}), 403, "invalid_request_error", "model_not_allowed")
	if got := tn.v1ModelIDs(tok); !eqStrings(got, []string{"fast"}) {
		t.Fatalf("/v1/models = %v, want [fast]", got)
	}

	// Allowing the model: every name that resolves to it works, including the alias.
	assertStatus(t, tn.api("PATCH", "/api/tokens/"+id, J{"allowed_models": []string{"llama-3.1-8b"}}), 200)
	assertStatus(t, tn.chat(tok, chatOpts{model: "fast"}), 200)
	assertStatus(t, tn.chat(tok, chatOpts{model: "llama-3.1-8b-instant"}), 200)
	assertErr(t, tn.chat(tok, chatOpts{model: "llama-3.3-70b"}), 403, "invalid_request_error", "model_not_allowed")
	if got := tn.v1ModelIDs(tok); !eqStrings(got, []string{"fast", "llama-3.1-8b"}) {
		t.Fatalf("/v1/models = %v, want [fast llama-3.1-8b]", got)
	}
}
