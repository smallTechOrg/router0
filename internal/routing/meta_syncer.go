package routing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MetaSyncer consolidates observed rate limit data from live responses and probes
// back into provider_rate_limits so the metadata served via /admin/models is authoritative.
//
// Precedence: observed (from headers) > published (from docs/migration seeds).
// Observed values always win when available because they reflect the actual plan/tier
// this key has been granted, which may differ from published free-tier defaults.
type MetaSyncer struct {
	pg   *pgxpool.Pool
	stop chan struct{}
}

func NewMetaSyncer(pg *pgxpool.Pool) *MetaSyncer {
	return &MetaSyncer{pg: pg, stop: make(chan struct{})}
}

func (m *MetaSyncer) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		m.sync(ctx)
		for {
			select {
			case <-ticker.C:
				m.sync(ctx)
			case <-m.stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (m *MetaSyncer) Stop() { close(m.stop) }

// sync consolidates the most-recent observed rate limits (last 24h) into provider_rate_limits.
// Uses the statistical mode (most frequent value) to smooth over transient anomalies.
func (m *MetaSyncer) sync(ctx context.Context) {
	// For each provider+model, find the most-frequently observed limit values in the last 24h.
	// We use MODE() aggregate which picks the most common value — ignores one-off outliers.
	rows, err := m.pg.Query(ctx, `
		SELECT
			provider,
			model_id,
			MODE() WITHIN GROUP (ORDER BY limit_rpm) AS mode_rpm,
			MODE() WITHIN GROUP (ORDER BY limit_tpm) AS mode_tpm,
			MODE() WITHIN GROUP (ORDER BY limit_tpd) AS mode_tpd,
			COUNT(*) AS observations
		FROM rate_limit_observations
		WHERE observed_at > NOW() - INTERVAL '24 hours'
		  AND (limit_rpm IS NOT NULL OR limit_tpm IS NOT NULL OR limit_tpd IS NOT NULL)
		GROUP BY provider, model_id
		HAVING COUNT(*) >= 2
	`)
	if err != nil {
		slog.Warn("meta_syncer: query failed", "error", err)
		return
	}
	defer rows.Close()

	updated := 0
	for rows.Next() {
		var provider, modelID string
		var modeRPM, modeTPM, modeTPD *int64
		var observations int
		if err := rows.Scan(&provider, &modelID, &modeRPM, &modeTPM, &modeTPD, &observations); err != nil {
			slog.Warn("meta_syncer: scan failed", "error", err)
			continue
		}

		// Upsert into provider_rate_limits with source='observed'.
		// ON CONFLICT: only update if new observed values differ from existing — avoid pointless writes.
		notes := fmt.Sprintf("Auto-learned from response headers (%d observations)", observations)
		_, err := m.pg.Exec(ctx, `
			INSERT INTO provider_rate_limits (provider, model_id, plan, limit_rpm, limit_tpm, limit_tpd, source, notes, updated_at)
			VALUES ($1, $2, 'observed', $3, $4, $5, 'observed', $6, NOW())
			ON CONFLICT (provider, model_id, plan) DO UPDATE SET
				limit_rpm  = COALESCE(EXCLUDED.limit_rpm, provider_rate_limits.limit_rpm),
				limit_tpm  = COALESCE(EXCLUDED.limit_tpm, provider_rate_limits.limit_tpm),
				limit_tpd  = COALESCE(EXCLUDED.limit_tpd, provider_rate_limits.limit_tpd),
				notes      = EXCLUDED.notes,
				updated_at = NOW()
			WHERE provider_rate_limits.limit_rpm IS DISTINCT FROM EXCLUDED.limit_rpm
			   OR provider_rate_limits.limit_tpm IS DISTINCT FROM EXCLUDED.limit_tpm
			   OR provider_rate_limits.limit_tpd IS DISTINCT FROM EXCLUDED.limit_tpd
		`, provider, modelID, modeRPM, modeTPM, modeTPD, notes)
		if err != nil {
			slog.Warn("meta_syncer: upsert failed", "provider", provider, "model", modelID, "error", err)
			continue
		}
		updated++
	}

	slog.Info("meta_syncer: sync complete", "updated", updated)
}

// SyncNow triggers an immediate sync (used after discovery or key onboarding).
func (m *MetaSyncer) SyncNow(ctx context.Context) {
	go m.sync(ctx)
}
