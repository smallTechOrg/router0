package keystate

import (
	"context"
	"fmt"
	"github.com/zer0-router/zer0/internal/testutil/testdb"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zer0-router/zer0/internal/providers"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// countingDB wraps a DB and counts Query calls.
type countingDB struct {
	DB
	mu      sync.Mutex
	queries int
}

func (c *countingDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.mu.Lock()
	c.queries++
	c.mu.Unlock()
	return c.DB.Query(ctx, sql, args...)
}
func (c *countingDB) count() int { c.mu.Lock(); defer c.mu.Unlock(); return c.queries }

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	testdb.Migrate(t, dsn)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("cannot ping: %v", err)
	}
	var reg *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('provider_key_state')::text`).Scan(&reg); err != nil || reg == nil {
		t.Skip("table provider_key_state missing (migration 000019 not applied)")
	}
	return pool
}

// seedKey creates user, org and provider key; returns org and key ids. Cleans up.
func seedKey(t *testing.T, pool *pgxpool.Pool) (orgID, keyID string) {
	t.Helper()
	ctx := context.Background()
	uniq := fmt.Sprintf("%d", time.Now().UnixNano())
	var userID string
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1,'x') RETURNING id::text`, "keystate-"+uniq+"@example.test").Scan(&userID))
	must(pool.QueryRow(ctx, `INSERT INTO orgs (name) VALUES ($1) RETURNING id::text`, "keystate-"+uniq).Scan(&orgID))
	must(pool.QueryRow(ctx, `INSERT INTO provider_keys (user_id, org_id, provider, label, ciphertext, nonce) VALUES ($1,$2,'groq','k-'||$3,'\x00','\x00') RETURNING id::text`, userID, orgID, uniq).Scan(&keyID))
	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM orgs WHERE id=$1`, orgID) // cascades keys + state
		pool.Exec(c, `DELETE FROM users WHERE id=$1`, userID)
	})
	return
}

func newTestStore(t *testing.T, pool *pgxpool.Pool) (*PGStore, *countingDB, *clock) {
	cdb := &countingDB{DB: pool}
	clk := &clock{t: time.Now()}
	return New(cdb, Options{Now: clk.Now}), cdb, clk
}

func TestMarkAndCheckPerClass(t *testing.T) {
	pool := testPool(t)
	_, key := seedKey(t, pool)
	s, _, _ := newTestStore(t, pool)
	ctx := context.Background()

	st, err := s.Check(ctx, key, "m1")
	if err != nil || st.Blocked {
		t.Fatalf("fresh key: %+v %v", st, err)
	}

	// auth / bad_request / model_unavailable write nothing.
	for _, c := range []providers.ErrorClass{providers.ErrorClassAuth, providers.ErrorClassBadRequest, providers.ErrorClassModelUnavailable} {
		if err := s.Mark(ctx, key, "m1", &providers.ProviderError{Class: c, HTTP: 400}); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := s.Check(ctx, key, "m1"); st.Blocked {
		t.Fatalf("auth/bad_request must not block: %+v", st)
	}

	// rate limit: per model.
	if err := s.Mark(ctx, key, "m1", &providers.ProviderError{Class: providers.ErrorClassRateLimit, HTTP: 429, RetryAfter: 20 * time.Second}); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Check(ctx, key, "m1")
	if !st.Blocked || st.Reason != ReasonRateLimited || st.RetryAfter < 19*time.Second || st.RetryAfter > 20*time.Second {
		t.Fatalf("rate limited m1: %+v", st)
	}
	if st, _ := s.Check(ctx, key, "m2"); st.Blocked {
		t.Fatalf("other model must not be blocked: %+v", st)
	}

	// transient: key-wide, shorter; rate_limited still wins for m1 (precedence).
	if err := s.Mark(ctx, key, "m2", &providers.ProviderError{Class: providers.ErrorClassTransient, HTTP: 503}); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Check(ctx, key, "m2"); !st.Blocked || st.Reason != ReasonTransient || st.RetryAfter > 10*time.Second {
		t.Fatalf("transient m2: %+v", st)
	}
	if st, _ := s.Check(ctx, key, "m1"); st.Reason != ReasonRateLimited {
		t.Fatalf("rate_limited should beat transient: %+v", st)
	}

	// credits: key-wide, beats everything.
	if err := s.Mark(ctx, key, "m1", &providers.ProviderError{Class: providers.ErrorClassInsufficientCredit, HTTP: 402, Code: "insufficient_quota"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"m1", "m2", "m3"} {
		st, _ := s.Check(ctx, key, m)
		if !st.Blocked || st.Reason != ReasonCreditsExhausted || st.RetryAfter < 59*time.Minute {
			t.Fatalf("credits %s: %+v", m, st)
		}
	}

	// Postgres is the source of truth: a brand-new store sees it.
	s2 := New(pool)
	if st, _ := s2.Check(ctx, key, "m3"); st.Reason != ReasonCreditsExhausted {
		t.Fatalf("fresh store should read state from DB: %+v", st)
	}
	var reason, lec string
	if err := pool.QueryRow(ctx, `SELECT reason, last_error_class FROM provider_key_state WHERE provider_key_id=$1 AND kind='credits_exhausted'`, key).Scan(&reason, &lec); err != nil {
		t.Fatal(err)
	}
	if reason != "provider_402" || lec != "insufficient_credits" {
		t.Fatalf("row: %q %q", reason, lec)
	}
}

func TestMarkNeverShortensAndDefaultsRetryAfter(t *testing.T) {
	pool := testPool(t)
	_, key := seedKey(t, pool)
	s, _, _ := newTestStore(t, pool)
	ctx := context.Background()
	s.Mark(ctx, key, "m", &providers.ProviderError{Class: providers.ErrorClassRateLimit, HTTP: 429, RetryAfter: 100 * time.Second})
	s.Mark(ctx, key, "m", &providers.ProviderError{Class: providers.ErrorClassRateLimit, HTTP: 429, RetryAfter: 5 * time.Second})
	if st, _ := New(pool).Check(ctx, key, "m"); st.RetryAfter < 90*time.Second {
		t.Fatalf("GREATEST should keep the longer cooldown: %+v", st)
	}
	_, key2 := seedKey(t, pool)
	s.Mark(ctx, key2, "m", &providers.ProviderError{Class: providers.ErrorClassRateLimit, HTTP: 429})
	if st, _ := s.Check(ctx, key2, "m"); st.RetryAfter < 29*time.Second || st.RetryAfter > 30*time.Second {
		t.Fatalf("default cooldown 30s: %+v", st)
	}
}

func TestCheckManyOneQueryAndCache(t *testing.T) {
	pool := testPool(t)
	_, k1 := seedKey(t, pool)
	_, k2 := seedKey(t, pool)
	cdb := &countingDB{DB: pool}
	clk := &clock{t: time.Now()}
	s := New(cdb, Options{Now: clk.Now, CacheTTL: 2 * time.Second})
	ctx := context.Background()

	// Seed k1 via a separate store so s's cache is cold.
	New(pool).Mark(ctx, k1, "m", &providers.ProviderError{Class: providers.ErrorClassRateLimit, HTTP: 429, RetryAfter: 60 * time.Second})

	pairs := []KeyModel{{k1, "m"}, {k1, "other"}, {k2, "m"}}
	got, err := s.CheckMany(ctx, pairs)
	if err != nil {
		t.Fatal(err)
	}
	if cdb.count() != 1 {
		t.Fatalf("want exactly 1 query, got %d", cdb.count())
	}
	if !got[KeyModel{k1, "m"}].Blocked || got[KeyModel{k1, "other"}].Blocked || got[KeyModel{k2, "m"}].Blocked {
		t.Fatalf("states: %+v", got)
	}
	// cache hit
	s.CheckMany(ctx, pairs)
	if cdb.count() != 1 {
		t.Fatalf("cache hit should not query, queries=%d", cdb.count())
	}
	// another instance marks k2; s doesn't see it until cache TTL passes
	New(pool).Mark(ctx, k2, "m", &providers.ProviderError{Class: providers.ErrorClassInsufficientCredit, HTTP: 402})
	if got, _ = s.CheckMany(ctx, pairs); got[KeyModel{k2, "m"}].Blocked {
		t.Fatal("stale cache expected within TTL")
	}
	clk.Add(3 * time.Second)
	if got, _ = s.CheckMany(ctx, pairs); !got[KeyModel{k2, "m"}].Blocked {
		t.Fatal("expired cache should re-read DB")
	}
	if cdb.count() != 2 {
		t.Fatalf("queries=%d", cdb.count())
	}
}

func TestMarkWritesThroughLocalCache(t *testing.T) {
	pool := testPool(t)
	_, key := seedKey(t, pool)
	s, cdb, _ := newTestStore(t, pool)
	ctx := context.Background()
	s.Check(ctx, key, "m") // warm cache with "not blocked"
	if err := s.Mark(ctx, key, "m", &providers.ProviderError{Class: providers.ErrorClassInsufficientCredit, HTTP: 402}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Check(ctx, key, "m")
	if !st.Blocked || st.Reason != ReasonCreditsExhausted {
		t.Fatalf("same instance must see its own Mark immediately: %+v (queries %d)", st, cdb.count())
	}
}

func TestExpiredRowsIgnoredAndSwept(t *testing.T) {
	pool := testPool(t)
	_, key := seedKey(t, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO provider_key_state (provider_key_id, org_id, kind, scope_model, until_at)
		SELECT id, org_id, 'transient_cooldown', '', NOW() - INTERVAL '2 hours' FROM provider_keys WHERE id=$1`, key); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	if st, _ := s.Check(ctx, key, "m"); st.Blocked {
		t.Fatalf("expired row must be ignored: %+v", st)
	}
	if n, err := s.Sweep(ctx); err != nil || n < 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	var cnt int
	pool.QueryRow(ctx, `SELECT count(*) FROM provider_key_state WHERE provider_key_id=$1`, key).Scan(&cnt)
	if cnt != 0 {
		t.Fatalf("row should be deleted, %d left", cnt)
	}
}

