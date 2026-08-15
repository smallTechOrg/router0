package health

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/providers"
)

const healthCacheTTL = 30 * time.Minute

type Prober struct {
	redis   *redis.Client
	keyRepo *keys.Repository
}

func NewProber(r *redis.Client, keyRepo *keys.Repository) *Prober {
	return &Prober{redis: r, keyRepo: keyRepo}
}

// Probe runs a 1-token health check for the given combo.
func (p *Prober) Probe(ctx context.Context, providerName, model, keyID, userID string) error {
	lockKey := fmt.Sprintf("probe_lock:%s:%s:%s", providerName, model, keyID)
	set, err := p.redis.SetNX(ctx, lockKey, "1", 5*time.Minute).Result()
	if err != nil || !set {
		return nil // already probing or lock error
	}
	defer p.redis.Del(ctx, lockKey)

	k, err := p.keyRepo.GetByID(ctx, keyID, userID)
	if err != nil {
		return err
	}
	decrypted, err := keys.Decrypt(k.Ciphertext, k.Nonce)
	if err != nil {
		return err
	}

	prov, err := providers.Get(providerName)
	if err != nil {
		return err
	}

	result, err := prov.HealthCheck(ctx, decrypted)
	if err != nil {
		result = &providers.HealthResult{OK: false, Error: err.Error()}
	}

	status := "ok"
	if !result.OK {
		status = "error"
	}

	cacheKey := fmt.Sprintf("health:%s:%s", providerName, model)
	p.redis.HSet(ctx, cacheKey,
		"status", status,
		"latency_ms", strconv.FormatInt(result.LatencyMs, 10),
		"checked_at", time.Now().UTC().Format(time.RFC3339),
	)
	p.redis.Expire(ctx, cacheKey, healthCacheTTL)

	slog.Info("health_probe",
		"provider", providerName,
		"model", model,
		"key_id", keyID,
		"status", status,
		"latency_ms", result.LatencyMs,
	)
	return nil
}

// GetCached returns the cached health result for a provider/model.
func (p *Prober) GetCached(ctx context.Context, providerName, model string) (status string, latencyMs int64, checkedAt time.Time) {
	cacheKey := fmt.Sprintf("health:%s:%s", providerName, model)
	vals, err := p.redis.HGetAll(ctx, cacheKey).Result()
	if err != nil || len(vals) == 0 {
		return "unknown", 0, time.Time{}
	}
	status = vals["status"]
	if v, ok := vals["latency_ms"]; ok {
		latencyMs, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := vals["checked_at"]; ok {
		checkedAt, _ = time.Parse(time.RFC3339, v)
	}
	return
}
