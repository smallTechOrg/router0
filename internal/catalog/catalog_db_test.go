package catalog

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zer0-router/zer0/internal/providers"
)

// fakeProv embeds the interface so only Name/ListModels need implementing.
type fakeProv struct {
	providers.Provider
	name   string
	mu     sync.Mutex
	models []string
	calls  atomic.Int32
	// perKey: apiKey -> error to return
	keyErr map[string]error
	block  chan struct{}
	// methods: model id -> Extra["methods"] (Gemini supportedGenerationMethods)
	methods map[string][]string
	// declared: model id -> Extra["kind"] (a provider that states each model's type itself, e.g. Mesh model_type)
	declared map[string]string
}

func (f *fakeProv) Name() string { return f.name }
func (f *fakeProv) set(m ...string) {
	f.mu.Lock()
	f.models = m
	f.mu.Unlock()
}
func (f *fakeProv) ListModels(ctx context.Context, apiKey string) ([]providers.ModelInfo, error) {
	f.calls.Add(1)
	if f.block != nil {
		<-f.block
	}
	if err := f.keyErr[apiKey]; err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []providers.ModelInfo
	for _, id := range f.models {
		mi := providers.ModelInfo{ID: id, ContextWindow: 8192}
		if m, ok := f.methods[id]; ok {
			mi.Extra = map[string]any{"methods": m}
		}
		if k, ok := f.declared[id]; ok {
			if mi.Extra == nil {
				mi.Extra = map[string]any{}
			}
			mi.Extra["kind"] = k
		}
		out = append(out, mi)
	}
	return out, nil
}

type fakeKeys struct {
	pool *pgxpool.Pool
	mu   sync.Mutex
	keys map[string][]ActiveKey // provider -> keys
	bad  []string
}

func (k *fakeKeys) ActiveKeys(_ context.Context, _, provider string) ([]ActiveKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.keys[provider], nil
}
func (k *fakeKeys) MarkAuthError(ctx context.Context, id string) error {
	k.mu.Lock()
	k.bad = append(k.bad, id)
	k.mu.Unlock()
	_, err := k.pool.Exec(ctx, `UPDATE provider_keys SET status='error' WHERE id=$1`, id)
	return err
}

type env struct {
	pool  *pgxpool.Pool
	org   string
	user  string
	repo  *Repository
	keys  *fakeKeys
	provs map[string]*fakeProv
	disc  *Discovery
	res   *CatalogResolver
}

// newEnv creates a unique org with one active key per named provider.
func newEnv(t *testing.T, provNames ...string) *env {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var user, org string
	if err := pool.QueryRow(ctx, `INSERT INTO users (email,password_hash) VALUES ($1,'x') RETURNING id`, "cat-"+suffix+"@test.local").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO orgs (name) VALUES ($1) RETURNING id`, "cat-"+suffix).Scan(&org); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM orgs WHERE id=$1`, org)
		pool.Exec(c, `DELETE FROM users WHERE id=$1`, user)
	})
	e := &env{pool: pool, org: org, user: user, repo: NewRepository(pool), keys: &fakeKeys{pool: pool, keys: map[string][]ActiveKey{}}, provs: map[string]*fakeProv{}}
	for _, p := range provNames {
		e.addKey(t, p, "label-1", p+"-key-1")
		e.provs[p] = &fakeProv{name: p, keyErr: map[string]error{}}
	}
	e.disc = NewDiscovery(e.repo, e.keys).WithProviderLookup(func(n string) (providers.Provider, error) {
		if p, ok := e.provs[n]; ok {
			return p, nil
		}
		return nil, fmt.Errorf("unknown provider %s", n)
	})
	e.res = NewResolver(e.repo, e.disc)
	return e
}

func (e *env) addKey(t *testing.T, provider, label, apiKey string) string {
	t.Helper()
	var id string
	err := e.pool.QueryRow(context.Background(), `
INSERT INTO provider_keys (user_id, org_id, provider, label, ciphertext, nonce, status)
VALUES ($1, $2, $3, $4, '\x00', '\x00', 'active') RETURNING id`, e.user, e.org, provider, label).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	e.keys.mu.Lock()
	e.keys.keys[provider] = append(e.keys.keys[provider], ActiveKey{ID: id, APIKey: apiKey})
	e.keys.mu.Unlock()
	return id
}

func providersOf(es []Entry) map[string]string {
	m := map[string]string{}
	for _, e := range es {
		m[e.Provider] = e.ProviderModelID
	}
	return m
}

