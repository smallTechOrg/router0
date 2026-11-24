package keys

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProviderKey is an org-owned provider API key. UserID means "created by".
type ProviderKey struct {
	ID             string
	OrgID          string
	UserID         string
	Provider       string
	Label          string
	KeyHint        string
	Ciphertext     []byte
	Nonce          []byte
	ModelID        string
	Status         string // active | error | disabled
	Position       int    // 0 = first within (org, provider)
	LastTestedAt   *time.Time
	LastTestResult string
	// MonthlyBudgetUSD is the optional per-key month budget (nil = no budget).
	MonthlyBudgetUSD *float64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

var (
	// ErrNotFound is returned when a key does not exist in the caller's org.
	ErrNotFound = errors.New("key not found")
	// ErrDuplicateLabel is returned when (org, provider, label) already exists.
	ErrDuplicateLabel = errors.New("key label already exists for provider")
	// ErrInvalidKeyOrder is returned when a reorder is not a permutation of the provider's keys.
	ErrInvalidKeyOrder = errors.New("key order must be a permutation of the provider's keys")
)

const keyCols = `id, org_id, user_id, provider, label, key_hint, ciphertext, nonce,
	COALESCE(model_id,''), status, position, last_tested_at, COALESCE(last_test_result,''),
	monthly_budget_usd::float8, created_at, updated_at`

func scanKey(row pgx.Row) (*ProviderKey, error) {
	k := &ProviderKey{}
	err := row.Scan(&k.ID, &k.OrgID, &k.UserID, &k.Provider, &k.Label, &k.KeyHint, &k.Ciphertext, &k.Nonce,
		&k.ModelID, &k.Status, &k.Position, &k.LastTestedAt, &k.LastTestResult, &k.MonthlyBudgetUSD, &k.CreatedAt, &k.UpdatedAt)
	return k, err
}

func collect(rows pgx.Rows) ([]*ProviderKey, error) {
	defer rows.Close()
	var out []*ProviderKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Repository is the org-scoped store for provider_keys.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// lockOrg serialises key/priority position writes for one org for the life of tx.
func lockOrg(ctx context.Context, tx pgx.Tx, orgID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('router0-keys:' || $1))`, orgID)
	return err
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// Create inserts a key at the end of the provider's key order and appends the
// provider to the org's priority if it is new. An empty label defaults to
// "<provider>-<n>". Plaintext never reaches this layer (ciphertext/nonce only).
func (r *Repository) Create(ctx context.Context, orgID, userID, provider, label, keyHint, modelID string, ciphertext, nonce []byte) (*ProviderKey, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockOrg(ctx, tx, orgID); err != nil {
		return nil, err
	}

	if label == "" {
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM provider_keys WHERE org_id=$1 AND provider=$2 AND status<>'disabled'`,
			orgID, provider).Scan(&n); err != nil {
			return nil, err
		}
		for i := n + 1; ; i++ {
			cand := fmt.Sprintf("%s-%d", provider, i)
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM provider_keys WHERE org_id=$1 AND provider=$2 AND label=$3)`,
				orgID, provider, cand).Scan(&exists); err != nil {
				return nil, err
			}
			if !exists {
				label = cand
				break
			}
		}
	}

	k, err := scanKey(tx.QueryRow(ctx,
		`INSERT INTO provider_keys (org_id, user_id, provider, label, key_hint, ciphertext, nonce, model_id, status, models, position)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'active','{}',
		   (SELECT COALESCE(MAX(position)+1, 0) FROM provider_keys WHERE org_id=$1 AND provider=$3 AND status<>'disabled'))
		 RETURNING `+keyCols,
		orgID, userID, provider, label, keyHint, ciphertext, nonce, nullStr(modelID)))
	if err != nil {
		if isUnique(err) {
			return nil, ErrDuplicateLabel
		}
		return nil, err
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO org_provider_priority (org_id, provider, position)
		 SELECT $1, $2, COALESCE((SELECT MAX(position)+1 FROM org_provider_priority WHERE org_id=$1), 0)
		 WHERE NOT EXISTS (SELECT 1 FROM org_provider_priority WHERE org_id=$1 AND provider=$2)`,
		orgID, provider); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return k, nil
}

// ListByOrg returns the org's non-disabled keys ordered by provider then position.
func (r *Repository) ListByOrg(ctx context.Context, orgID string) ([]*ProviderKey, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+keyCols+` FROM provider_keys WHERE org_id=$1 AND status<>'disabled'
		 ORDER BY provider, position, created_at`, orgID)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

// GetByID returns a non-disabled key owned by the org, or ErrNotFound.
func (r *Repository) GetByID(ctx context.Context, id, orgID string) (*ProviderKey, error) {
	k, err := scanKey(r.pool.QueryRow(ctx,
		`SELECT `+keyCols+` FROM provider_keys WHERE id=$1 AND org_id=$2 AND status<>'disabled'`, id, orgID))
	if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
		return nil, ErrNotFound
	}
	return k, err
}

func isInvalidUUID(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "22P02"
}

