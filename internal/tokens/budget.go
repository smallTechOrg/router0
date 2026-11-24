package tokens

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zer0-router/zer0/internal/cache"
)

// SetBudget sets (budget != nil) or clears (nil) a live token's monthly USD
// budget. ErrNotFound if the token is not a live token of orgID.
func (r *Repository) SetBudget(ctx context.Context, orgID, tokenID string, budget *float64) error {
	if !uuidRe.MatchString(tokenID) {
		return ErrNotFound
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE service_tokens SET monthly_budget_usd = $3::numeric
		  WHERE id = $1 AND org_id = $2 AND revoked_at IS NULL`, tokenID, orgID, budget)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MaxAllowedModels bounds a token's allowlist.
const MaxAllowedModels = 200

// SetAllowedModels sets (models != nil) or clears (nil) a live token's model
// allowlist. Entries must already be normalised (lowercased, de-duplicated).
// ErrNotFound if the token is not a live token of orgID.
func (r *Repository) SetAllowedModels(ctx context.Context, orgID, tokenID string, models []string) error {
	if !uuidRe.MatchString(tokenID) {
		return ErrNotFound
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE service_tokens SET allowed_models = $3::text[]
		  WHERE id = $1 AND org_id = $2 AND revoked_at IS NULL`, tokenID, orgID, models)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// BudgetStatus is a token's budget position this UTC month.
type BudgetStatus struct {
	HasBudget   bool
	BudgetUSD   float64
	SpendUSD    float64
	budgetMicro int64
	spendMicro  int64
}

// Exceeded reports whether the token has a budget and has reached it
// (compared in whole micro-USD, like key budgets).
func (b BudgetStatus) Exceeded() bool { return b.HasBudget && b.spendMicro >= b.budgetMicro }

// BudgetGuard answers "may this token spend more this month?" for the
// inference path with a short cache. Spend writes (metadata recorder) and
// budget changes evict the token's entry, so a reached budget applies to the
// next request on this instance.
type BudgetGuard struct {
	db    budgetDB
	cache *cache.TTL[string, Policy]
	gen   atomic.Uint64 // bumped by every eviction; a read that raced one is not cached
}

// Policy is everything the inference path enforces per token: its budget
// position and its model allowlist.
type Policy struct {
	Budget BudgetStatus
	// allowed holds lowercased canonical/alias names; nil = every model.
	allowed map[string]bool
}

// Restricted reports whether the token has a model allowlist.
func (p Policy) Restricted() bool { return p.allowed != nil }

// Allows reports whether a request for requested (as sent), resolved to
// canonical, may be served: true without an allowlist, else when either name
// is on it (an allowlisted alias works, as does any name that resolves to an
// allowlisted canonical model).
func (p Policy) Allows(canonical, requested string) bool {
	if p.allowed == nil {
		return true
	}
	return p.allowed[strings.ToLower(strings.TrimSpace(canonical))] || p.allowed[strings.ToLower(strings.TrimSpace(requested))]
}

type budgetDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// NewBudgetGuard builds a guard; ttl 0 means 2 s.
func NewBudgetGuard(db budgetDB, ttl time.Duration) *BudgetGuard {
	if ttl <= 0 {
		ttl = 2 * time.Second
	}
	return &BudgetGuard{db: db, cache: cache.New[string, Policy](ttl, 10000)}
}

// Check returns the token's budget status (see Policy).
func (g *BudgetGuard) Check(ctx context.Context, tokenID string) BudgetStatus {
	return g.Policy(ctx, tokenID).Budget
}

// Policy returns the token's budget status and model allowlist. It fails open:
// on a database error the token is reported as unlimited and unrestricted and a
// warning is logged.
func (g *BudgetGuard) Policy(ctx context.Context, tokenID string) Policy {
	if tokenID == "" {
		return Policy{}
	}
	if p, ok := g.cache.Get(tokenID); ok {
		return p
	}
	gen := g.gen.Load()
	var budget *float64
	var spendMicro int64
	var allowed []string
	err := g.db.QueryRow(ctx,
		`SELECT t.monthly_budget_usd::float8, COALESCE(sp.spend_micro_usd, 0), t.allowed_models
		   FROM service_tokens t
		   LEFT JOIN service_token_spend sp
		     ON sp.service_token_id = t.id AND sp.month = (date_trunc('month', NOW() AT TIME ZONE 'UTC'))::date
		  WHERE t.id = $1`, tokenID).Scan(&budget, &spendMicro, &allowed)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("token policy check failed, failing open", "error", err.Error())
		return Policy{}
	}
	p := Policy{Budget: BudgetStatus{SpendUSD: float64(spendMicro) / 1e6, spendMicro: spendMicro}}
	if budget != nil {
		p.Budget.HasBudget, p.Budget.BudgetUSD, p.Budget.budgetMicro = true, *budget, int64(*budget*1e6+0.5)
	}
	if allowed != nil {
		p.allowed = make(map[string]bool, len(allowed))
		for _, m := range allowed {
			p.allowed[strings.ToLower(m)] = true
		}
	}
	if g.gen.Load() == gen {
		g.cache.Set(tokenID, p)
	}
	return p
}

// Evict drops the cached policy (after a budget or allowlist change). Reads
// that were in flight are not cached, so the change applies to the next request.
func (g *BudgetGuard) Evict(tokenID string) {
	g.gen.Add(1)
	g.cache.Delete(tokenID)
}

// SpendChanged is called after the token's spend was written (metadata
// recorder). Only a token with a budget depends on spend; others keep their
// cached policy so steady traffic does not re-query on every request.
func (g *BudgetGuard) SpendChanged(tokenID string) {
	if p, ok := g.cache.Get(tokenID); ok && !p.Budget.HasBudget {
		return
	}
	g.Evict(tokenID)
}
