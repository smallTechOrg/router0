package models

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/providers"
	"github.com/zer0-router/zer0/internal/routing"
)

const discoveryInterval = 6 * time.Hour

// DiscoveryJob runs periodic model discovery using admin provider keys.
type DiscoveryJob struct {
	registry   *Registry
	pg         *pgxpool.Pool
	redis      *redis.Client
	pool       *routing.Pool
	stop       chan struct{}
	onComplete func() // optional hook called after each full discovery run
}

func NewDiscoveryJob(pg *pgxpool.Pool, r *redis.Client, pool *routing.Pool) *DiscoveryJob {
	return &DiscoveryJob{
		registry: NewRegistry(pg),
		pg:       pg,
		redis:    r,
		pool:     pool,
		stop:     make(chan struct{}),
	}
}

func (j *DiscoveryJob) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(discoveryInterval)
		defer ticker.Stop()
		j.Run(ctx)
		for {
			select {
			case <-ticker.C:
				j.Run(ctx)
			case <-j.stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (j *DiscoveryJob) Stop() { close(j.stop) }

// OnComplete registers a callback invoked after each full Run() completes.
func (j *DiscoveryJob) OnComplete(fn func()) { j.onComplete = fn }

// Run discovers models for all providers. Uses admin keys when available,
// falls back to any active user key for providers without an admin key.
func (j *DiscoveryJob) Run(ctx context.Context) {
	defer func() {
		if j.onComplete != nil {
			j.onComplete()
		}
	}()
	// Build map of providers covered by admin keys
	adminKeys, _ := j.registry.ListAdminKeys(ctx)
	adminProviders := map[string]bool{}
	for _, ak := range adminKeys {
		if ak.Status == "active" {
			adminProviders[ak.Provider] = true
		}
	}

	// Run discovery for admin-keyed providers
	for _, ak := range adminKeys {
		if ak.Status != "active" {
			continue
		}
		full, err := j.registry.GetAdminKey(ctx, ak.Provider)
		if err != nil {
			continue
		}
		decrypted, err := keys.Decrypt(full.Ciphertext, full.Nonce)
		if err != nil {
			slog.Warn("discovery: decrypt admin key failed", "provider", ak.Provider)
			continue
		}
		j.runDiscoveryWithKey(ctx, ak.Provider, decrypted)
	}

	// For providers with user keys but no admin key, use any active user key.
	// Prefer keys that haven't failed auth (last_test_result not an auth error), newest first.
	rows, err := j.pg.Query(ctx,
		`SELECT DISTINCT ON (provider) provider, ciphertext, nonce
		 FROM provider_keys
		 WHERE status = 'active'
		   AND COALESCE(last_test_result, 'ok') NOT LIKE 'error:%'
		 ORDER BY provider, created_at DESC`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var provider string
		var ciphertext, nonce []byte
		if err := rows.Scan(&provider, &ciphertext, &nonce); err != nil {
			continue
		}
		if adminProviders[provider] {
			continue // already covered
		}
		decrypted, err := keys.Decrypt(ciphertext, nonce)
		if err != nil {
			continue
		}
		j.runDiscoveryWithKey(ctx, provider, decrypted)
	}
}

func (j *DiscoveryJob) runDiscoveryWithKey(ctx context.Context, provider, apiKey string) {
	prov, err := providers.Get(provider)
	if err != nil {
		return
	}
	discovered, err := prov.ListModels(ctx, apiKey)
	if err != nil {
		slog.Warn("discovery: ListModels failed", "provider", provider, "error", err)
		return
	}
	if len(discovered) == 0 {
		slog.Debug("discovery: no models returned", "provider", provider)
		return
	}
	if err := j.registry.Upsert(ctx, provider, discovered); err == nil {
		j.registry.MarkDiscoveryRun(ctx, provider)
	}
	slog.Info("discovery: models discovered", "provider", provider, "count", len(discovered))
	j.backfillPool(ctx, provider, discovered)
}

// RunForProvider triggers immediate discovery for one provider using any available key.
// Falls back to user keys if no admin key exists.
func (j *DiscoveryJob) RunForProvider(ctx context.Context, provider, apiKey string) ([]providers.ModelInfo, error) {
	prov, err := providers.Get(provider)
	if err != nil {
		return nil, err
	}
	discovered, err := prov.ListModels(ctx, apiKey)
	if err != nil {
		return nil, err
	}
	if len(discovered) > 0 {
		j.registry.Upsert(ctx, provider, discovered)
		slog.Info("discovery: triggered for provider", "provider", provider, "count", len(discovered))
		j.backfillPool(ctx, provider, discovered)
	}
	return discovered, nil
}

// backfillPool ensures every user key for a provider has pool combos for all discovered models.
func (j *DiscoveryJob) backfillPool(ctx context.Context, provider string, discovered []providers.ModelInfo) {
	rows, err := j.pg.Query(ctx,
		`SELECT id, user_id FROM provider_keys WHERE provider = $1 AND status = 'active'`, provider)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var keyID, userID string
		if err := rows.Scan(&keyID, &userID); err != nil {
			continue
		}
		for _, m := range discovered {
			if m.ParameterCountB <= 0 {
				continue
			}
			j.pool.AddComboToAll(ctx, userID, provider, m.ID, keyID, m.ParameterCountB, m.ContextWindow)
		}
	}
}