// ActiveByOrgProvider returns status='active' keys ordered by position (routing contract).
func (r *Repository) ActiveByOrgProvider(ctx context.Context, orgID, provider string) ([]*ProviderKey, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+keyCols+` FROM provider_keys WHERE org_id=$1 AND provider=$2 AND status='active'
		 ORDER BY position, created_at`, orgID, provider)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

// Disable soft-deletes a key. The label is suffixed so it can be reused (the
// legacy (user_id, provider, label) unique constraint still covers disabled rows).
func (r *Repository) Disable(ctx context.Context, id, orgID string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE provider_keys SET status='disabled', label = label || '~deleted-' || left(id::text, 8), updated_at=NOW()
		 WHERE id=$1 AND org_id=$2 AND status<>'disabled'`, id, orgID)
	if err != nil {
		if isInvalidUUID(err) {
			return ErrNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// KeyUpdate is a partial update of a key (PATCH /api/keys/{id}). Nil fields
// are left unchanged; SetBudget with a nil Budget clears the budget.
type KeyUpdate struct {
	Label     *string
	SetBudget bool
	Budget    *float64
}

// Update applies a partial update and returns the updated key.
func (r *Repository) Update(ctx context.Context, id, orgID string, u KeyUpdate) (*ProviderKey, error) {
	k, err := scanKey(r.pool.QueryRow(ctx,
		`UPDATE provider_keys SET
		   label = COALESCE($3, label),
		   monthly_budget_usd = CASE WHEN $4 THEN $5::numeric ELSE monthly_budget_usd END,
		   updated_at = NOW()
		 WHERE id=$1 AND org_id=$2 AND status<>'disabled'
		 RETURNING `+keyCols, id, orgID, u.Label, u.SetBudget, u.Budget))
	switch {
	case errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err):
		return nil, ErrNotFound
	case isUnique(err):
		return nil, ErrDuplicateLabel
	}
	return k, err
}

// MarkAuthError takes a key out of plans after an auth failure (status='error').
// It is a no-op for keys that are not currently active.
func (r *Repository) MarkAuthError(ctx context.Context, keyID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE provider_keys SET status='error', last_test_result='auth_error', updated_at=NOW()
		 WHERE id=$1 AND status='active'`, keyID)
	return err
}

// RecordTest stores a test outcome. A successful test re-enables a key in 'error'.
// A failed test with an auth class marks the key 'error'.
func (r *Repository) RecordTest(ctx context.Context, id, orgID, result string, ok, authFailure bool) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE provider_keys SET last_tested_at=NOW(), last_test_result=$3, updated_at=NOW(),
		   status = CASE WHEN $4 AND status='error' THEN 'active'
		                 WHEN $5 AND status='active' THEN 'error' ELSE status END
		 WHERE id=$1 AND org_id=$2 AND status<>'disabled'`, id, orgID, result, ok, authFailure)
	return err
}

// ReorderKeys sets the key order for (org, provider); ids must be a permutation
// of the provider's non-disabled key ids.
func (r *Repository) ReorderKeys(ctx context.Context, orgID, provider string, ids []string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockOrg(ctx, tx, orgID); err != nil {
		return err
	}
	if err := reorderKeysTx(ctx, tx, orgID, provider, ids); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MoveKey moves one key to newPos (0-based, clamped) within its provider.
func (r *Repository) MoveKey(ctx context.Context, orgID, keyID string, newPos int) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockOrg(ctx, tx, orgID); err != nil {
		return err
	}
	var provider string
	if err := tx.QueryRow(ctx,
		`SELECT provider FROM provider_keys WHERE id=$1 AND org_id=$2 AND status<>'disabled'`, keyID, orgID).Scan(&provider); err != nil {
		if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
			return ErrNotFound
		}
		return err
	}
	rows, err := tx.Query(ctx,
		`SELECT id FROM provider_keys WHERE org_id=$1 AND provider=$2 AND status<>'disabled' AND id<>$3
		 ORDER BY position, created_at`, orgID, provider, keyID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if newPos < 0 {
		newPos = 0
	}
	if newPos > len(ids) {
		newPos = len(ids)
	}
	ids = append(ids[:newPos], append([]string{keyID}, ids[newPos:]...)...)
	if err := reorderKeysTx(ctx, tx, orgID, provider, ids); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func reorderKeysTx(ctx context.Context, tx pgx.Tx, orgID, provider string, ids []string) error {
	rows, err := tx.Query(ctx,
		`SELECT id FROM provider_keys WHERE org_id=$1 AND provider=$2 AND status<>'disabled'`, orgID, provider)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		have[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) != len(have) {
		return ErrInvalidKeyOrder
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !have[id] || seen[id] {
			return ErrInvalidKeyOrder
		}
		seen[id] = true
	}
	for i, id := range ids {
		if _, err := tx.Exec(ctx,
			`UPDATE provider_keys SET position=$1, updated_at=NOW() WHERE id=$2 AND org_id=$3`, i, id, orgID); err != nil {
			return err
		}
	}
	return nil
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
