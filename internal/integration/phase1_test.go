//go:build integration

package integration

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/testutil/mockprovider"
)

// fixture: tenant with a service token and the given Groq / OpenRouter keys
// (Groq keys first, then OpenRouter), catalog warm for llama-3.3-70b.
func fixture(t *testing.T, groqKeys []string, orKeys []string) (*env, *tenant, string) {
	t.Helper()
	e := newEnv(t)
	tn := e.newTenant("fx")
	var provs []string
	labels := map[string]int{}
	for _, k := range groqKeys {
		labels["groq"]++
		tn.addKey("groq", k, "groq-"+string(rune('a'+labels["groq"]-1)))
	}
	if len(groqKeys) > 0 {
		provs = append(provs, "groq")
	}
	for _, k := range orKeys {
		labels["or"]++
		lbl := "or-main"
		if labels["or"] > 1 {
			lbl = "or-" + strconv.Itoa(labels["or"])
		}
		tn.addKey("openrouter", k, lbl)
	}
	if len(orKeys) > 0 {
		provs = append(provs, "openrouter")
	}
	tn.waitCatalog("llama-3.3-70b", provs...)
	tok, _ := tn.token("svc")
	return e, tn, tok
}

var tokenRe = regexp.MustCompile(`^r0-sk-[1-9A-HJ-NP-Za-km-z]{32}$`)

