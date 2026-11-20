package models

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zer0-router/zer0/internal/providers"
)

// Registry persists and retrieves discovered models.
type Registry struct {
	pool *pgxpool.Pool
}

func NewRegistry(pool *pgxpool.Pool) *Registry {
	return &Registry{pool: pool}
}

// Upsert writes a batch of discovered models for a provider.
// Models with known parameter count < 70B are skipped entirely.
func (r *Registry) Upsert(ctx context.Context, provider string, models []providers.ModelInfo) error {
	for _, m := range models {
		if m.ParameterCountB == 0 {
			continue // skip models with unknown parameter count
		}
		extra, _ := json.Marshal(m.Extra)
		var paramCount *float64
		if m.ParameterCountB > 0 {
			paramCount = &m.ParameterCountB
		}
		var arch *string
		if m.Architecture != "" {
			arch = &m.Architecture
		}
		_, err := r.pool.Exec(ctx, `
			INSERT INTO provider_models
				(provider, model_id, display_name, context_window, max_output_tokens,
				 input_cost_per_mtok, output_cost_per_mtok, supports_streaming, is_available,
				 parameter_count_b, architecture, extra, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,true,$9,$10,$11,NOW())
			ON CONFLICT (provider, model_id) DO UPDATE SET
				display_name          = EXCLUDED.display_name,
				context_window        = EXCLUDED.context_window,
				max_output_tokens     = EXCLUDED.max_output_tokens,
				input_cost_per_mtok   = EXCLUDED.input_cost_per_mtok,
				output_cost_per_mtok  = EXCLUDED.output_cost_per_mtok,
				supports_streaming    = EXCLUDED.supports_streaming,
				is_available          = true,
				parameter_count_b     = COALESCE(EXCLUDED.parameter_count_b, provider_models.parameter_count_b),
				architecture          = COALESCE(EXCLUDED.architecture, provider_models.architecture),
				extra                 = EXCLUDED.extra,
				updated_at            = NOW()`,
			provider, m.ID, m.DisplayName, m.ContextWindow, m.MaxOutputTokens,
			m.InputCostPerMTok, m.OutputCostPerMTok, m.SupportsStreaming,
			paramCount, arch, extra,
		)
		if err != nil {
			slog.Warn("model registry: upsert failed", "provider", provider, "model", m.ID, "error", err)
		}
	}
	return nil
}

// ListByProvider returns all available models for a provider.
func (r *Registry) ListByProvider(ctx context.Context, provider string) ([]providers.ModelInfo, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT model_id, display_name, context_window, max_output_tokens,
		       input_cost_per_mtok, output_cost_per_mtok, supports_streaming, extra
		FROM provider_models
		WHERE provider = $1 AND is_available = true
		ORDER BY model_id`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []providers.ModelInfo
	for rows.Next() {
		var m providers.ModelInfo
		var extraJSON []byte
		if err := rows.Scan(&m.ID, &m.DisplayName, &m.ContextWindow, &m.MaxOutputTokens,
			&m.InputCostPerMTok, &m.OutputCostPerMTok, &m.SupportsStreaming, &extraJSON); err != nil {
			continue
		}
		if len(extraJSON) > 0 {
			json.Unmarshal(extraJSON, &m.Extra)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListAll returns all available models across all providers.
func (r *Registry) ListAll(ctx context.Context) (map[string][]providers.ModelInfo, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT provider, model_id, display_name, context_window, max_output_tokens,
		       input_cost_per_mtok, output_cost_per_mtok, supports_streaming
		FROM provider_models
		WHERE is_available = true
		ORDER BY provider, model_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]providers.ModelInfo)
	for rows.Next() {
		var prov string
		var m providers.ModelInfo
		if err := rows.Scan(&prov, &m.ID, &m.DisplayName, &m.ContextWindow, &m.MaxOutputTokens,
			&m.InputCostPerMTok, &m.OutputCostPerMTok, &m.SupportsStreaming); err != nil {
			continue
		}
		out[prov] = append(out[prov], m)
	}
	return out, rows.Err()
}

// AdminKey is an admin-owned provider API key for discovery.
type AdminKey struct {
	ID         string
	Provider   string
	Ciphertext []byte
	Nonce      []byte
	Label      string
	Status     string
}

// SaveAdminKey upserts an admin provider key.
func (r *Registry) SaveAdminKey(ctx context.Context, provider string, ciphertext, nonce []byte, label string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO admin_provider_keys (provider, ciphertext, nonce, label, status, updated_at)
		VALUES ($1,$2,$3,$4,'active',NOW())
		ON CONFLICT (provider) DO UPDATE SET
			ciphertext = EXCLUDED.ciphertext,
			nonce      = EXCLUDED.nonce,
			label      = EXCLUDED.label,
			status     = 'active',
			updated_at = NOW()`,
		provider, ciphertext, nonce, label)
	return err
}

// GetAdminKey retrieves an admin provider key by provider name.
func (r *Registry) GetAdminKey(ctx context.Context, provider string) (*AdminKey, error) {
	k := &AdminKey{}
	err := r.pool.QueryRow(ctx, `
		SELECT id, provider, ciphertext, nonce, label, status
		FROM admin_provider_keys WHERE provider = $1 AND status = 'active'`, provider).
		Scan(&k.ID, &k.Provider, &k.Ciphertext, &k.Nonce, &k.Label, &k.Status)
	if err != nil {
		return nil, err
	}
	return k, nil
}

// ListAdminKeys returns all admin provider keys (without ciphertext for listing).
func (r *Registry) ListAdminKeys(ctx context.Context) ([]AdminKey, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, provider, label, status, last_discovery_at
		FROM admin_provider_keys ORDER BY provider`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []AdminKey
	for rows.Next() {
		var k AdminKey
		var lastDiscovery *time.Time
		if err := rows.Scan(&k.ID, &k.Provider, &k.Label, &k.Status, &lastDiscovery); err != nil {
			continue
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// MarkDiscoveryRun updates last_discovery_at for a provider.
func (r *Registry) MarkDiscoveryRun(ctx context.Context, provider string) {
	r.pool.Exec(ctx, `UPDATE admin_provider_keys SET last_discovery_at = NOW() WHERE provider = $1`, provider)
}
