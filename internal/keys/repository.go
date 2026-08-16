package keys

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type ProviderKey struct {
	ID             string
	UserID         string
	Provider       string
	Label          string
	KeyHint        string
	Ciphertext     []byte
	Nonce          []byte
	ModelID        string
	Status         string
	LastTestedAt   *time.Time
	LastTestResult string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Create(ctx context.Context, userID, provider, label, keyHint, modelID string, ciphertext, nonce []byte) (*ProviderKey, error) {
	k := &ProviderKey{}
	err := r.pool.QueryRow(ctx,
		`INSERT INTO provider_keys (user_id, provider, label, key_hint, ciphertext, nonce, model_id, status, models)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', '{}')
		 RETURNING id, user_id, provider, label, key_hint, ciphertext, nonce, COALESCE(model_id,''), status, created_at, updated_at`,
		userID, provider, label, keyHint, ciphertext, nonce, nullStr(modelID),
	).Scan(&k.ID, &k.UserID, &k.Provider, &k.Label, &k.KeyHint, &k.Ciphertext, &k.Nonce, &k.ModelID, &k.Status, &k.CreatedAt, &k.UpdatedAt)
	return k, err
}

func (r *Repository) ListByUser(ctx context.Context, userID string) ([]*ProviderKey, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, user_id, provider, label, key_hint, ciphertext, nonce, COALESCE(model_id,''), status, last_tested_at, COALESCE(last_test_result,''), created_at, updated_at
		 FROM provider_keys WHERE user_id = $1 AND status != 'disabled' ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []*ProviderKey
	for rows.Next() {
		k := &ProviderKey{}
		if err := rows.Scan(&k.ID, &k.UserID, &k.Provider, &k.Label, &k.KeyHint, &k.Ciphertext, &k.Nonce, &k.ModelID, &k.Status, &k.LastTestedAt, &k.LastTestResult, &k.CreatedAt, &k.UpdatedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (r *Repository) GetByID(ctx context.Context, id, userID string) (*ProviderKey, error) {
	k := &ProviderKey{}
	err := r.pool.QueryRow(ctx,
		`SELECT id, user_id, provider, label, key_hint, ciphertext, nonce, COALESCE(model_id,''), status, last_tested_at, COALESCE(last_test_result,''), created_at, updated_at
		 FROM provider_keys WHERE id = $1 AND user_id = $2 AND status != 'disabled'`,
		id, userID,
	).Scan(&k.ID, &k.UserID, &k.Provider, &k.Label, &k.KeyHint, &k.Ciphertext, &k.Nonce, &k.ModelID, &k.Status, &k.LastTestedAt, &k.LastTestResult, &k.CreatedAt, &k.UpdatedAt)
	return k, err
}

func (r *Repository) GetByIDAdmin(ctx context.Context, id string) (*ProviderKey, error) {
	k := &ProviderKey{}
	err := r.pool.QueryRow(ctx,
		`SELECT id, user_id, provider, label, key_hint, ciphertext, nonce, COALESCE(model_id,''), status, last_tested_at, COALESCE(last_test_result,''), created_at, updated_at
		 FROM provider_keys WHERE id = $1`,
		id,
	).Scan(&k.ID, &k.UserID, &k.Provider, &k.Label, &k.KeyHint, &k.Ciphertext, &k.Nonce, &k.ModelID, &k.Status, &k.LastTestedAt, &k.LastTestResult, &k.CreatedAt, &k.UpdatedAt)
	return k, err
}

func (r *Repository) ListActiveByUser(ctx context.Context, userID string) ([]*ProviderKey, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, user_id, provider, label, key_hint, ciphertext, nonce, COALESCE(model_id,''), status, last_tested_at, COALESCE(last_test_result,''), created_at, updated_at
		 FROM provider_keys WHERE user_id = $1 AND status = 'active' ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ks []*ProviderKey
	for rows.Next() {
		k := &ProviderKey{}
		if err := rows.Scan(&k.ID, &k.UserID, &k.Provider, &k.Label, &k.KeyHint, &k.Ciphertext, &k.Nonce, &k.ModelID, &k.Status, &k.LastTestedAt, &k.LastTestResult, &k.CreatedAt, &k.UpdatedAt); err != nil {
			return nil, err
		}
		ks = append(ks, k)
	}
	return ks, rows.Err()
}

func (r *Repository) Disable(ctx context.Context, id, userID string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE provider_keys SET status='disabled', updated_at=NOW() WHERE id=$1 AND user_id=$2`,
		id, userID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) UpdateTestResult(ctx context.Context, id, result string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE provider_keys SET last_tested_at=NOW(), last_test_result=$1, updated_at=NOW() WHERE id=$2`,
		result, id,
	)
	return err
}

var ErrNotFound = &notFoundError{}

type notFoundError struct{}

func (e *notFoundError) Error() string { return "not found" }

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
