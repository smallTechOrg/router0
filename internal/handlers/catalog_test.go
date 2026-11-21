package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/zer0-router/zer0/internal/catalog"
)

func catalogRouter(h *CatalogHandler, a *AliasesHandler) http.Handler {
	r := chi.NewRouter()
	r.Get("/v1/models", h.ListModels)
	r.Get("/api/catalog", h.GetCatalog)
	r.Get("/api/aliases", a.List)
	r.Post("/api/aliases", a.Create)
	r.Delete("/api/aliases/{alias}", a.Delete)
	return r
}

func call(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestCatalogAndAliasesHandlers(t *testing.T) {
	db := testPool(t)
	ctx := context.Background()
	org := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO orgs (id, name) VALUES ($1,$2)`, org, "cat-h-"+org); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(ctx, `DELETE FROM orgs WHERE id=$1`, org) })
	var uid string
	if err := db.QueryRow(ctx, `INSERT INTO users (email,password_hash) VALUES ($1,'x') RETURNING id`, org+"@test.local").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid) })
	for _, p := range []string{"groq", "google", "meshapi"} {
		if _, err := db.Exec(ctx, `INSERT INTO provider_keys (user_id, org_id, provider, label, ciphertext, nonce, status)
			VALUES ($1,$2,$3,'l','\x00','\x00','active')`, uid, org, p); err != nil {
			t.Fatal(err)
		}
	}
	repo := catalog.NewRepository(db)
	apply := func(p string, ids ...string) {
		var ms []catalog.Model
		for _, id := range ids {
			ms = append(ms, catalog.Model{ProviderModelID: id, Kind: catalog.ClassifyKind(p, id, nil)})
		}
		if _, err := repo.ApplyListing(ctx, org, p, "discovered", ms); err != nil {
			t.Fatal(err)
		}
	}
	apply("groq", "llama-3.3-70b-versatile", "whisper-large-v3")
	apply("google", "gemini-2.5-flash", "gemini-2.5-flash-image", "gemini-2.5-flash-preview-tts")
	apply("meshapi", "meta-llama/llama-3.3-70b-instruct", "openai/dall-e-3")

	orgFn := func(*http.Request) string { return org }
	rt := catalogRouter(NewCatalogHandler(repo, nil, orgFn), NewAliasesHandler(repo, orgFn, func(*http.Request) string { return uid }))

	// aliases
	if c, _ := call(t, rt, "POST", "/api/aliases", `{"alias":"Fast","canonical_model":"llama-3.3-70b"}`); c != 201 {
		t.Fatalf("create=%d", c)
	}
	if c, b := call(t, rt, "POST", "/api/aliases", `{"alias":"fast","canonical_model":"x"}`); c != 409 || b["error"].(map[string]any)["code"] != "alias_conflict" {
		t.Fatalf("dup=%d %v", c, b)
	}
	if c, b := call(t, rt, "POST", "/api/aliases", `{"alias":"gemini-2.5-flash","canonical_model":"x"}`); c != 422 {
		t.Fatalf("shadow=%d %v", c, b)
	}
	if c, _ := call(t, rt, "POST", "/api/aliases", `{"alias":"bad name","canonical_model":"x"}`); c != 422 {
		t.Fatalf("invalid=%d", c)
	}
	if c, _ := call(t, rt, "POST", "/api/aliases", `{`); c != 400 {
		t.Fatalf("badjson=%d", c)
	}
	if c, _ := call(t, rt, "POST", "/api/aliases", `{"alias":"later","canonical_model":"not-listed-yet"}`); c != 201 {
		t.Fatalf("future target=%d", c)
	}

	// /v1/models: chat only, aliases with alias_of, alias with unservable target omitted.
	c, body := call(t, rt, "GET", "/v1/models", "")
	if c != 200 {
		t.Fatalf("models=%d", c)
	}
	ids := map[string]map[string]any{}
	for _, d := range body["data"].([]any) {
		m := d.(map[string]any)
		ids[m["id"].(string)] = m["router0"].(map[string]any)
		if m["router0"].(map[string]any)["kind"] != "chat" {
			t.Fatalf("non-chat entry %v", m)
		}
	}
	if len(ids) != 3 || ids["gemini-2.5-flash"] == nil || ids["llama-3.3-70b"] == nil || ids["fast"]["alias_of"] != "llama-3.3-70b" {
		t.Fatalf("models=%v", ids)
	}
	if len(ids["llama-3.3-70b"]["providers"].([]any)) != 2 {
		t.Fatalf("providers=%v", ids["llama-3.3-70b"])
	}

	// /api/catalog: all kinds, kind filter, q filter, invalid kind.
	_, body = call(t, rt, "GET", "/api/catalog", "")
	if n := len(body["models"].([]any)); n != 6 {
		t.Fatalf("all models=%d %v", n, body["models"])
	}
	if len(body["aliases"].([]any)) != 2 || len(body["refreshes"].([]any)) != 3 {
		t.Fatalf("aliases/refreshes: %v", body)
	}
	_, body = call(t, rt, "GET", "/api/catalog?kind=image", "")
	ms := body["models"].([]any)
	if len(ms) != 2 {
		t.Fatalf("image=%v", ms)
	}
	p0 := ms[0].(map[string]any)["providers"].([]any)[0].(map[string]any)
	if p0["kind"] != "image" || p0["provider_model_id"] == "" {
		t.Fatalf("provider entry=%v", p0)
	}
	_, body = call(t, rt, "GET", "/api/catalog?kind=audio", "")
	if len(body["models"].([]any)) != 2 {
		t.Fatalf("audio=%v", body["models"])
	}
	_, body = call(t, rt, "GET", "/api/catalog?q=LLAMA-3.3&kind=chat", "")
	if len(body["models"].([]any)) != 1 {
		t.Fatalf("q=%v", body["models"])
	}
	if c, _ := call(t, rt, "GET", "/api/catalog?kind=video", ""); c != 422 {
		t.Fatalf("invalid kind=%d", c)
	}

	// list + delete
	_, body = call(t, rt, "GET", "/api/aliases", "")
	if len(body["aliases"].([]any)) != 2 {
		t.Fatalf("list=%v", body)
	}
	if c, _ := call(t, rt, "DELETE", "/api/aliases/FAST", ""); c != 204 {
		t.Fatalf("delete=%d", c)
	}
	if c, _ := call(t, rt, "DELETE", "/api/aliases/fast", ""); c != 404 {
		t.Fatalf("delete missing=%d", c)
	}

	// unauthenticated
	none := catalogRouter(NewCatalogHandler(repo, nil, func(*http.Request) string { return "" }), NewAliasesHandler(repo, func(*http.Request) string { return "" }, nil))
	if c, _ := call(t, none, "GET", "/v1/models", ""); c != 401 {
		t.Fatalf("unauth=%d", c)
	}
}