func TestPhase1_RegisterCreatesOrg(t *testing.T) {
	e := newEnv(t)
	email := uniq("owner") + "@example.test"
	invite := e.invite(email, "", "")

	// short password -> 422 (invite stays usable)
	assertErr(t, e.register(email, "short-pw-10", "", invite), 422, "validation_error", "password_too_short")

	orgName := "Acme " + uniq("org")
	r := e.register(email, "correct-horse-battery", orgName, invite)
	assertStatus(t, r, 201)
	j := r.json()
	if _, has := j["zer0_key"]; has {
		t.Fatalf("register response must not carry zer0_key: %s", r)
	}
	if str(dig(j, "org", "role")) != "owner" || str(dig(j, "org", "name")) != orgName || str(j["access_token"]) == "" {
		t.Fatalf("unexpected register body: %s", r)
	}
	orgID := str(dig(j, "org", "id"))
	userID := str(dig(j, "user", "id"))
	var n int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM org_members m JOIN orgs o ON o.id = m.org_id WHERE m.user_id = $1 AND o.id = $2 AND m.role = 'owner'`,
		userID, orgID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("org_members owner row: n=%d err=%v", n, err)
	}

	// invite reuse -> 409; unknown -> 400
	assertErr(t, e.register(uniq("x")+"@example.test", "correct-horse-battery", "", invite), 409, "conflict", "invite_used")
	assertErr(t, e.register(uniq("x")+"@example.test", "correct-horse-battery", "", "nope"), 400, "validation_error", "invalid_invite")
	// expired -> 410
	exp := e.invite(uniq("exp")+"@example.test", "", "")
	if _, err := e.pool.Exec(context.Background(), `UPDATE invites SET expires_at = NOW() - INTERVAL '1 hour' WHERE token = $1`, exp); err != nil {
		t.Fatal(err)
	}
	assertErr(t, e.register(uniq("x")+"@example.test", "correct-horse-battery", "", exp), 410, "gone", "invite_expired")

	// GET /auth/me returns the org (dashboard shape)
	owner := &tenant{e: e, orgID: orgID, userID: userID, jwt: str(j["access_token"])}
	me := owner.api("GET", "/auth/me", nil)
	assertStatus(t, me, 200)
	if str(dig(me.json(), "org", "id")) != orgID || str(dig(me.json(), "org", "role")) != "owner" ||
		str(dig(me.json(), "org", "name")) != orgName || str(dig(me.json(), "email")) != email {
		t.Fatalf("/auth/me: %s", me)
	}

	// invite into the existing org as member -> member; writes are 403, reads 200
	member := owner.newMember()
	if member.orgID != orgID {
		t.Fatalf("member joined org %s, want %s", member.orgID, orgID)
	}
	mj := member.api("GET", "/auth/me", nil).json()
	if str(dig(mj, "org", "role")) != "member" {
		t.Fatalf("member role: %v", mj)
	}
	assertErr(t, member.api("POST", "/api/tokens", J{"name": "nope"}), 403, "forbidden", "org_role_required")
	assertErr(t, member.api("POST", "/api/keys", J{"provider": "groq", "key": "mock-ok-x", "label": "x"}), 403, "forbidden", "org_role_required")
	assertErr(t, member.api("PUT", "/api/routing/priority", J{"providers": []string{}}), 403, "forbidden", "org_role_required")
	assertErr(t, member.api("POST", "/api/catalog/refresh", J{}), 403, "forbidden", "org_role_required")
	assertStatus(t, member.api("GET", "/api/tokens", nil), 200)
	assertStatus(t, member.api("GET", "/api/keys", nil), 200)

	// a JWT without org_id still works (org resolved from membership)
	legacyJWT, _ := auth.IssueAccessToken(e.secret, userID, email, "user", "")
	assertStatus(t, e.do("GET", "/api/keys", legacyJWT, nil, nil), 200)

	// non-admin cannot use /admin; admin sees the org with counts (dashboard shape)
	assertStatus(t, owner.api("GET", "/admin/orgs", nil), 403)
	ao := e.do("GET", "/admin/orgs", e.admin, nil, nil)
	assertStatus(t, ao, 200)
	found := false
	for _, o := range list(ao.json()["orgs"]) {
		if str(dig(o, "id")) == orgID {
			found = true
			if num(dig(o, "member_count")) != 2 || dig(o, "token_count") == nil || dig(o, "key_count") == nil {
				t.Fatalf("admin org counts: %v", o)
			}
		}
	}
	if !found {
		t.Fatalf("org %s not in /admin/orgs", orgID)
	}
	au := e.do("GET", "/admin/users", e.admin, nil, nil)
	assertStatus(t, au, 200)
	inv := e.do("POST", "/admin/invites", e.admin, J{"email": uniq("i") + "@example.test"}, nil)
	if inv.json()["token"] == nil || inv.json()["expires_at"] == nil {
		t.Fatalf("invite shape: %s", inv)
	}
}

func TestPhase1_TokenLifecycle(t *testing.T) {
	e, tn, _ := fixture(t, []string{"mock-ok-a"}, nil)

	r := tn.api("POST", "/api/tokens", J{"name": "svc-a"})
	assertStatus(t, r, 201)
	tokA := str(r.json()["token"])
	idA := str(r.json()["id"])
	if !tokenRe.MatchString(tokA) {
		t.Fatalf("token format: %q", tokA)
	}
	var hash, alg string
	if err := e.pool.QueryRow(context.Background(), `SELECT token_hash, hash_alg FROM service_tokens WHERE id = $1`, idA).Scan(&hash, &alg); err != nil {
		t.Fatal(err)
	}
	if len(hash) != 64 || alg != "sha256" || strings.Contains(hash, tokA) {
		t.Fatalf("stored hash %q alg %q", hash, alg)
	}
	assertErr(t, tn.api("POST", "/api/tokens", J{"name": "svc-a"}), 409, "conflict", "token_name_exists")
	assertStatus(t, tn.api("POST", "/api/tokens", J{"name": "bad name!"}), 422)

	tokB, idB := tn.token("svc-b")

	// list never carries the secret
	l := tn.api("GET", "/api/tokens", nil)
	assertStatus(t, l, 200)
	if strings.Contains(string(l.body), tokA) || strings.Contains(string(l.body), `"token":`) {
		t.Fatalf("token list leaks secret: %s", l)
	}

	// both tokens work; the wrong token is 401
	okA := tn.chat(tokA, chatOpts{})
	assertStatus(t, okA, 200)
	assertStatus(t, tn.chat(tokB, chatOpts{}), 200)
	bogus := "r0-sk-" + strings.Repeat("z", 32)
	assertErr(t, tn.chat(bogus, chatOpts{}), 401, "authentication_error", "invalid_api_key")
	assertStatus(t, e.do("POST", "/v1/chat/completions", "", J{"model": "x"}, nil), 401)

	// request log carries the right token_name
	row := tn.waitLog(okA.header.Get("X-Router0-Request-Id"), 1)
	if str(row["token_name"]) != "svc-a" {
		t.Fatalf("token_name = %v", row["token_name"])
	}

	// revoke A: the very next request is 401, B is unaffected
	assertStatus(t, tn.api("DELETE", "/api/tokens/"+idA, nil), 204)
	assertErr(t, tn.chat(tokA, chatOpts{}), 401, "authentication_error", "invalid_api_key")
	assertStatus(t, tn.chat(tokB, chatOpts{}), 200)
	assertStatus(t, tn.api("DELETE", "/api/tokens/"+idA, nil), 204) // idempotent
	_ = idB
	// revoked tokens stay listed
	l = tn.api("GET", "/api/tokens", nil)
	revoked := false
	for _, x := range list(l.json()["tokens"]) {
		if str(dig(x, "id")) == idA && dig(x, "revoked_at") != nil {
			revoked = true
		}
	}
	if !revoked {
		t.Fatalf("revoked token not listed as revoked: %s", l)
	}
	// the name can be reused once revoked
	assertStatus(t, tn.api("POST", "/api/tokens", J{"name": "svc-a"}), 201)
}

func TestPhase1_LegacyTokenAccepted(t *testing.T) {
	e, tn, _ := fixture(t, []string{"mock-ok-a"}, nil)
	full, _, err := auth.GenerateServiceToken() // random base58 body, so reruns never collide on prefix
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimPrefix(full, auth.ServiceTokenScheme)
	raw := auth.LegacyTokenScheme + body
	h, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO service_tokens (org_id, name, token_prefix, token_hash, hash_alg, created_by) VALUES ($1,'legacy-default',$2,$3,'bcrypt',$4)`,
		tn.orgID, body[:auth.TokenPrefixLen], string(h), tn.userID); err != nil {
		t.Fatal(err)
	}
	r := tn.chat(raw, chatOpts{})
	assertStatus(t, r, 200)
	assertErr(t, tn.chat(auth.LegacyTokenScheme+strings.Repeat("z", 32), chatOpts{}), 401, "authentication_error", "invalid_api_key")

	l := tn.api("GET", "/api/tokens", nil)
	ok := false
	for _, x := range list(l.json()["tokens"]) {
		if str(dig(x, "name")) == "legacy-default" && dig(x, "legacy") == true {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("legacy-default not listed with legacy:true: %s", l)
	}
	row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 1)
	if str(row["token_name"]) != "legacy-default" {
		t.Fatalf("token_name = %v", row["token_name"])
	}
}

