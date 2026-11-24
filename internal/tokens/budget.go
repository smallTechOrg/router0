package tokens

import (
	"context"
	"errors"
	"log/slog"
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
	cache *cache.TTL[string, BudgetStatus]
}

type budgetDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// NewBudgetGuard builds a guard; ttl 0 means 2 s.
func NewBudgetGuard(db budgetDB, ttl time.Duration) *BudgetGuard {
	if ttl <= 0 {
		ttl = 2 * time.Second
	}
	return &BudgetGuard{db: db, cache: cache.New[string, BudgetStatus](ttl, 10000)}
}

// Check returns the token's budget status. It fails open: on a database error
// the token is reported as unlimited and a warning is logged.
func (g *BudgetGuard) Check(ctx context.Context, tokenID string) BudgetStatus {
	if tokenID == "" {
		return BudgetStatus{}
	}
	if st, ok := g.cache.Get(tokenID); ok {
		return st
	}
	var budget *float64
	var spendMicro int64
	err := g.db.QueryRow(ctx,
		`SELECT t.monthly_budget_usd::float8, COALESCE(sp.spend_micro_usd, 0)
		   FROM service_tokens t
		   LEFT JOIN service_token_spend sp
		     ON sp.service_token_id = t.id AND sp.month = (date_trunc('month', NOW() AT TIME ZONE 'UTC'))::date
		  WHERE t.id = $1`, tokenID).Scan(&budget, &spendMicro)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("token budget check failed, failing open", "error", err.Error())
			return BudgetStatus{}
		}
	}
	st := BudgetStatus{SpendUSD: float64(spendMicro) / 1e6, spendMicro: spendMicro}
	if budget != nil {
		st.HasBudget, st.BudgetUSD, st.budgetMicro = true, *budget, int64(*budget*1e6+0.5)
	}
	g.cache.Set(tokenID, st)
	return st
}

// Evict drops the cached status (after a spend write or a budget change).
func (g *BudgetGuard) Evict(tokenID string) { g.cache.Delete(tokenID) }
