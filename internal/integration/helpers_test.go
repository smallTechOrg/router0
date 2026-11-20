//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/db"
	"github.com/zer0-router/zer0/internal/server"
	"github.com/zer0-router/zer0/internal/testutil/mockprovider"
)

// J is a loose JSON object.
type J = map[string]any

var (
	migrateOnce sync.Once
	migrateErr  error
	poolOnce    sync.Once
	sharedPool  *pgxpool.Pool
	poolErr     error
)

func mustEnv(t *testing.T, k string) string {
	t.Helper()
	v := os.Getenv(k)
	if v == "" {
		t.Fatalf("env %s is required (see spec/roadmap.md GATE_ENV)", k)
	}
	return v
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := mustEnv(t, "DATABASE_URL")
	mustEnv(t, "AES_KEY")
	migrateOnce.Do(func() {
		_, f, _, _ := runtime.Caller(0)
		migrateErr = db.RunMigrations(dsn, filepath.Join(filepath.Dir(f), "..", "..", "migrations"))
	})
	if migrateErr != nil {
		t.Fatalf("migrations: %v", migrateErr)
	}
	poolOnce.Do(func() { sharedPool, poolErr = db.NewPool(context.Background(), dsn) })
	if poolErr != nil {
		t.Fatalf("pool: %v", poolErr)
	}
	return sharedPool
}

// ---- HTTP helpers ----

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) json() J {
	var j J
	_ = json.Unmarshal(r.body, &j)
	return j
}

func (r resp) String() string { return fmt.Sprintf("HTTP %d %s", r.status, r.body) }