func TestPhase1_CatalogDiscovery(t *testing.T) {
	e := newEnv(t)
	tn := e.newTenant("cat")
	tok, _ := tn.token("svc")

	// key with a 401 at listing time is marked error; discovery succeeds with the next key
	badID := tn.addKey("groq", "mock-401-bad", "groq-bad")
	waitFor(t, 5*time.Second, "bad key marked error", func() bool {
		for _, k := range list(tn.api("GET", "/api/keys", nil).json()["keys"]) {
			if str(dig(k, "id")) == badID && str(dig(k, "status")) == "error" {
				return true
			}
		}
		return false
	})
	tn.addKey("groq", "mock-ok-good", "groq-good")
	tn.addKey("openrouter", "mock-ok-or", "or-main")
	tn.waitCatalog("llama-3.3-70b", "groq", "openrouter")

	// /v1/models (service token) lists the canonical model with both providers
	m := tn.e.do("GET", "/v1/models", tok, nil, nil)
	assertStatus(t, m, 200)
	var provs []string
	for _, d := range list(m.json()["data"]) {
		if str(dig(d, "id")) == "llama-3.3-70b" {
			for _, p := range list(dig(d, "router0", "providers")) {
				provs = append(provs, str(dig(p, "provider"))+"="+str(dig(p, "provider_model_id")))
			}
		}
	}
	if !eqStrings(provs, []string{"groq=llama-3.3-70b-versatile", "openrouter=meta-llama/llama-3.3-70b-instruct"}) {
		t.Fatalf("v1/models providers: %v (%s)", provs, m)
	}

	// a provider-specific name resolves to the canonical model; Groq first by priority
	r := tn.chat(tok, chatOpts{model: "meta-llama/llama-3.3-70b-instruct"})
	assertStatus(t, r, 200)
	if r.header.Get("X-Router0-Provider") != "groq" || r.header.Get("X-Router0-Model") != "llama-3.3-70b-versatile" ||
		r.header.Get("X-Router0-Canonical-Model") != "llama-3.3-70b" || r.header.Get("X-Router0-Key") != "groq-good" {
		t.Fatalf("headers: %v", r.header)
	}
	if str(r.json()["model"]) != "llama-3.3-70b" {
		t.Fatalf("response model = %v", r.json()["model"])
	}

	// the Groq listing drops the model; refresh makes it unavailable on Groq
	e.groq.SetModels("llama-3.1-8b-instant")
	rr := tn.api("POST", "/api/catalog/refresh", J{"provider": "groq"})
	assertStatus(t, rr, 200)
	if list(rr.json()["refreshes"]) == nil {
		t.Fatalf("refresh body: %s", rr)
	}
	var avail int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM org_model_catalog WHERE org_id=$1 AND provider='groq' AND canonical_model='llama-3.3-70b' AND is_available`, tn.orgID).Scan(&avail); err != nil || avail != 0 {
		t.Fatalf("groq row still available: n=%d err=%v", avail, err)
	}
	before := e.groq.TotalChatCalls()
	r2 := tn.chat(tok, chatOpts{})
	assertStatus(t, r2, 200)
	if r2.header.Get("X-Router0-Provider") != "openrouter" || e.groq.TotalChatCalls() != before {
		t.Fatalf("expected openrouter without calling groq: %v groq calls %d->%d", r2.header, before, e.groq.TotalChatCalls())
	}
}

func TestPhase1_PriorityOrder(t *testing.T) {
	_, tn, tok := fixture(t, []string{"mock-ok-a"}, []string{"mock-ok-or"})

	r := tn.chat(tok, chatOpts{})
	assertStatus(t, r, 200)
	if r.header.Get("X-Router0-Provider") != "groq" {
		t.Fatalf("default order should serve groq: %v", r.header)
	}
	p := tn.api("GET", "/api/routing/priority", nil)
	assertStatus(t, p, 200)
	if str(dig(p.json(), "providers", 0, "provider")) != "groq" || str(dig(p.json(), "providers", 1, "provider")) != "openrouter" {
		t.Fatalf("priority: %s", p)
	}
	groqKey := str(dig(p.json(), "providers", 0, "keys", 0, "id"))

	// invalid permutations change nothing
	assertErr(t, tn.api("PUT", "/api/routing/priority", J{"providers": []string{"groq"}}), 422, "validation_error", "invalid_priority")
	assertErr(t, tn.api("PUT", "/api/routing/priority", J{"providers": []string{"groq", "openrouter"},
		"key_order": J{"groq": []string{"00000000-0000-0000-0000-000000000001"}}}), 422, "validation_error", "invalid_key_order")
	_ = groqKey
	if str(dig(tn.api("GET", "/api/routing/priority", nil).json(), "providers", 0, "provider")) != "groq" {
		t.Fatal("failed PUT changed priority")
	}

	// move OpenRouter first
	assertStatus(t, tn.api("PUT", "/api/routing/priority", J{"providers": []string{"openrouter", "groq"}}), 200)
	r = tn.chat(tok, chatOpts{})
	assertStatus(t, r, 200)
	if r.header.Get("X-Router0-Provider") != "openrouter" || r.header.Get("X-Router0-Attempts") != "1" {
		t.Fatalf("after reorder: %v", r.header)
	}
	row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 1)
	if str(dig(row, "trail", 0, "provider")) != "openrouter" {
		t.Fatalf("trail: %v", row["trail"])
	}
}

func TestPhase1_KeyOrderFailover(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-500-a", "mock-ok-b"}, nil)

	p := tn.api("GET", "/api/routing/priority", nil)
	pj := p.json()
	if str(dig(pj, "providers", 0, "provider")) != "groq" ||
		str(dig(pj, "providers", 0, "keys", 0, "label")) != "groq-a" || num(dig(pj, "providers", 0, "keys", 0, "position")) != 0 ||
		str(dig(pj, "providers", 0, "keys", 1, "label")) != "groq-b" || num(dig(pj, "providers", 0, "keys", 1, "position")) != 1 {
		t.Fatalf("key positions: %s", p)
	}
	k1 := str(dig(pj, "providers", 0, "keys", 0, "id"))
	k2 := str(dig(pj, "providers", 0, "keys", 1, "id"))

	r := tn.chat(tok, chatOpts{})
	assertStatus(t, r, 200)
	if r.header.Get("X-Router0-Key") != "groq-b" || r.header.Get("X-Router0-Attempts") != "2" {
		t.Fatalf("headers: %v", r.header)
	}
	row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 2)
	if !eqStrings(trailOf(row), []string{"groq/groq-a:error/transient", "groq/groq-b:success"}) {
		t.Fatalf("trail: %v", trailOf(row))
	}
	if e.groq.ChatCalls("mock-500-a") != 1 || e.groq.ChatCalls("mock-ok-b") != 1 {
		t.Fatal("unexpected upstream call counts")
	}

	// swap key order: b first, one attempt
	assertStatus(t, tn.api("PUT", "/api/routing/priority", J{"providers": []string{"groq"}, "key_order": J{"groq": []string{k2, k1}}}), 200)
	r = tn.chat(tok, chatOpts{})
	assertStatus(t, r, 200)
	if r.header.Get("X-Router0-Key") != "groq-b" || r.header.Get("X-Router0-Attempts") != "1" {
		t.Fatalf("after key reorder: %v", r.header)
	}

	// deleting a key removes it from plans; no response leaks raw keys
	assertStatus(t, tn.api("DELETE", "/api/keys/"+k1, nil), 204)
	for _, path := range []string{"/api/keys", "/api/routing/priority"} {
		b := string(tn.api("GET", path, nil).body)
		if strings.Contains(b, "mock-500-a") || strings.Contains(b, "mock-ok-b") {
			t.Fatalf("%s leaks a raw key: %s", path, b)
		}
	}
	before := e.groq.ChatCalls("mock-500-a")
	assertStatus(t, tn.chat(tok, chatOpts{}), 200)
	if e.groq.ChatCalls("mock-500-a") != before {
		t.Fatal("deleted key was still attempted")
	}
}

func TestPhase1_ProviderFailover(t *testing.T) {
	_, tn, tok := fixture(t, []string{"mock-429-30-a", "mock-429-30-b"}, []string{"mock-ok-or"})
	r := tn.chat(tok, chatOpts{})
	assertStatus(t, r, 200)
	if r.header.Get("X-Router0-Provider") != "openrouter" || r.header.Get("X-Router0-Attempts") != "3" {
		t.Fatalf("headers: %v", r.header)
	}
	row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 3)
	want := []string{"groq/groq-a:error/rate_limit", "groq/groq-b:error/rate_limit", "openrouter/or-main:success"}
	if !eqStrings(trailOf(row), want) {
		t.Fatalf("trail: %v", trailOf(row))
	}
}

func TestPhase1_HeaderOverride(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-ok-a"}, []string{"mock-ok-or"})

	r := tn.chat(tok, chatOpts{headers: map[string]string{"X-Router0-Providers": "openrouter"}})
	assertStatus(t, r, 200)
	if r.header.Get("X-Router0-Provider") != "openrouter" || e.groq.TotalChatCalls() != 0 {
		t.Fatalf("override ignored: %v groq calls=%d", r.header, e.groq.TotalChatCalls())
	}

	// order + case + whitespace
	r = tn.chat(tok, chatOpts{headers: map[string]string{"X-Router0-Providers": " OpenRouter , groq "}})
	assertStatus(t, r, 200)
	if r.header.Get("X-Router0-Provider") != "openrouter" {
		t.Fatalf("headers: %v", r.header)
	}

	// unknown provider -> 400, nothing called
	groqBefore, orBefore := e.groq.TotalChatCalls(), e.or.TotalChatCalls()
	assertErr(t, tn.chat(tok, chatOpts{headers: map[string]string{"X-Router0-Providers": "nosuch"}}), 400, "invalid_request_error", "invalid_provider_override")
	if e.groq.TotalChatCalls() != groqBefore || e.or.TotalChatCalls() != orBefore {
		t.Fatal("invalid override called a provider")
	}

	// a valid provider without an org key -> empty plan -> 404 (gemini is an alias of google)
	j := assertErr(t, tn.chat(tok, chatOpts{headers: map[string]string{"X-Router0-Providers": "gemini"}}), 404, "invalid_request_error", "model_not_found")
	if str(dig(j, "error", "param")) != "model" {
		t.Fatalf("param: %v", j)
	}
}

func TestPhase1_Streaming(t *testing.T) {
	_, tn, tok := fixture(t, []string{"mock-ok-a"}, nil)

	r := tn.chat(tok, chatOpts{stream: true})
	assertStatus(t, r, 200)
	if !strings.HasPrefix(r.header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type: %v", r.header)
	}
	s := parseSSE(r.body)
	if s.contentChunks() != 3 || !s.done || len(s.errors) != 0 {
		t.Fatalf("stream: content=%d done=%v errs=%d\n%s", s.contentChunks(), s.done, len(s.errors), r.body)
	}
	for _, c := range s.chunks {
		if str(c["model"]) != "llama-3.3-70b" {
			t.Fatalf("chunk model = %v", c["model"])
		}
		if c["usage"] != nil {
			t.Fatalf("usage chunk forwarded without include_usage: %v", c)
		}
	}
	if r.header.Get("X-Router0-Provider") != "groq" || r.header.Get("X-Router0-Canonical-Model") != "llama-3.3-70b" {
		t.Fatalf("headers: %v", r.header)
	}
	row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 1)
	if row["stream"] != true || str(row["status"]) != "success" || num(row["input_tokens"]) != 12 || num(row["output_tokens"]) != 7 {
		t.Fatalf("log row: %v", row)
	}

	// include_usage forwards the usage chunk
	r = tn.chat(tok, chatOpts{stream: true, usage: true})
	s = parseSSE(r.body)
	hasUsage := false
	for _, c := range s.chunks {
		if c["usage"] != nil {
			hasUsage = true
		}
	}
	if !hasUsage || !s.done {
		t.Fatalf("include_usage: %s", r.body)
	}
}

func TestPhase1_StreamFailoverBeforeFirstByte(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-503-a", "mock-ok-b"}, nil)
	r := tn.chat(tok, chatOpts{stream: true})
	assertStatus(t, r, 200)
	s := parseSSE(r.body)
	if s.contentChunks() != 3 || !s.done || len(s.errors) != 0 {
		t.Fatalf("stream: content=%d done=%v\n%s", s.contentChunks(), s.done, r.body)
	}
	row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 2)
	if !eqStrings(trailOf(row), []string{"groq/groq-a:error/transient", "groq/groq-b:success"}) || num(row["attempt_count"]) != 2 {
		t.Fatalf("trail: %v row=%v", trailOf(row), row)
	}
	if e.groq.ChatCalls("mock-503-a") != 1 {
		t.Fatal("key a should have been called once")
	}
}

func TestPhase1_StreamInterrupted(t *testing.T) {
	_, tn, tok := fixture(t, []string{"mock-midstream-a"}, nil)
	r := tn.chat(tok, chatOpts{stream: true})
	assertStatus(t, r, 200)
	s := parseSSE(r.body)
	if s.contentChunks() != 2 || !s.done || len(s.errors) != 1 {
		t.Fatalf("stream: content=%d done=%v errs=%d\n%s", s.contentChunks(), s.done, len(s.errors), r.body)
	}
	if str(dig(s.errors[0], "error", "code")) != "stream_interrupted" || str(dig(s.errors[0], "error", "type")) != "upstream_error" {
		t.Fatalf("error event: %v", s.errors[0])
	}
	row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 1)
	if str(row["status"]) != "error" || str(row["error_code"]) != "stream_interrupted" {
		t.Fatalf("log row: %v", row)
	}
}

func TestPhase1_ModelNotFound(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-ok-a"}, []string{"mock-ok-or"})
	r := tn.chat(tok, chatOpts{model: "no-such-model"})
	j := assertErr(t, r, 404, "invalid_request_error", "model_not_found")
	if str(dig(j, "error", "param")) != "model" || r.header.Get("X-Router0-Request-Id") == "" {
		t.Fatalf("404 body/headers: %s %v", r, r.header)
	}
	if e.groq.TotalChatCalls() != 0 || e.or.TotalChatCalls() != 0 {
		t.Fatal("providers were called for an unknown model")
	}
	row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 0)
	if str(row["provider"]) != "none" || str(row["error_code"]) != "model_not_found" || num(row["attempt_count"]) != 0 {
		t.Fatalf("log row: %v", row)
	}
	assertErr(t, tn.e.do("POST", "/v1/chat/completions", tok, J{"messages": []J{{"role": "user", "content": "x"}}}, nil), 400, "invalid_request_error", "missing_model")
	assertErr(t, tn.e.do("POST", "/v1/chat/completions", tok, "{not json", nil), 400, "invalid_request_error", "invalid_json")
}

func TestPhase1_InsufficientCredits(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-402-a"}, nil)
	r := tn.chat(tok, chatOpts{})
	j := assertErr(t, r, 402, "insufficient_credits", "insufficient_credits")
	if strings.Contains(string(r.body), mockprovider.SentinelBody) {
		t.Fatalf("provider body leaked: %s", r)
	}
	if str(dig(j, "error", "router0", "request_id")) == "" || len(list(dig(j, "error", "router0", "attempts"))) != 1 {
		t.Fatalf("router0 extension: %s", r)
	}
	// second request: no upstream call, still 402, attempt skipped
	r2 := tn.chat(tok, chatOpts{})
	j2 := assertErr(t, r2, 402, "insufficient_credits", "insufficient_credits")
	if e.groq.ChatCalls("mock-402-a") != 1 {
		t.Fatalf("second request called upstream (%d calls)", e.groq.ChatCalls("mock-402-a"))
	}
	if str(dig(j2, "error", "router0", "attempts", 0, "outcome")) != "skipped" {
		t.Fatalf("attempt: %s", r2)
	}
}

func TestPhase1_RateLimitedRetryAfter(t *testing.T) {
	_, tn, tok := fixture(t, []string{"mock-429-5-a", "mock-429-9-b"}, nil)
	r := tn.chat(tok, chatOpts{})
	assertErr(t, r, 429, "rate_limit_error", "rate_limited")
	ra, err := strconv.Atoi(r.header.Get("Retry-After"))
	if err != nil || ra < 4 || ra > 5 {
		t.Fatalf("Retry-After = %q", r.header.Get("Retry-After"))
	}
	if strings.Contains(string(r.body), mockprovider.SentinelBody) {
		t.Fatalf("provider body leaked: %s", r)
	}
	if n := len(list(dig(r.json(), "error", "router0", "attempts"))); n != 2 {
		t.Fatalf("attempts in body: %d", n)
	}
}

func TestPhase1_MixedPrecedence(t *testing.T) {
	type tc struct {
		name   string
		groq   []string
		or     []string
		status int
		typ    string
		code   string
		retry  [2]int // inclusive range, only for 429
	}
	cases := []tc{
		{"429 beats 402", []string{"mock-429-20-a"}, []string{"mock-402-or"}, 429, "rate_limit_error", "rate_limited", [2]int{19, 20}},
		{"402 beats 503", []string{"mock-402-a", "mock-500-b"}, nil, 402, "insufficient_credits", "insufficient_credits", [2]int{}},
		{"404+503 is 503", []string{"mock-404-a", "mock-500-b"}, nil, 503, "upstream_error", "all_providers_failed", [2]int{}},
		{"400+404 is 400", []string{"mock-404-a", "mock-400-b"}, nil, 400, "invalid_request_error", "upstream_rejected_request", [2]int{}},
		{"all 404 is 404", []string{"mock-404-a"}, nil, 404, "invalid_request_error", "model_not_found", [2]int{}},
		{"429+500 is 429", []string{"mock-500-a", "mock-429-7-b"}, nil, 429, "rate_limit_error", "rate_limited", [2]int{6, 7}},
	}
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			_, tn, tok := fixture(t, c.groq, c.or)
			r := tn.chat(tok, chatOpts{})
			assertErr(t, r, c.status, c.typ, c.code)
			if c.status == 429 {
				ra, err := strconv.Atoi(r.header.Get("Retry-After"))
				if err != nil || ra < c.retry[0] || ra > c.retry[1] {
					t.Fatalf("Retry-After = %q, want %v", r.header.Get("Retry-After"), c.retry)
				}
			}
			if c.status != 400 && strings.Contains(string(r.body), mockprovider.SentinelBody) {
				t.Fatalf("provider body leaked: %s", r)
			}
		})
	}
}

func TestPhase1_CooldownSkips(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-429-30-a", "mock-ok-b"}, nil)
	r1 := tn.chat(tok, chatOpts{})
	assertStatus(t, r1, 200)
	if r1.header.Get("X-Router0-Key") != "groq-b" || e.groq.ChatCalls("mock-429-30-a") != 1 {
		t.Fatalf("first request: %v", r1.header)
	}
	r2 := tn.chat(tok, chatOpts{})
	assertStatus(t, r2, 200)
	if e.groq.ChatCalls("mock-429-30-a") != 1 {
		t.Fatal("key in cooldown was called again")
	}
	if r2.header.Get("X-Router0-Attempts") != "2" {
		t.Fatalf("attempts header: %v", r2.header)
	}
	row := tn.waitLog(r2.header.Get("X-Router0-Request-Id"), 2)
	if str(dig(row, "trail", 0, "outcome")) != "skipped" || str(dig(row, "trail", 0, "class")) != "rate_limit" ||
		str(dig(row, "trail", 1, "outcome")) != "success" {
		t.Fatalf("trail: %v", row["trail"])
	}
	// the key list shows the cooldown
	var blocked bool
	for _, k := range list(tn.api("GET", "/api/keys", nil).json()["keys"]) {
		if str(dig(k, "label")) == "groq-a" {
			blocked = str(dig(k, "state", "reason")) == "rate_limited" && num(dig(k, "state", "retry_after_s")) > 0
		}
	}
	if !blocked {
		t.Fatal("GET /api/keys does not show groq-a rate_limited")
	}

	// a single-key org whose key just had a transient error is still attempted next time
	e2, tn2, tok2 := fixture(t, []string{"mock-500-solo"}, nil)
	assertErr(t, tn2.chat(tok2, chatOpts{}), 503, "upstream_error", "all_providers_failed")
	assertErr(t, tn2.chat(tok2, chatOpts{}), 503, "upstream_error", "all_providers_failed")
	if n := e2.groq.ChatCalls("mock-500-solo"); n != 2 {
		t.Fatalf("transient key should be retried, got %d calls", n)
	}
}

func TestPhase1_CooldownPersistsAcrossRestart(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-429-30-a", "mock-ok-b"}, nil)
	r1 := tn.chat(tok, chatOpts{})
	assertStatus(t, r1, 200)
	if e.groq.ChatCalls("mock-429-30-a") != 1 {
		t.Fatal("key a should have been called once")
	}

	// second instance on the same database: fresh in-memory caches
	_, ts2 := e.startServer()
	r2 := doHTTP(t, ts2.URL, "POST", "/v1/chat/completions", tok,
		J{"model": "llama-3.3-70b", "messages": []J{{"role": "user", "content": "hi"}}}, nil)
	assertStatus(t, r2, 200)
	if r2.header.Get("X-Router0-Key") != "groq-b" {
		t.Fatalf("headers: %v", r2.header)
	}
	if e.groq.ChatCalls("mock-429-30-a") != 1 {
		t.Fatal("restarted instance called the cooled-down key: Postgres is not the source of truth")
	}
}

func TestPhase1_RevokeInvalidatesCacheImmediately(t *testing.T) {
	_, tn, _ := fixture(t, []string{"mock-ok-a"}, nil)
	tok, id := tn.token("warm")
	for i := 0; i < 3; i++ { // warm the in-process cache
		assertStatus(t, tn.chat(tok, chatOpts{}), 200)
	}
	assertStatus(t, tn.api("DELETE", "/api/tokens/"+id, nil), 204)
	assertErr(t, tn.chat(tok, chatOpts{}), 401, "authentication_error", "invalid_api_key")
	assertStatus(t, tn.e.do("GET", "/v1/models", tok, nil, nil), 401)
}

func TestPhase1_AuthErrorDisablesKey(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-ok-flip-a", "mock-ok-b"}, nil)
	var keyA string
	for _, k := range list(tn.api("GET", "/api/keys", nil).json()["keys"]) {
		if str(dig(k, "label")) == "groq-a" {
			keyA = str(dig(k, "id"))
		}
	}
	e.groq.Alias("mock-ok-flip-a", "mock-401-a") // provider starts rejecting the key

	r := tn.chat(tok, chatOpts{})
	assertStatus(t, r, 200)
	if r.header.Get("X-Router0-Key") != "groq-b" {
		t.Fatalf("headers: %v", r.header)
	}
	row := tn.waitLog(r.header.Get("X-Router0-Request-Id"), 2)
	if str(dig(row, "trail", 0, "class")) != "auth" {
		t.Fatalf("trail: %v", row["trail"])
	}
	keyStatus := func() string {
		for _, k := range list(tn.api("GET", "/api/keys", nil).json()["keys"]) {
			if str(dig(k, "id")) == keyA {
				return str(dig(k, "status"))
			}
		}
		return ""
	}
	if keyStatus() != "error" {
		t.Fatalf("key status = %q, want error", keyStatus())
	}
	calls := e.groq.ChatCalls("mock-ok-flip-a")
	assertStatus(t, tn.chat(tok, chatOpts{}), 200)
	if e.groq.ChatCalls("mock-ok-flip-a") != calls {
		t.Fatal("errored key was attempted again")
	}

	// still bad: the test endpoint reports the error class and leaves the key in error
	tr := tn.api("POST", "/api/keys/"+keyA+"/test", nil)
	assertStatus(t, tr, 200)
	if str(tr.json()["status"]) != "error" || str(tr.json()["class"]) != "auth" || keyStatus() != "error" {
		t.Fatalf("test (still bad): %s status=%s", tr, keyStatus())
	}
	// provider recovers: test succeeds and re-activates the key
	e.groq.Alias("mock-ok-flip-a", "mock-ok-a")
	tr = tn.api("POST", "/api/keys/"+keyA+"/test", nil)
	assertStatus(t, tr, 200)
	if str(tr.json()["status"]) != "ok" || keyStatus() != "active" {
		t.Fatalf("test (recovered): %s status=%s", tr, keyStatus())
	}
}

func TestPhase1_RequestLogTrail(t *testing.T) {
	_, tn, tok := fixture(t, []string{"mock-429-30-a", "mock-ok-b"}, nil)
	r := tn.chat(tok, chatOpts{})
	assertStatus(t, r, 200)
	id := r.header.Get("X-Router0-Request-Id")
	if !strings.HasPrefix(id, "r0-") {
		t.Fatalf("request id %q", id)
	}

	var item J
	waitFor(t, 3*time.Second, "request in GET /api/requests", func() bool {
		for _, x := range list(tn.api("GET", "/api/requests", nil).json()["requests"]) {
			if str(dig(x, "request_id")) == id {
				item = x.(J)
				return len(list(item["trail"])) == 2
			}
		}
		return false
	})
	if num(item["attempt_count"]) != 2 || str(item["key_label"]) != "groq-b" || str(dig(item, "trail", 0, "class")) != "rate_limit" ||
		str(item["status"]) != "success" || num(item["http_status"]) != 200 || str(item["canonical_model"]) != "llama-3.3-70b" ||
		str(item["provider_model_id"]) != "llama-3.3-70b-versatile" || str(item["token_name"]) != "svc" ||
		num(dig(item, "trail", 0, "http_status")) != 429 || num(dig(item, "trail", 0, "order")) != 0 || num(dig(item, "trail", 1, "order")) != 1 {
		t.Fatalf("request item: %v", item)
	}
	one := tn.api("GET", "/api/requests/"+id, nil)
	assertStatus(t, one, 200)
	if str(one.json()["request_id"]) != id {
		t.Fatalf("single: %s", one)
	}
	f := tn.api("GET", "/api/requests?status=error", nil)
	for _, x := range list(f.json()["requests"]) {
		if str(dig(x, "request_id")) == id {
			t.Fatal("status filter returned a success row")
		}
	}
	// costs: {costs:[...]}
	c := tn.api("GET", "/api/costs", nil)
	assertStatus(t, c, 200)
	if len(list(c.json()["costs"])) == 0 {
		t.Fatalf("costs: %s", c)
	}
	// removed endpoints
	assertStatus(t, tn.api("GET", "/api/pool", nil), 404)
	assertStatus(t, tn.api("GET", "/api/attempts", nil), 404)
}

func TestPhase1_NoContentStored(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-500-a", "mock-ok-b"}, []string{"mock-402-or"})
	const prompt = "SENTINEL-PROMPT-7f3a"
	var ids []string
	r := tn.chat(tok, chatOpts{prompt: prompt})
	assertStatus(t, r, 200)
	if !strings.Contains(string(r.body), "SENTINEL-RESP") {
		t.Fatalf("expected the mock reply in the response: %s", r)
	}
	ids = append(ids, r.header.Get("X-Router0-Request-Id"))
	r = tn.chat(tok, chatOpts{prompt: prompt, stream: true})
	assertStatus(t, r, 200)
	ids = append(ids, r.header.Get("X-Router0-Request-Id"))
	// all-fail request: Groq off, OpenRouter 402 -> error body must not echo the provider body
	r = tn.chat(tok, chatOpts{prompt: prompt, headers: map[string]string{"X-Router0-Providers": "openrouter"}})
	assertStatus(t, r, 402)
	if strings.Contains(string(r.body), mockprovider.SentinelBody) || strings.Contains(string(r.body), prompt) {
		t.Fatalf("error response leaks provider body or prompt: %s", r)
	}
	ids = append(ids, r.header.Get("X-Router0-Request-Id"))
	for _, id := range ids {
		tn.waitLog(id, 1)
	}

	ctx := context.Background()
	var n int
	// non-vacuous: rows exist
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM request_metadata WHERE org_id = $1`, tn.orgID).Scan(&n); err != nil || n < 3 {
		t.Fatalf("request_metadata rows: %d err=%v", n, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM request_attempts WHERE org_id = $1 AND outcome = 'error'`, tn.orgID).Scan(&n); err != nil || n < 2 {
		t.Fatalf("request_attempts error rows: %d err=%v", n, err)
	}
	// no sentinel anywhere in any column of the org's rows (prompt, response, provider body)
	for _, tbl := range []string{"request_metadata", "request_attempts"} {
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM `+tbl+` t WHERE t.org_id = $1 AND t::text ILIKE '%sentinel%'`, tn.orgID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s holds SENTINEL content in %d rows", tbl, n)
		}
	}
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(max(length(error_message)),0) FROM request_attempts WHERE org_id = $1`, tn.orgID).Scan(&n); err != nil || n > 200 {
		t.Fatalf("error_message length %d err=%v", n, err)
	}
}

func TestPhase1_OrgIsolation(t *testing.T) {
	e := newEnv(t)
	a := e.newTenant("iso-a")
	b := e.newTenant("iso-b")

	keyB := b.addKey("groq", "mock-ok-b1", "groq-b")
	b.waitCatalog("llama-3.3-70b", "groq")
	tokB, tokBID := b.token("svc-b")
	rb := b.chat(tokB, chatOpts{})
	assertStatus(t, rb, 200)
	reqB := rb.header.Get("X-Router0-Request-Id")
	b.waitLog(reqB, 1)

	// A sees nothing of B
	assertStatus(t, a.api("GET", "/api/requests/"+reqB, nil), 404)
	for _, x := range list(a.api("GET", "/api/requests", nil).json()["requests"]) {
		t.Fatalf("org A sees a request: %v", x)
	}
	if n := len(list(a.api("GET", "/api/keys", nil).json()["keys"])); n != 0 {
		t.Fatalf("org A sees %d keys", n)
	}
	if n := len(list(a.api("GET", "/api/tokens", nil).json()["tokens"])); n != 0 {
		t.Fatalf("org A sees %d tokens", n)
	}
	if n := len(list(a.api("GET", "/api/catalog", nil).json()["models"])); n != 0 {
		t.Fatalf("org A sees %d catalog models", n)
	}
	assertStatus(t, a.api("DELETE", "/api/keys/"+keyB, nil), 404)
	assertStatus(t, a.api("POST", "/api/keys/"+keyB+"/test", nil), 404)
	assertStatus(t, a.api("DELETE", "/api/tokens/"+tokBID, nil), 404)
	// B's token and key are untouched
	assertStatus(t, b.chat(tokB, chatOpts{}), 200)

	// A's token cannot use B's keys; A's /v1/models is empty
	tokA, _ := a.token("svc-a")
	assertErr(t, a.chat(tokA, chatOpts{}), 404, "invalid_request_error", "model_not_found")
	m := e.do("GET", "/v1/models", tokA, nil, nil)
	assertStatus(t, m, 200)
	if n := len(list(m.json()["data"])); n != 0 {
		t.Fatalf("org A /v1/models has %d entries", n)
	}
	// a service token is not a dashboard credential and vice versa
	assertStatus(t, e.do("GET", "/api/keys", tokB, nil, nil), 401)
	assertStatus(t, e.do("GET", "/v1/models", b.jwt, nil, nil), 401)
}

func TestPhase1_MetricsRenamed(t *testing.T) {
	e, tn, tok := fixture(t, []string{"mock-429-30-a", "mock-ok-b"}, nil)
	assertStatus(t, tn.chat(tok, chatOpts{}), 200)

	for _, path := range []string{"/health", "/"} {
		h := e.do("GET", path, "", nil, nil)
		assertStatus(t, h, 200)
		if str(h.json()["status"]) != "ok" {
			t.Fatalf("GET %s: %s", path, h)
		}
	}
	h := e.do("GET", "/healthz", "", nil, nil)
	assertStatus(t, h, 200)
	if str(h.json()["status"]) != "ok" || str(h.json()["db"]) != "ok" || h.json()["redis"] != nil {
		t.Fatalf("healthz: %s", h)
	}

	m := e.do("GET", "/metrics", "", nil, nil)
	assertStatus(t, m, 200)
	body := string(m.body)
	for _, name := range []string{"router0_requests_total", "router0_request_duration_seconds", "router0_attempts_total", "router0_key_state_marks_total"} {
		if !strings.Contains(body, name) {
			t.Errorf("metrics missing %s", name)
		}
	}
	if strings.Contains(body, "zer0_") {
		t.Errorf("metrics still contain zer0_ names")
	}
}
