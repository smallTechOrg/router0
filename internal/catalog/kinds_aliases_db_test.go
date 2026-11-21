package catalog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func kindOf(t *testing.T, e *env, provider, id string) string {
	t.Helper()
	var k string
	if err := e.pool.QueryRow(context.Background(),
		`SELECT kind FROM org_model_catalog WHERE org_id=$1 AND provider=$2 AND provider_model_id=$3`, e.org, provider, id).Scan(&k); err != nil {
		t.Fatalf("row %s/%s: %v", provider, id, err)
	}
	return k
}

func TestDiscoveryStoresEveryKind(t *testing.T) {
	e := newEnv(t, "google", "meshapi", "anthropic")
	ctx := context.Background()
	gen := []string{"generateContent", "streamGenerateContent"}
	e.provs["google"].set("gemini-2.5-flash", "gemini-2.5-flash-image", "gemini-2.5-flash-preview-tts", "lyria-3-pro-preview",
		"gemini-2.5-computer-use-preview-10", "gemini-robotics-er-2-preview", "text-embedding-004", "gemini-3.5-transcribe",
		"gemini-omni-flash-preview", "aqa")
	e.provs["google"].methods = map[string][]string{
		"gemini-2.5-flash": gen, "gemini-2.5-flash-image": gen, "gemini-2.5-flash-preview-tts": gen,
		"text-embedding-004": {"embedContent"}, "aqa": {"generateAnswer"}, "gemini-omni-flash-preview": gen,
	}
	e.provs["meshapi"].set("mistral/mistral-7b-instruct-v0", "meta-llama/llama-3.3-70b-instruct", "anthropic/claude-sonnet-5.5", "openai/dall-e-3")
	e.provs["anthropic"].set("claude-sonnet-5-5")
	for _, p := range []string{"google", "meshapi", "anthropic"} {
		if _, err := e.disc.RefreshProvider(ctx, e.org, p); err != nil {
			t.Fatal(p, err)
		}
	}
	want := map[[2]string]string{
		{"google", "gemini-2.5-flash"}:                   "chat",
		{"google", "gemini-2.5-flash-image"}:             "image",
		{"google", "gemini-2.5-flash-preview-tts"}:       "audio",
		{"google", "lyria-3-pro-preview"}:                "audio",
		{"google", "gemini-2.5-computer-use-preview-10"}: "other",
		{"google", "gemini-robotics-er-2-preview"}:       "other",
		{"google", "text-embedding-004"}:                 "embedding",
		{"google", "aqa"}:                                "other",
		{"google", "gemini-omni-flash-preview"}:          "chat",
		{"meshapi", "openai/dall-e-3"}:                   "image",
		{"meshapi", "mistral/mistral-7b-instruct-v0"}:    "chat",
		{"anthropic", "claude-sonnet-5-5"}:               "chat",
	}
	for k, v := range want {
		if got := kindOf(t, e, k[0], k[1]); got != v {
			t.Errorf("%v kind=%s want %s", k, got, v)
		}
	}
	chat, _ := e.repo.List(ctx, e.org, "", "chat", true)
	for _, r := range chat {
		if r.Kind != "chat" {
			t.Errorf("non-chat in chat list: %+v", r)
		}
	}
	img, _ := e.repo.List(ctx, e.org, "", "image", false)
	if len(img) != 2 {
		t.Errorf("image rows=%d", len(img))
	}
	// Mesh + Anthropic + Mesh share canonical claude-sonnet-5-5.
	got, _ := e.res.Resolve(ctx, e.org, "claude-sonnet-5-5")
	if m := providersOf(got); len(m) != 2 || m["meshapi"] != "anthropic/claude-sonnet-5.5" || m["anthropic"] != "claude-sonnet-5-5" {
		t.Fatalf("claude entries: %+v", got)
	}
	if got, _ := e.res.Resolve(ctx, e.org, "mistral-7b"); len(got) != 1 {
		t.Fatalf("mistral-7b: %+v", got)
	}
}

func TestNonChatNotResolvedNorResurrected(t *testing.T) {
	e := newEnv(t, "google")
	ctx := context.Background()
	e.provs["google"].set("gemini-2.5-flash-image")
	if _, err := e.disc.RefreshProvider(ctx, e.org, "google"); err != nil {
		t.Fatal(err)
	}
	e.pool.Exec(ctx, `UPDATE org_catalog_refreshes SET last_refreshed_at = NOW() - interval '1 hour' WHERE org_id=$1`, e.org)
	before := e.provs["google"].calls.Load()
	if got, err := e.res.Resolve(ctx, e.org, "gemini-2.5-flash-image"); err != nil || got != nil {
		t.Fatalf("non-chat resolved: %+v %v", got, err)
	}
	if e.provs["google"].calls.Load() != before {
		t.Fatal("non-chat miss must not trigger re-listing")
	}
	if k := kindOf(t, e, "google", "gemini-2.5-flash-image"); k != "image" {
		t.Fatalf("kind=%s", k)
	}
	// Reappearing via on-demand refresh keeps its kind.
	e.provs["google"].set("gemini-2.5-flash-image", "brand-new-tts")
	e.pool.Exec(ctx, `UPDATE org_catalog_refreshes SET last_refreshed_at = NOW() - interval '1 hour' WHERE org_id=$1`, e.org)
	if got, _ := e.res.Resolve(ctx, e.org, "brand-new-tts"); got != nil {
		t.Fatalf("on-demand tts resolved as chat: %+v", got)
	}
	if k := kindOf(t, e, "google", "brand-new-tts"); k != "audio" {
		t.Fatalf("kind=%s", k)
	}
}