func TestDiscoverAndResolve(t *testing.T) {
	e := newEnv(t, "groq", "openrouter")
	ctx := context.Background()
	e.provs["groq"].set("llama-3.3-70b-versatile", "whisper-large-v3", "llama-guard-3-8b", "llama-3.1-8b-instant")
	e.provs["openrouter"].set("meta-llama/llama-3.3-70b-instruct", "meta-llama/llama-3.3-70b-instruct:free", "openai/text-embedding-3-small")

	if n, err := e.disc.RefreshProvider(ctx, e.org, "groq"); err != nil || n != 4 {
		t.Fatalf("groq refresh n=%d err=%v (all kinds stored)", n, err)
	}
	if n, err := e.disc.RefreshProvider(ctx, e.org, "openrouter"); err != nil || n != 3 {
		t.Fatalf("openrouter refresh n=%d err=%v", n, err)
	}

	got, err := e.res.Resolve(ctx, e.org, "meta-llama/llama-3.3-70b-instruct")
	if err != nil {
		t.Fatal(err)
	}
	m := providersOf(got)
	if len(m) != 2 || m["groq"] != "llama-3.3-70b-versatile" || m["openrouter"] != "meta-llama/llama-3.3-70b-instruct" {
		t.Fatalf("unexpected entries: %+v", got)
	}
	if got[0].Canonical != "llama-3.3-70b" || got[0].Kind != "chat" {
		t.Fatalf("bad entry %+v", got[0])
	}
	// :free is a distinct canonical model, only on openrouter.
	free, _ := e.res.Resolve(ctx, e.org, "llama-3.3-70b:free")
	if m := providersOf(free); len(m) != 1 || m["openrouter"] == "" {
		t.Fatalf("free: %+v", free)
	}
	// embedding model is not resolvable as chat but is as embedding.
	if r, _ := e.res.Resolve(ctx, e.org, "text-embedding-3-small"); len(r) != 0 {
		t.Fatalf("embedding resolved as chat: %+v", r)
	}
	if r, _ := e.res.ResolveKind(ctx, e.org, "text-embedding-3-small", "embedding"); len(r) != 1 {
		t.Fatalf("embedding not resolved: %+v", r)
	}

	rows, err := e.repo.List(ctx, e.org, "llama-3.3", "chat", true)
	if err != nil || len(rows) != 3 {
		t.Fatalf("list rows=%d err=%v", len(rows), err)
	}
	refs, _ := e.repo.Refreshes(ctx, e.org)
	if len(refs) != 2 || refs[0].LastStatus != "ok" {
		t.Fatalf("refreshes: %+v", refs)
	}

	// Vanished model: groq drops llama-3.3-70b-versatile.
	e.provs["groq"].set("llama-3.1-8b-instant")
	if _, err := e.disc.RefreshProvider(ctx, e.org, "groq"); err != nil {
		t.Fatal(err)
	}
	e.res.od.misses = map[string]*missEntry{}
	e.pool.Exec(ctx, `UPDATE org_catalog_refreshes SET last_refreshed_at = NOW() - interval '1 hour' WHERE org_id=$1`, e.org)
	got, _ = e.res.Resolve(ctx, e.org, "llama-3.3-70b")
	if m := providersOf(got); len(m) != 1 || m["openrouter"] == "" {
		t.Fatalf("after vanish: %+v", got)
	}

	// MarkUnavailable for a single row.
	if err := e.repo.MarkUnavailable(ctx, e.org, "openrouter", "meta-llama/llama-3.3-70b-instruct"); err != nil {
		t.Fatal(err)
	}
	var avail bool
	e.pool.QueryRow(ctx, `SELECT is_available FROM org_model_catalog WHERE org_id=$1 AND provider='openrouter' AND provider_model_id='meta-llama/llama-3.3-70b-instruct'`, e.org).Scan(&avail)
	if avail {
		t.Fatal("row should be unavailable")
	}
}