func TestMarkUnknownKeyAndNil(t *testing.T) {
	pool := testPool(t)
	s := New(pool)
	ctx := context.Background()
	if err := s.Mark(ctx, "00000000-0000-0000-0000-000000000000", "m", &providers.ProviderError{Class: providers.ErrorClassTransient}); err != nil {
		t.Fatalf("unknown key is a no-op insert: %v", err)
	}
	if err := s.Mark(ctx, "x", "m", nil); err != nil {
		t.Fatal(err)
	}
}

// Pure unit tests (no DB).

func TestResolvePrecedence(t *testing.T) {
	now := time.Now()
	e := keyEntry{rows: []row{
		{kind: ReasonTransient, until: now.Add(5 * time.Second)},
		{kind: ReasonRateLimited, scope: "m", until: now.Add(30 * time.Second)},
		{kind: ReasonBudgetExhausted, until: now.Add(time.Hour)},
		{kind: ReasonCreditsExhausted, until: now.Add(2 * time.Hour)},
		{kind: ReasonRateLimited, scope: "other", until: now.Add(time.Hour)},
	}}
	if st := resolve(e, "m", now); st.Reason != ReasonCreditsExhausted {
		t.Fatalf("%+v", st)
	}
	e.rows = e.rows[:3]
	if st := resolve(e, "m", now); st.Reason != ReasonBudgetExhausted {
		t.Fatalf("%+v", st)
	}
	e.rows = e.rows[:2]
	if st := resolve(e, "m", now); st.Reason != ReasonRateLimited || st.RetryAfter != 30*time.Second {
		t.Fatalf("%+v", st)
	}
	if st := resolve(e, "zzz", now); st.Reason != ReasonTransient {
		t.Fatalf("model-scoped limit must not apply to other models: %+v", st)
	}
	if st := resolve(e, "m", now.Add(time.Minute)); st.Blocked {
		t.Fatalf("all expired: %+v", st)
	}
}