// dig walks nested JSON by string keys and int indices; nil if absent.
func dig(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k < 0 || k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func list(v any) []any {
	a, _ := v.([]any)
	return a
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

func doHTTP(t *testing.T, base, method, path, bearer string, body any, hdr map[string]string) resp {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, header: res.Header, body: b}
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ---- environment ----

// env is one gateway instance wired exactly like production (server.Build),
// with fresh Groq and OpenRouter mock providers behind the *_BASE_URL overrides.
type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	secret string
	groq   *mockprovider.Server
	or     *mockprovider.Server
	app    *server.App
	ts     *httptest.Server
	admin  string // platform-admin JWT
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testPool(t)
	e := &env{t: t, pool: pool, secret: mustEnv(t, "JWT_SECRET")}
	e.groq = mockprovider.New(mockprovider.Options{Flavour: mockprovider.Groq})
	e.or = mockprovider.New(mockprovider.Options{Flavour: mockprovider.OpenRouter})
	t.Setenv("GROQ_BASE_URL", e.groq.BaseURL())
	t.Setenv("OPENROUTER_BASE_URL", e.or.BaseURL())
	t.Cleanup(e.groq.Close)
	t.Cleanup(e.or.Close)
	e.app, e.ts = e.startServer()
	e.admin = e.makeAdmin()
	return e
}

// newRealEnv is newEnv without mock providers: adapters use their real base
// URLs (used only by the smoke tests).
func newRealEnv(t *testing.T) *env {
	t.Helper()
	pool := testPool(t)
	e := &env{t: t, pool: pool, secret: mustEnv(t, "JWT_SECRET")}
	t.Setenv("GROQ_BASE_URL", "")
	t.Setenv("OPENROUTER_BASE_URL", "")
	e.app, e.ts = e.startServer()
	e.admin = e.makeAdmin()
	return e
}

// startServer builds another server.Build instance on the same database.
func (e *env) startServer() (*server.App, *httptest.Server) {
	app := server.Build(server.Deps{Pool: e.pool, JWTSecret: e.secret})
	ts := httptest.NewServer(app.Handler)
	e.t.Cleanup(func() {
		ts.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Close(ctx)
	})
	return app, ts
}

func uniq(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// makeAdmin inserts a platform admin with an org and returns its JWT.
func (e *env) makeAdmin() string {
	e.t.Helper()
	ctx := context.Background()
	email := uniq("admin") + "@example.test"
	var uid, oid string
	if err := e.pool.QueryRow(ctx, `INSERT INTO users (email, password_hash, role, status) VALUES ($1,'x','admin','active') RETURNING id::text`, email).Scan(&uid); err != nil {
		e.t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO orgs (name) VALUES ($1) RETURNING id::text`, email).Scan(&oid); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO org_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, oid, uid); err != nil {
		e.t.Fatal(err)
	}
	tok, err := auth.IssueAccessToken(e.secret, uid, email, "admin", oid)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func (e *env) do(method, path, bearer string, body any, hdr map[string]string) resp {
	e.t.Helper()
	return doHTTP(e.t, e.ts.URL, method, path, bearer, body, hdr)
}

// ---- tenants ----

type tenant struct {
	e      *env
	email  string
	userID string
	orgID  string
	jwt    string
}

// invite creates an invite through the admin API. orgID "" = new org for the invitee.
func (e *env) invite(email, orgID, orgRole string) string {
	e.t.Helper()
	body := J{"email": email}
	if orgID != "" {
		body["org_id"] = orgID
		body["org_role"] = orgRole
	}
	r := e.do("POST", "/admin/invites", e.admin, body, nil)
	if r.status != 201 {
		e.t.Fatalf("create invite: %s", r)
	}
	return str(r.json()["token"])
}

func (e *env) register(email, password, orgName, inviteToken string) resp {
	body := J{"invite_token": inviteToken, "email": email, "password": password}
	if orgName != "" {
		body["org_name"] = orgName
	}
	return e.do("POST", "/auth/register", "", body, nil)
}

func (e *env) tenantFromRegister(r resp) *tenant {
	e.t.Helper()
	if r.status != 201 {
		e.t.Fatalf("register: %s", r)
	}
	j := r.json()
	return &tenant{e: e, email: str(dig(j, "user", "email")), userID: str(dig(j, "user", "id")),
		orgID: str(dig(j, "org", "id")), jwt: str(j["access_token"])}
}

// newTenant registers a fresh owner with a new org.
func (e *env) newTenant(name string) *tenant {
	e.t.Helper()
	email := uniq(name) + "@example.test"
	return e.tenantFromRegister(e.register(email, "correct-horse-battery", "Org "+email, e.invite(email, "", "")))
}

// newMember registers a member into tn's org.
func (tn *tenant) newMember() *tenant {
	tn.e.t.Helper()
	email := uniq("member") + "@example.test"
	return tn.e.tenantFromRegister(tn.e.register(email, "correct-horse-battery", "", tn.e.invite(email, tn.orgID, "member")))
}

func (tn *tenant) api(method, path string, body any) resp {
	tn.e.t.Helper()
	return tn.e.do(method, path, tn.jwt, body, nil)
}

// addKey adds a provider key and returns its id.
func (tn *tenant) addKey(provider, key, label string) string {
	tn.e.t.Helper()
	r := tn.api("POST", "/api/keys", J{"provider": provider, "key": key, "label": label})
	if r.status != 201 {
		tn.e.t.Fatalf("add key %s/%s: %s", provider, label, r)
	}
	if strings.Contains(string(r.body), key) {
		tn.e.t.Fatalf("add key response leaks the raw key: %s", r)
	}
	return str(r.json()["id"])
}

// waitCatalog waits until canonical is available on every listed provider.
func (tn *tenant) waitCatalog(canonical string, provs ...string) {
	tn.e.t.Helper()
	tn.waitCatalogTimeout(canonical, 5*time.Second, provs...)
}

func (tn *tenant) waitCatalogTimeout(canonical string, d time.Duration, provs ...string) {
	tn.e.t.Helper()
	waitFor(tn.e.t, d, fmt.Sprintf("catalog %s on %v", canonical, provs), func() bool {
		r := tn.api("GET", "/api/catalog", nil)
		have := map[string]bool{}
		for _, m := range list(r.json()["models"]) {
			if str(dig(m, "canonical")) != canonical {
				continue
			}
			for _, p := range list(dig(m, "providers")) {
				if dig(p, "available") == true {
					have[str(dig(p, "provider"))] = true
				}
			}
		}
		for _, p := range provs {
			if !have[p] {
				return false
			}
		}
		return true
	})
}

// token creates a service token and returns (raw token, id).
func (tn *tenant) token(name string) (string, string) {
	tn.e.t.Helper()
	r := tn.api("POST", "/api/tokens", J{"name": name})
	if r.status != 201 {
		tn.e.t.Fatalf("create token: %s", r)
	}
	j := r.json()
	return str(j["token"]), str(j["id"])
}

type chatOpts struct {
	model   string
	stream  bool
	prompt  string
	usage   bool
	headers map[string]string
}

func (tn *tenant) chat(token string, o chatOpts) resp {
	tn.e.t.Helper()
	if o.model == "" {
		o.model = "llama-3.3-70b"
	}
	if o.prompt == "" {
		o.prompt = "hello"
	}
	body := J{"model": o.model, "messages": []J{{"role": "user", "content": o.prompt}}}
	if o.stream {
		body["stream"] = true
		if o.usage {
			body["stream_options"] = J{"include_usage": true}
		}
	}
	return tn.e.do("POST", "/v1/chat/completions", token, body, o.headers)
}

// waitLog polls the request log until the request (with at least wantTrail trail entries) is visible.
func (tn *tenant) waitLog(reqID string, wantTrail int) J {
	tn.e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last resp
	for {
		last = tn.api("GET", "/api/requests/"+reqID, nil)
		if last.status == 200 {
			if out := last.json(); len(list(out["trail"])) >= wantTrail {
				return out
			}
		}
		if time.Now().After(deadline) {
			tn.e.t.Fatalf("request log row %s not ready after 5s (want trail>=%d); last response: %s", reqID, wantTrail, last)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// sseResult is a parsed SSE body: JSON chunks, error events and the [DONE] flag.
type sseResult struct {
	chunks []J
	errors []J
	done   bool
}

func parseSSE(body []byte) sseResult {
	var out sseResult
	for _, line := range strings.Split(string(body), "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			out.done = true
			continue
		}
		var j J
		if json.Unmarshal([]byte(data), &j) != nil {
			continue
		}
		if j["error"] != nil {
			out.errors = append(out.errors, j)
		} else {
			out.chunks = append(out.chunks, j)
		}
	}
	return out
}

func (s sseResult) contentChunks() int {
	n := 0
	for _, c := range s.chunks {
		if str(dig(c, "choices", 0, "delta", "content")) != "" {
			n++
		}
	}
	return n
}

func assertStatus(t *testing.T, r resp, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("want HTTP %d, got %s", want, r)
	}
}

func assertErr(t *testing.T, r resp, status int, typ, code string) J {
	t.Helper()
	assertStatus(t, r, status)
	j := r.json()
	if str(dig(j, "error", "type")) != typ || str(dig(j, "error", "code")) != code {
		t.Fatalf("want error %s/%s, got %s", typ, code, r)
	}
	return j
}

// trailOf renders each trail entry as "provider/label:outcome[/class]".
func trailOf(row J) []string {
	var out []string
	for _, e := range list(row["trail"]) {
		s := str(dig(e, "provider")) + "/" + str(dig(e, "key_label")) + ":" + str(dig(e, "outcome"))
		if c := str(dig(e, "class")); c != "" {
			s += "/" + c
		}
		out = append(out, s)
	}
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