func TestShortestAndExactModelID(t *testing.T) {
	e := newEnv(t, "groq")
	ctx := context.Background()
	e.provs["groq"].set("llama-3.3-70b-versatile", "llama-3.3-70b-instruct-x", "llama-3.3-70b")
	if _, err := e.disc.RefreshProvider(ctx, e.org, "groq"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.res.Resolve(ctx, e.org, "llama-3.3-70b-versatile")
	if len(got) != 1 || got[0].ProviderModelID != "llama-3.3-70b-versatile" {
		t.Fatalf("exact match expected: %+v", got)
	}
	got, _ = e.res.Resolve(ctx, e.org, "llama-3.3-70b")
	if len(got) != 1 || got[0].ProviderModelID != "llama-3.3-70b" {
		t.Fatalf("shortest expected: %+v", got)
	}
}

func TestKeyFallbackOnAuthError(t *testing.T) {
	e := newEnv(t, "groq")
	ctx := context.Background()
	k2 := e.addKey(t, "groq", "label-2", "groq-key-2")
	_ = k2
	e.provs["groq"].keyErr["groq-key-1"] = &providers.ProviderError{Class: providers.ErrorClassAuth, HTTP: 401}
	e.provs["groq"].set("llama-3.1-8b-instant")
	n, err := e.disc.RefreshProvider(ctx, e.org, "groq")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(e.keys.bad) != 1 {
		t.Fatalf("expected first key marked, got %v", e.keys.bad)
	}
	var st string
	e.pool.QueryRow(ctx, `SELECT status FROM provider_keys WHERE id=$1`, e.keys.bad[0]).Scan(&st)
	if st != "error" {
		t.Fatalf("status=%s", st)
	}
}

func TestRefreshErrorKeepsRows(t *testing.T) {
	e := newEnv(t, "groq")
	ctx := context.Background()
	e.provs["groq"].set("llama-3.1-8b-instant")
	if _, err := e.disc.RefreshProvider(ctx, e.org, "groq"); err != nil {
		t.Fatal(err)
	}
	e.provs["groq"].keyErr["groq-key-1"] = &providers.ProviderError{Class: providers.ErrorClassTransient, HTTP: 503}
	if _, err := e.disc.RefreshProvider(ctx, e.org, "groq"); err == nil {
		t.Fatal("expected error")
	}
	refs, _ := e.repo.Refreshes(ctx, e.org)
	if refs[0].LastStatus != "error" || refs[0].LastErrorClass != "transient" {
		t.Fatalf("%+v", refs[0])
	}
	rows, _ := e.repo.List(ctx, e.org, "", "", true)
	if len(rows) != 1 {
		t.Fatalf("rows kept expected, got %d", len(rows))
	}
}

func TestRefreshInProgress(t *testing.T) {
	e := newEnv(t, "groq")
	ctx := context.Background()
	e.provs["groq"].block = make(chan struct{})
	e.provs["groq"].set("llama-3.1-8b-instant")
	done := make(chan error, 1)
	go func() { _, err := e.disc.RefreshProvider(ctx, e.org, "groq"); done <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for e.provs["groq"].calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := e.disc.RefreshProvider(ctx, e.org, "groq"); err != ErrInProgress {
		t.Fatalf("want ErrInProgress, got %v", err)
	}
	close(e.provs["groq"].block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOnDemandDiscoveryAndDebounce(t *testing.T) {
	e := newEnv(t, "groq")
	ctx := context.Background()
	e.provs["groq"].set("llama-3.1-8b-instant")
	if _, err := e.disc.RefreshProvider(ctx, e.org, "groq"); err != nil {
		t.Fatal(err)
	}
	// Provider starts listing new-model-x after the last refresh; last refresh is old.
	e.provs["groq"].set("llama-3.1-8b-instant", "new-model-x")
	e.pool.Exec(ctx, `UPDATE org_catalog_refreshes SET last_refreshed_at = NOW() - interval '1 hour' WHERE org_id=$1`, e.org)

	before := e.provs["groq"].calls.Load()
	got, err := e.res.Resolve(ctx, e.org, "new-model-x")
	if err != nil || len(got) != 1 || got[0].ProviderModelID != "new-model-x" {
		t.Fatalf("on-demand: %+v err=%v", got, err)
	}
	if e.provs["groq"].calls.Load() != before+1 {
		t.Fatal("expected exactly one listing call")
	}
	var src string
	e.pool.QueryRow(ctx, `SELECT source FROM org_model_catalog WHERE org_id=$1 AND provider_model_id='new-model-x'`, e.org).Scan(&src)
	if src != "on_demand" {
		t.Fatalf("source=%q", src)
	}

	// Unknown model: first miss refreshes (if stale), second within 60 s does not call the provider.
	e.pool.Exec(ctx, `UPDATE org_catalog_refreshes SET last_refreshed_at = NOW() - interval '1 hour' WHERE org_id=$1`, e.org)
	if got, _ := e.res.Resolve(ctx, e.org, "nope-model"); got != nil {
		t.Fatalf("expected not found: %+v", got)
	}
	calls := e.provs["groq"].calls.Load()
	e.pool.Exec(ctx, `UPDATE org_catalog_refreshes SET last_refreshed_at = NOW() - interval '1 hour' WHERE org_id=$1`, e.org)
	if got, _ := e.res.Resolve(ctx, e.org, "nope-model"); got != nil {
		t.Fatalf("expected not found: %+v", got)
	}
	if e.provs["groq"].calls.Load() != calls {
		t.Fatal("debounced miss must not call provider")
	}
}
