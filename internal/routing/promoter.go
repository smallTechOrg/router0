package routing

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Thresholds (env-overridable)
func promoteMinScore() float64    { return envFloat("PROMOTE_MIN_SCORE", 0.65) }
func demoteMaxErrorRate() float64 { return envFloat("DEMOTE_MAX_ERROR_RATE", 0.25) }
func promotionWindow() string     { return "1 hour" }

// Promoter runs pool promotion/demotion based on observed request_metadata.
type Promoter struct {
	pg    *pgxpool.Pool
	redis *redis.Client
	stop  chan struct{}
}

func NewPromoter(pg *pgxpool.Pool, r *redis.Client) *Promoter {
	return &Promoter{pg: pg, redis: r, stop: make(chan struct{})}
}

func (p *Promoter) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		p.run(ctx)
		for {
			select {
			case <-ticker.C:
				p.run(ctx)
			case <-p.stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (p *Promoter) Stop() { close(p.stop) }

type comboStats struct {
	userID        string
	provider      string
	model         string
	providerKeyID string
	totalRequests int
	errorRate     float64
	avgLatencyMs  float64
	score         float64
}

func (pr *Promoter) run(ctx context.Context) {
	// First accumulate all-time stats into model_perf_stats
	pr.updatePerfStats(ctx)

	stats, err := pr.gatherStats(ctx)
	if err != nil {
		slog.Error("promoter: gather stats failed", "error", err)
		return
	}

	updated := 0
	for _, s := range stats {
		pr.pg.Exec(ctx,
			`UPDATE provider_models SET score=$1, avg_latency_ms=$2, error_rate=$3, updated_at=NOW()
			 WHERE provider=$4 AND model_id=$5`,
			s.score, int(s.avgLatencyMs), s.errorRate, s.provider, s.model,
		)

		if s.totalRequests < 3 {
			continue
		}
		member := fmt.Sprintf("%s:%s:%s", s.provider, s.model, s.providerKeyID)
		// Update score in all tier pools this combo belongs to
		for _, tier := range []Tier{TierFlashLite, TierFlash, TierPro} {
			poolKey := fmt.Sprintf("pool:%s:%s", s.userID, tier.String())
			// Only update if the member exists in this pool
			if pr.redis.ZScore(ctx, poolKey, member).Err() == nil {
				pr.redis.ZAdd(ctx, poolKey, redis.Z{Score: s.score, Member: member})
			}
		}
		updated++
	}
	slog.Info("promoter: run complete", "updated", updated, "combos_evaluated", len(stats))
}

// updatePerfStats upserts all-time aggregate stats into model_perf_stats from
// the full request_metadata history. Runs on every promoter tick.
func (pr *Promoter) updatePerfStats(ctx context.Context) {
	_, err := pr.pg.Exec(ctx, `
		INSERT INTO model_perf_stats (
			provider, model_id,
			total_requests, total_errors, total_latency_ms,
			p50_latency_ms, p95_latency_ms, p99_latency_ms,
			min_latency_ms, max_latency_ms,
			last_updated_at
		)
		SELECT
			provider,
			model AS model_id,
			COUNT(*),
			SUM(CASE WHEN status != 'success' THEN 1 ELSE 0 END),
			SUM(latency_ms),
			PERCENTILE_CONT(0.50) WITHIN GROUP (ORDER BY latency_ms)::int,
			PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms)::int,
			PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY latency_ms)::int,
			MIN(latency_ms),
			MAX(latency_ms),
			NOW()
		FROM request_metadata
		WHERE provider_key_id IS NOT NULL
		GROUP BY provider, model
		ON CONFLICT (provider, model_id) DO UPDATE SET
			total_requests   = EXCLUDED.total_requests,
			total_errors     = EXCLUDED.total_errors,
			total_latency_ms = EXCLUDED.total_latency_ms,
			p50_latency_ms   = EXCLUDED.p50_latency_ms,
			p95_latency_ms   = EXCLUDED.p95_latency_ms,
			p99_latency_ms   = EXCLUDED.p99_latency_ms,
			min_latency_ms   = EXCLUDED.min_latency_ms,
			max_latency_ms   = EXCLUDED.max_latency_ms,
			last_updated_at  = NOW()
	`)
	if err != nil {
		slog.Warn("promoter: update perf stats failed", "error", err)
	}
}

func (pr *Promoter) gatherStats(ctx context.Context) ([]comboStats, error) {
	// Use all-time data so scores accumulate. Recent data (1h) gets 2x weight
	// so the score is responsive to current conditions while retaining history.
	// Only score combos whose model still exists in provider_models and passes
	// the routing eligibility filter (probe_ok, context>=100K, params>=70B or unknown).
	rows, err := pr.pg.Query(ctx, `
		SELECT
			rm.user_id,
			rm.provider,
			rm.model,
			rm.provider_key_id::text,
			COUNT(*) AS total,
			COALESCE(
				SUM(CASE WHEN rm.status != 'success' THEN 1 ELSE 0 END)::float / NULLIF(COUNT(*),0),
			0) AS error_rate,
			COALESCE(
				(SUM(CASE WHEN rm.created_at > NOW() - INTERVAL '1 hour' THEN rm.latency_ms * 2 ELSE rm.latency_ms END)::float)
				/ NULLIF(SUM(CASE WHEN rm.created_at > NOW() - INTERVAL '1 hour' THEN 2 ELSE 1 END), 0),
			500) AS weighted_avg_latency_ms
		FROM request_metadata rm
		JOIN provider_models pm ON pm.provider = rm.provider AND pm.model_id = rm.model
		WHERE rm.provider_key_id IS NOT NULL
		  AND pm.probe_status = 'probe_ok'
		  AND pm.context_window >= 100000
		  AND pm.parameter_count_b > 0
		GROUP BY rm.user_id, rm.provider, rm.model, rm.provider_key_id
		HAVING COUNT(*) >= 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var maxLatency float64 = 5000
	var stats []comboStats
	for rows.Next() {
		var s comboStats
		if err := rows.Scan(&s.userID, &s.provider, &s.model, &s.providerKeyID,
			&s.totalRequests, &s.errorRate, &s.avgLatencyMs); err != nil {
			continue
		}
		if s.avgLatencyMs > maxLatency {
			maxLatency = s.avgLatencyMs
		}
		stats = append(stats, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range stats {
		s := &stats[i]
		latencyRatio := s.avgLatencyMs / maxLatency
		s.score = (1-s.errorRate)*0.35 + (1-latencyRatio)*0.30 + 0.20 + 1.0*0.15
		if s.score > 1 {
			s.score = 1
		}
	}
	return stats, nil
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
