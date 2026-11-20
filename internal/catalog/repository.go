package catalog

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Entry is one (canonical model -> provider model) mapping returned by the Resolver.
type Entry struct{ Canonical, Provider, ProviderModelID, Kind string }

// Model is a discovered model ready to be stored.
type Model struct {
	ProviderModelID   string
	Kind              string // chat | embedding
	ContextWindow     int
	InputCostPerMTok  float64
	OutputCostPerMTok float64
}

// Row is a stored catalog row.
type Row struct {
	Canonical       string
	Provider        string
	ProviderModelID string
	Kind            string
	Source          string
	Available       bool
	LastSeenAt      time.Time
}

// Refresh is a row of org_catalog_refreshes.
type Refresh struct {
	Provider        string
	LastRefreshedAt time.Time
	LastStatus      string
	LastErrorClass  string
	ModelCount      int
}

// OrgProvider is an (org, provider) pair that has at least one active key.
type OrgProvider struct{ OrgID, Provider string }

// Repository persists org_model_catalog and org_catalog_refreshes.
type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// ApplyListing upserts every listed model (available, last_seen_at=now), marks
// the org+provider's unlisted rows unavailable, and records an ok refresh, all
// in one transaction. source is applied to newly inserted rows only.
func (r *Repository) ApplyListing(ctx context.Context, orgID, provider, source string, models []Model) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	ids := make([]string, 0, len(models))
	batch := &pgx.Batch{}
	for _, m := range models {
		ids = append(ids, m.ProviderModelID)
		kind := m.Kind
		if kind == "" {
			kind = "chat"
		}
		var ctxWin, in, out any
		if m.ContextWindow > 0 {
			ctxWin = m.ContextWindow
		}
		if m.InputCostPerMTok > 0 || m.OutputCostPerMTok > 0 {
			in, out = m.InputCostPerMTok, m.OutputCostPerMTok
		}
		batch.Queue(`
INSERT INTO org_model_catalog
  (org_id, canonical_model, provider, provider_model_id, kind, source, context_window, input_cost_per_mtok, output_cost_per_mtok)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (org_id, provider, provider_model_id) DO UPDATE SET
  canonical_model = EXCLUDED.canonical_model,
  kind = EXCLUDED.kind,
  context_window = COALESCE(EXCLUDED.context_window, org_model_catalog.context_window),
  input_cost_per_mtok = COALESCE(EXCLUDED.input_cost_per_mtok, org_model_catalog.input_cost_per_mtok),
  output_cost_per_mtok = COALESCE(EXCLUDED.output_cost_per_mtok, org_model_catalog.output_cost_per_mtok),
  is_available = true,
  last_seen_at = NOW()`,
			orgID, Canonicalize(provider, m.ProviderModelID), provider, m.ProviderModelID, kind, source, ctxWin, in, out)
	}
	if len(models) > 0 {
		br := tx.SendBatch(ctx, batch)
		for range models {
			if _, err := br.Exec(); err != nil {
				br.Close()
				return 0, err
			}
		}
		if err := br.Close(); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE org_model_catalog SET is_available = false
		 WHERE org_id=$1 AND provider=$2 AND is_available AND NOT (provider_model_id = ANY($3))`,
		orgID, provider, ids); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO org_catalog_refreshes (org_id, provider, last_refreshed_at, last_status, last_error_class, model_count)
VALUES ($1,$2,NOW(),'ok',NULL,$3)
ON CONFLICT (org_id, provider) DO UPDATE SET last_refreshed_at=NOW(), last_status='ok', last_error_class=NULL, model_count=$3`,
		orgID, provider, len(models)); err != nil {
		return 0, err
	}
	return len(models), tx.Commit(ctx)
}

// RecordRefreshError records a failed refresh; existing catalog rows are kept.
func (r *Repository) RecordRefreshError(ctx context.Context, orgID, provider, class string) error {
	_, err := r.pool.Exec(ctx, `
INSERT INTO org_catalog_refreshes (org_id, provider, last_refreshed_at, last_status, last_error_class, model_count)
VALUES ($1,$2,NOW(),'error',NULLIF($3,''),0)
ON CONFLICT (org_id, provider) DO UPDATE SET last_refreshed_at=NOW(), last_status='error', last_error_class=NULLIF($3,'')`,
		orgID, provider, class)
	return err
}

