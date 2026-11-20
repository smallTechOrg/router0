package routing

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Remediator applies auto-corrections based on provider errors.
type Remediator struct {
	pg   *pgxpool.Pool
	pool *Pool
}

func NewRemediator(pg *pgxpool.Pool, pool *Pool) *Remediator {
	return &Remediator{pg: pg, pool: pool}
}

// HandleNotFound marks a model as probe_error and removes it from all pools
// for all users. A 404 means the model doesn't exist on this provider.
func (rm *Remediator) HandleNotFound(ctx context.Context, provider, model string) {
	_, err := rm.pg.Exec(ctx,
		`UPDATE provider_models SET probe_status = 'probe_error', is_available = false, updated_at = now()
		 WHERE provider = $1 AND model_id = $2 AND probe_status != 'probe_error'`,
		provider, model)
	if err != nil {
		slog.Error("remediate: failed to mark model as probe_error", "provider", provider, "model", model, "error", err)
		return
	}
	slog.Warn("remediate: model 404 — marked probe_error and removing from pools",
		"provider", provider, "model", model)

	// Remove from all users' pools
	rows, err := rm.pg.Query(ctx,
		`SELECT DISTINCT user_id FROM provider_keys WHERE provider = $1 AND status = 'active'`, provider)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var userID string
		if rows.Scan(&userID) == nil {
			rm.pool.RemoveModelFromPools(ctx, userID, provider, model)
		}
	}
}

// HandleAuthError zeros all combos for a provider key across all tiers.
func (rm *Remediator) HandleAuthError(ctx context.Context, userID, provider, keyID string) {
	rows, err := rm.pg.Query(ctx,
		`SELECT model_id FROM provider_models WHERE provider = $1 AND probe_status = 'probe_ok'`, provider)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var modelID string
		if rows.Scan(&modelID) == nil {
			for _, tier := range []Tier{TierFlashLite, TierFlash, TierPro} {
				rm.pool.UpdateScore(ctx, userID, provider, modelID, keyID, tier, 0.0)
			}
		}
	}
	slog.Warn("remediate: auth error — zeroed all combos for key", "provider", provider, "key_id", keyID)
}

