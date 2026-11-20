// Package keystate records time-boxed provider-key cooldown/exhaustion state in
// Postgres (provider_key_state) behind a short write-through in-memory cache.
package keystate

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zer0-router/zer0/internal/cache"
	"github.com/zer0-router/zer0/internal/providers"
)

// Reasons reported in State.Reason (they equal the provider_key_state.kind values).
const (
	ReasonRateLimited      = "rate_limited"
	ReasonCreditsExhausted = "credits_exhausted"
	ReasonTransient        = "transient_cooldown"
	ReasonBudgetExhausted  = "budget_exhausted"
)

// State is the effective state of one (key, model) pair. Blocked is true for every
// active row, including transient_cooldown; callers that only deprioritise
// transient keys should inspect Reason.
type State struct {
	Blocked    bool
	Reason     string // rate_limited | credits_exhausted | transient_cooldown | budget_exhausted
	RetryAfter time.Duration
}

// KeyModel identifies a (provider key, provider model id) pair.
type KeyModel struct{ KeyID, ProviderModelID string }

// Store is the contract consumed by routing and the keys handlers.
type Store interface {
	Check(ctx context.Context, keyID, providerModelID string) (State, error)
	CheckMany(ctx context.Context, pairs []KeyModel) (map[KeyModel]State, error)
	Mark(ctx context.Context, keyID, providerModelID string, e *providers.ProviderError) error
}

// DB is the subset of *pgxpool.Pool used by the store.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Options tune the store. Zero values take env/spec defaults.
type Options struct {
	CacheTTL          time.Duration // ROUTER0_KEYSTATE_CACHE_MS, default 2s
	CreditsCooldown   time.Duration // ROUTER0_CREDITS_COOLDOWN_SECONDS, default 3600s
	TransientCooldown time.Duration // ROUTER0_TRANSIENT_COOLDOWN_SECONDS, default 10s
	DefaultRateLimit  time.Duration // ROUTER0_DEFAULT_RATE_LIMIT_COOLDOWN_SECONDS, default 30s
	Now               func() time.Time
	MaxCachedKeys     int // default 10000
	Logger            *slog.Logger
}

type row struct {
	kind  string
	scope string
	until time.Time
}

type keyEntry struct{ rows []row }

// PGStore implements Store over Postgres.
type PGStore struct {
	db    DB
	opt   Options
	now   func() time.Time
	cache *cache.TTL[string, keyEntry]
	gen   atomic.Uint64 // bumped by every write; fetches that raced a write are not cached
	log   *slog.Logger
}

var _ Store = (*PGStore)(nil)

func envDur(name string, unit time.Duration, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * unit
		}
	}
	return def
}

// New builds a store over pool (any DB, typically *pgxpool.Pool).
func New(db DB, opts ...Options) *PGStore {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = envDur("ROUTER0_KEYSTATE_CACHE_MS", time.Millisecond, 2*time.Second)
	}
	if o.CreditsCooldown <= 0 {
		o.CreditsCooldown = envDur("ROUTER0_CREDITS_COOLDOWN_SECONDS", time.Second, time.Hour)
	}
	if o.TransientCooldown <= 0 {
		o.TransientCooldown = envDur("ROUTER0_TRANSIENT_COOLDOWN_SECONDS", time.Second, 10*time.Second)
	}
	if o.DefaultRateLimit <= 0 {
		o.DefaultRateLimit = providers.DefaultRateLimitCooldown()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxCachedKeys <= 0 {
		o.MaxCachedKeys = 10000
	}
	lg := o.Logger
	if lg == nil {
		lg = slog.Default()
	}
	return &PGStore{
		db:    db,
		opt:   o,
		now:   o.Now,
		cache: cache.NewWithClock[string, keyEntry](o.CacheTTL, o.MaxCachedKeys, o.Now),
		log:   lg,
	}
}

// Check returns the state of one (key, model) pair. See CheckMany.
func (s *PGStore) Check(ctx context.Context, keyID, providerModelID string) (State, error) {
	km := KeyModel{keyID, providerModelID}
	m, err := s.CheckMany(ctx, []KeyModel{km})
	return m[km], err
}

// CheckMany resolves every pair with at most one SELECT (none on a full cache
// hit). It fails open: on a database error all uncached pairs are reported as not
// blocked, a warning is logged and the error is NOT returned, so a Postgres blip
// never turns into an inference outage. The returned map has an entry for every
// requested pair.
func (s *PGStore) CheckMany(ctx context.Context, pairs []KeyModel) (map[KeyModel]State, error) {
	out := make(map[KeyModel]State, len(pairs))
	entries := make(map[string]keyEntry)
	var missing []string
	seen := map[string]bool{}
	for _, p := range pairs {
		if seen[p.KeyID] {
			continue
		}
		seen[p.KeyID] = true
		if e, ok := s.cache.Get(p.KeyID); ok {
			entries[p.KeyID] = e
		} else {
			missing = append(missing, p.KeyID)
		}
	}
	if len(missing) > 0 {
		fetched, err := s.fetch(ctx, missing)
		if err != nil {
			s.log.Warn("keystate check failed, failing open", "error", err.Error())
		} else {
			for k, e := range fetched {
				entries[k] = e
			}
		}
	}
	now := s.now()
	for _, p := range pairs {
		out[p] = resolve(entries[p.KeyID], p.ProviderModelID, now)
	}
	return out, nil
}