// MarkUnavailable sets a single row unavailable until the next discovery run.
func (r *Repository) MarkUnavailable(ctx context.Context, orgID, provider, providerModelID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE org_model_catalog SET is_available=false WHERE org_id=$1 AND provider=$2 AND provider_model_id=$3`,
		orgID, provider, providerModelID)
	return err
}

const activeKeyClause = ` AND EXISTS (SELECT 1 FROM provider_keys k WHERE k.org_id=c.org_id AND k.provider=c.provider AND k.status='active')`

// Lookup returns available rows for a canonical model and kind, restricted to
// providers with at least one active key, ordered by provider then model id.
func (r *Repository) Lookup(ctx context.Context, orgID, canonical, kind string) ([]Row, error) {
	return r.query(ctx, `
SELECT canonical_model, provider, provider_model_id, kind, source, is_available, last_seen_at
FROM org_model_catalog c
WHERE org_id=$1 AND canonical_model=$2 AND is_available AND kind=$3`+activeKeyClause+`
ORDER BY provider, provider_model_id`, orgID, canonical, kind)
}

// List returns catalog rows ordered by canonical model. availableOnly restricts
// to available rows on providers with an active key. q filters canonical names
// by case-insensitive substring.
func (r *Repository) List(ctx context.Context, orgID, q string, availableOnly bool) ([]Row, error) {
	sql := `
SELECT canonical_model, provider, provider_model_id, kind, source, is_available, last_seen_at
FROM org_model_catalog c
WHERE org_id=$1 AND ($2='' OR position(lower($2) in canonical_model) > 0)`
	if availableOnly {
		sql += ` AND is_available` + activeKeyClause
	}
	sql += ` ORDER BY canonical_model, provider, provider_model_id`
	return r.query(ctx, sql, orgID, q)
}

func (r *Repository) query(ctx context.Context, sql string, args ...any) ([]Row, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var x Row
		if err := rows.Scan(&x.Canonical, &x.Provider, &x.ProviderModelID, &x.Kind, &x.Source, &x.Available, &x.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Refreshes lists the org's refresh records ordered by provider.
func (r *Repository) Refreshes(ctx context.Context, orgID string) ([]Refresh, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT provider, last_refreshed_at, last_status, COALESCE(last_error_class,''), model_count
		 FROM org_catalog_refreshes WHERE org_id=$1 ORDER BY provider`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Refresh
	for rows.Next() {
		var x Refresh
		if err := rows.Scan(&x.Provider, &x.LastRefreshedAt, &x.LastStatus, &x.LastErrorClass, &x.ModelCount); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// OrgProviders returns the org's providers that have an active key (alphabetical).
func (r *Repository) OrgProviders(ctx context.Context, orgID string) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT provider FROM provider_keys WHERE org_id=$1 AND status='active' ORDER BY provider`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AllOrgProviders returns every (org, provider) pair with an active key. If
// neverRefreshedOnly is set, only pairs without an org_catalog_refreshes row.
func (r *Repository) AllOrgProviders(ctx context.Context, neverRefreshedOnly bool) ([]OrgProvider, error) {
	sql := `SELECT DISTINCT k.org_id::text, k.provider FROM provider_keys k
	        WHERE k.org_id IS NOT NULL AND k.status='active'`
	if neverRefreshedOnly {
		sql += ` AND NOT EXISTS (SELECT 1 FROM org_catalog_refreshes f WHERE f.org_id=k.org_id AND f.provider=k.provider)`
	}
	rows, err := r.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgProvider
	for rows.Next() {
		var x OrgProvider
		if err := rows.Scan(&x.OrgID, &x.Provider); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// LastRefreshed returns the last refresh time, or zero time if never.
func (r *Repository) LastRefreshed(ctx context.Context, orgID, provider string) (time.Time, error) {
	var t time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT last_refreshed_at FROM org_catalog_refreshes WHERE org_id=$1 AND provider=$2`, orgID, provider).Scan(&t)
	if err == pgx.ErrNoRows {
		return time.Time{}, nil
	}
	return t, err
}
