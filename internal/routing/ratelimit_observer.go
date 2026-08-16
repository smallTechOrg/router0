package routing

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/zer0-router/zer0/internal/providers"
)

// RateLimitObserver records observed rate limit headers and updates Redis urgency flags.
type RateLimitObserver struct {
	pg    *pgxpool.Pool
	redis *redis.Client
}

func NewRateLimitObserver(pg *pgxpool.Pool, r *redis.Client) *RateLimitObserver {
	return &RateLimitObserver{pg: pg, redis: r}
}

// Observe records a rate limit observation from a provider response.
// It also sets an urgency flag in Redis when remaining capacity is low.
func (o *RateLimitObserver) Observe(ctx context.Context, provider, model string, rl *providers.RateLimitHeaders) {
	if rl == nil {
		return
	}
	if rl.LimitRPM == 0 && rl.LimitTPM == 0 && rl.LimitTPD == 0 {
		return
	}

	// Persist observation for learning
	go func() {
		_, err := o.pg.Exec(context.Background(), `
			INSERT INTO rate_limit_observations (provider, model_id, limit_rpm, limit_tpm, limit_tpd, source)
			VALUES ($1,$2,$3,$4,$5,'header')`,
			provider, model, nullableInt(rl.LimitRPM), nullableInt(rl.LimitTPM), nullableInt(rl.LimitTPD))
		if err != nil {
			slog.Debug("rate_limit_observer: insert failed", "error", err)
		}
	}()

	// Set urgency flags in Redis so the scorer can deprioritize exhausted combos
	if rl.LimitRPM > 0 && rl.RemainingRPM >= 0 {
		pct := float64(rl.RemainingRPM) / float64(rl.LimitRPM)
		urgencyKey := "rl_urgency:" + provider + ":" + model
		if pct < 0.10 {
			// <10% RPM remaining — mark as near-exhausted for 30s
			o.redis.Set(ctx, urgencyKey, strconv.FormatFloat(pct, 'f', 4, 64), 30_000_000_000)
			slog.Warn("rate_limit: near RPM exhaustion", "provider", provider, "model", model,
				"remaining", rl.RemainingRPM, "limit", rl.LimitRPM)
		} else {
			o.redis.Del(ctx, urgencyKey)
		}
	}
}

// MarkExhausted flags a key:model combo as rate-limited for 60 seconds.
// Called when a 429 is received so future requests skip this combo immediately.
func (o *RateLimitObserver) MarkExhausted(ctx context.Context, keyID, provider, model string) {
	key := "rl_exhaust:" + keyID + ":" + provider + ":" + model
	o.redis.Set(ctx, key, "1", 60_000_000_000)
}

// IsKeyExhausted checks if a specific key:provider:model combo is rate-limited right now.
func (o *RateLimitObserver) IsKeyExhausted(ctx context.Context, keyID, provider, model string) bool {
	key := "rl_exhaust:" + keyID + ":" + provider + ":" + model
	v, err := o.redis.Exists(ctx, key).Result()
	return err == nil && v > 0
}

// IsNearExhaustion returns true if a combo is flagged as near rate-limit exhaustion.
func (o *RateLimitObserver) IsNearExhaustion(ctx context.Context, provider, model string) bool {
	key := "rl_urgency:" + provider + ":" + model
	v, err := o.redis.Exists(ctx, key).Result()
	return err == nil && v > 0
}

func nullableInt(n int) interface{} {
	if n == 0 {
		return nil
	}
	return n
}