// fetch loads all active rows for the keys in one query and caches the result
// unless a write happened meanwhile.
func (s *PGStore) fetch(ctx context.Context, keyIDs []string) (map[string]keyEntry, error) {
	g := s.gen.Load()
	rows, err := s.db.Query(ctx,
		`SELECT provider_key_id::text, kind, scope_model, until_at
		   FROM provider_key_state
		  WHERE provider_key_id = ANY($1::uuid[]) AND until_at > NOW()`, keyIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := make(map[string]keyEntry, len(keyIDs))
	for _, id := range keyIDs {
		res[id] = keyEntry{}
	}
	for rows.Next() {
		var id string
		var r row
		if err := rows.Scan(&id, &r.kind, &r.scope, &r.until); err != nil {
			return nil, err
		}
		e := res[id]
		e.rows = append(e.rows, r)
		res[id] = e
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if s.gen.Load() == g {
		for id, e := range res {
			s.cache.Set(id, e)
		}
	}
	return res, nil
}

// resolve picks the first active row in precedence order.
func resolve(e keyEntry, model string, now time.Time) State {
	var best *row
	rank := func(r row) int {
		switch r.kind {
		case ReasonCreditsExhausted:
			return 0
		case ReasonBudgetExhausted:
			return 1
		case ReasonRateLimited:
			if r.scope == model {
				return 2
			}
			return -1
		case ReasonTransient:
			return 3
		}
		return -1
	}
	bestRank := 99
	for i := range e.rows {
		r := e.rows[i]
		if !r.until.After(now) {
			continue
		}
		if rk := rank(r); rk >= 0 && rk < bestRank {
			bestRank, best = rk, &e.rows[i]
		}
	}
	if best == nil {
		return State{}
	}
	return State{Blocked: true, Reason: best.kind, RetryAfter: best.until.Sub(now)}
}

// Mark records the cooldown implied by a classified error. Only rate_limit,
// insufficient_credits and transient classes write state; others are ignored
// (auth is stored on provider_keys by the keys package). The local cache is
// updated even when the database write fails; that error is then returned.
func (s *PGStore) Mark(ctx context.Context, keyID, providerModelID string, e *providers.ProviderError) error {
	if e == nil || keyID == "" {
		return nil
	}
	var kind, scope, reason string
	var d time.Duration
	switch e.Class {
	case providers.ErrorClassRateLimit:
		kind, scope = ReasonRateLimited, providerModelID
		d = e.RetryAfter
		if d <= 0 {
			d = s.opt.DefaultRateLimit
		}
		d = providers.ClampCooldown(d)
		reason = "provider_429"
	case providers.ErrorClassInsufficientCredit:
		kind, d, reason = ReasonCreditsExhausted, s.opt.CreditsCooldown, "provider_402"
	case providers.ErrorClassTransient:
		kind, d, reason = ReasonTransient, s.opt.TransientCooldown, "provider_error"
	default:
		return nil
	}
	if e.HTTP != 429 && e.HTTP != 402 && e.Code != "" {
		reason = "provider_code:" + truncate(e.Code, 100)
	}
	now := s.now()
	until := now.Add(d)

	s.gen.Add(1)
	s.applyLocal(keyID, row{kind: kind, scope: scope, until: until}, now)

	_, err := s.db.Exec(ctx,
		`INSERT INTO provider_key_state (provider_key_id, org_id, kind, scope_model, until_at, reason, last_error_class)
		 SELECT id, org_id, $2, $3, $4, $5, $6 FROM provider_keys WHERE id = $1
		 ON CONFLICT (provider_key_id, kind, scope_model) DO UPDATE SET
		   until_at = GREATEST(provider_key_state.until_at, EXCLUDED.until_at),
		   reason = EXCLUDED.reason,
		   last_error_class = EXCLUDED.last_error_class,
		   updated_at = NOW()`,
		keyID, kind, scope, until, reason, string(e.Class))
	if err != nil {
		s.log.Warn("keystate mark failed", "error", err.Error(), "kind", kind)
		return err
	}
	// Reconcile the cache with the DB (GREATEST may have kept a later until_at);
	// bump gen again so fetches that began before the commit are not cached.
	s.gen.Add(1)
	s.cache.Delete(keyID)
	return nil
}

// applyLocal merges a row into the cached entry for the key. If the key has no
// cached entry, the entry is left absent so the next check reads the DB, but the
// marked row is kept in a short-lived overlay entry so this instance never
// retries the key it just marked.
func (s *PGStore) applyLocal(keyID string, r row, now time.Time) {
	e, _ := s.cache.Get(keyID)
	merged := make([]row, 0, len(e.rows)+1)
	for _, x := range e.rows {
		if !x.until.After(now) {
			continue
		}
		if x.kind == r.kind && x.scope == r.scope {
			if x.until.After(r.until) {
				r.until = x.until
			}
			continue
		}
		merged = append(merged, x)
	}
	merged = append(merged, r)
	s.cache.Set(keyID, keyEntry{rows: merged})
}

// Invalidate drops cached state for a key (e.g. after it is deleted or reset).
func (s *PGStore) Invalidate(keyID string) {
	s.gen.Add(1)
	s.cache.Delete(keyID)
}

// Sweep deletes rows that expired more than an hour ago and returns how many.
func (s *PGStore) Sweep(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM provider_key_state WHERE until_at < NOW() - INTERVAL '1 hour'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