func TestAliasesCRUDAndResolve(t *testing.T) {
	e := newEnv(t, "groq")
	ctx := context.Background()
	e.provs["groq"].set("llama-3.3-70b-versatile", "whisper-large-v3")
	if _, err := e.disc.RefreshProvider(ctx, e.org, "groq"); err != nil {
		t.Fatal(err)
	}
	a, err := e.repo.CreateAlias(ctx, e.org, "  Fast ", "Llama-3.3-70B-Versatile", e.user)
	if err != nil || a.Alias != "fast" || a.CanonicalModel != "llama-3.3-70b" {
		t.Fatalf("create: %+v %v", a, err)
	}
	if _, err := e.repo.CreateAlias(ctx, e.org, "FAST", "x", ""); !errors.Is(err, ErrAliasExists) {
		t.Fatalf("dup: %v", err)
	}
	for _, name := range []string{"llama-3.3-70b", "llama-3.3-70b-versatile", "Llama-3.3-70B-Versatile"} {
		if _, err := e.repo.CreateAlias(ctx, e.org, name, "other-model", ""); !errors.Is(err, ErrAliasShadows) {
			t.Fatalf("shadow %q: %v", name, err)
		}
	}
	// non-chat model names don't shadow; target need not exist; bad names rejected.
	if _, err := e.repo.CreateAlias(ctx, e.org, "whisper-large-v3", "later-model", ""); err != nil {
		t.Fatalf("non-chat alias: %v", err)
	}
	for _, bad := range []string{"", "a b", "-x", "a/b"} {
		if _, err := e.repo.CreateAlias(ctx, e.org, bad, "m", ""); !errors.Is(err, ErrAliasInvalid) {
			t.Fatalf("bad %q: %v", bad, err)
		}
	}
	if _, err := e.repo.CreateAlias(ctx, e.org, "chain", "fast", ""); !errors.Is(err, ErrAliasInvalid) {
		t.Fatalf("chain: %v", err)
	}
	if _, err := e.repo.CreateAlias(ctx, e.org, "self", "self", ""); !errors.Is(err, ErrAliasInvalid) {
		t.Fatalf("self: %v", err)
	}

	got, err := e.res.Resolve(ctx, e.org, "FAST")
	if err != nil || len(got) != 1 || got[0].Canonical != "llama-3.3-70b" || got[0].ProviderModelID != "llama-3.3-70b-versatile" {
		t.Fatalf("alias resolve: %+v %v", got, err)
	}
	list, _ := e.repo.ListAliases(ctx, e.org)
	if len(list) != 2 || list[0].Alias != "fast" {
		t.Fatalf("list: %+v", list)
	}
	// Alias to a not-yet-existing model resolves after it appears (on-demand).
	e.provs["groq"].set("llama-3.3-70b-versatile", "later-model")
	e.pool.Exec(ctx, `UPDATE org_catalog_refreshes SET last_refreshed_at = NOW() - interval '1 hour' WHERE org_id=$1`, e.org)
	if got, _ := e.res.Resolve(ctx, e.org, "whisper-large-v3"); len(got) != 1 || got[0].Canonical != "later-model" {
		t.Fatalf("alias to later model: %+v", got)
	}
	if err := e.repo.DeleteAlias(ctx, e.org, "FAST"); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.DeleteAlias(ctx, e.org, "fast"); !errors.Is(err, ErrAliasNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
	if got, _ := e.res.Resolve(ctx, e.org, "fast"); got != nil {
		t.Fatalf("deleted alias resolves: %+v", got)
	}
}

// Concurrent misses for the same model refresh once; waiters see the result.
func TestOnDemandConcurrent(t *testing.T) {
	e := newEnv(t, "groq")
	ctx := context.Background()
	e.provs["groq"].set("llama-3.1-8b-instant")
	if _, err := e.disc.RefreshProvider(ctx, e.org, "groq"); err != nil {
		t.Fatal(err)
	}
	e.provs["groq"].set("llama-3.1-8b-instant", "new-model-y")
	e.pool.Exec(ctx, `UPDATE org_catalog_refreshes SET last_refreshed_at = NOW() - interval '1 hour' WHERE org_id=$1`, e.org)
	before := e.provs["groq"].calls.Load()
	var wg sync.WaitGroup
	res := make([][]Entry, 8)
	for i := range res {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res[i], _ = e.res.Resolve(ctx, e.org, "new-model-y")
		}(i)
	}
	wg.Wait()
	for i, r := range res {
		if len(r) != 1 {
			t.Errorf("caller %d got %+v", i, r)
		}
	}
	if n := e.provs["groq"].calls.Load() - before; n != 1 {
		t.Fatalf("expected 1 listing call, got %d", n)
	}
	_ = time.Second
}
