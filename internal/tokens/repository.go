// Package tokens implements org service tokens (r0-sk-…): issuing, listing,
// revoking and verifying them.
package tokens

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zer0-router/zer0/internal/auth"
)

var (
	// ErrInvalid: unknown or malformed token (same value as auth.ErrInvalidToken).
	ErrInvalid = auth.ErrInvalidToken
	// ErrRevoked: token existed but was revoked (same value as auth.ErrRevokedToken).
	ErrRevoked = auth.ErrRevokedToken

	ErrNameExists  = errors.New("token name already in use")
	ErrInvalidName = errors.New("invalid token name")
	ErrNotFound    = errors.New("token not found")
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ValidName reports whether name is 1-64 chars of [A-Za-z0-9._-].
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Token is a service token as listed to the dashboard. The secret is never included.
type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"` // scheme + 12 chars, e.g. r0-sk-7Hk2pQ9xLm3a
	CreatedAt  time.Time  `json:"created_at"`
	CreatedBy  *string    `json:"created_by"` // creator's email
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	Legacy     bool       `json:"legacy"`
}

// Record is what the verifier needs from storage.
type Record struct {
	ID, OrgID, Name string
	Revoked         bool
	Hash            string // only for legacy lookups: the bcrypt hash
}

// Store is the persistence the Verifier depends on (faked in tests).
type Store interface {
	// FindByHash looks up a sha256 token by hex hash. ErrInvalid if no row.
	FindByHash(ctx context.Context, hash string) (Record, error)
	// LegacyByPrefix returns bcrypt-hashed rows with the 12-char prefix.
	LegacyByPrefix(ctx context.Context, prefix string) ([]Record, error)
	// TouchLastUsed sets last_used_at = now for the token.
	TouchLastUsed(ctx context.Context, tokenID string) error
}

type Repository struct {
	pool *pgxpool.Pool

	mu       sync.RWMutex
	onRevoke []func(tokenID string)
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// OnRevoke registers a callback invoked after a token is revoked (used by the
// Verifier to evict its cache). NewVerifier registers itself automatically.
func (r *Repository) OnRevoke(fn func(tokenID string)) {
	r.mu.Lock()
	r.onRevoke = append(r.onRevoke, fn)
	r.mu.Unlock()
}

// Create issues a new token for orgID. The full token is returned once; only its
// sha256 and 12-char prefix are stored. ErrNameExists if a live token has the name.
func (r *Repository) Create(ctx context.Context, orgID, createdBy, name string) (tok *Token, fullToken string, err error) {
	if !ValidName(name) {
		return nil, "", ErrInvalidName
	}
	fullToken, prefix, err := auth.GenerateServiceToken()
	if err != nil {
		return nil, "", err
	}
	tok = &Token{Name: name, Prefix: auth.ServiceTokenScheme + prefix}
	err = r.pool.QueryRow(ctx,
		`INSERT INTO service_tokens (org_id, name, token_prefix, token_hash, hash_alg, created_by)
		 VALUES ($1, $2, $3, $4, 'sha256', NULLIF($5, '')::uuid)
		 RETURNING id::text, created_at`,
		orgID, name, prefix, auth.HashToken(fullToken), createdBy,
	).Scan(&tok.ID, &tok.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, "", ErrNameExists
		}
		return nil, "", err
	}
	return tok, fullToken, nil
}

// List returns the org's tokens, live first (newest first), then revoked.
func (r *Repository) List(ctx context.Context, orgID string) ([]Token, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT t.id::text, t.name, t.token_prefix, t.hash_alg, t.created_at, u.email, t.last_used_at, t.revoked_at
		   FROM service_tokens t LEFT JOIN users u ON u.id = t.created_by
		  WHERE t.org_id = $1
		  ORDER BY (t.revoked_at IS NOT NULL), t.created_at DESC, t.id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		var t Token
		var prefix, alg string
		if err := rows.Scan(&t.ID, &t.Name, &prefix, &alg, &t.CreatedAt, &t.CreatedBy, &t.LastUsedAt, &t.RevokedAt); err != nil {
			return nil, err
		}
		t.Legacy = alg == "bcrypt"
		if t.Legacy {
			t.Prefix = auth.LegacyTokenScheme + prefix
		} else {
			t.Prefix = auth.ServiceTokenScheme + prefix
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Revoke revokes a token of orgID. Idempotent for already-revoked tokens.
// ErrNotFound if the id does not exist in this org (including malformed ids).
func (r *Repository) Revoke(ctx context.Context, orgID, tokenID, revokedBy string) error {
	if !uuidRe.MatchString(tokenID) {
		return ErrNotFound
	}
	var id string
	err := r.pool.QueryRow(ctx,
		`UPDATE service_tokens
		    SET revoked_at = COALESCE(revoked_at, NOW()),
		        revoked_by = CASE WHEN revoked_at IS NULL THEN NULLIF($3, '')::uuid ELSE revoked_by END
		  WHERE id = $1 AND org_id = $2
		RETURNING id::text`, tokenID, orgID, revokedBy).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	r.mu.RLock()
	hooks := append([]func(string){}, r.onRevoke...)
	r.mu.RUnlock()
	for _, fn := range hooks {
		fn(id)
	}
	return nil
}

// FindByHash implements Store.
func (r *Repository) FindByHash(ctx context.Context, hash string) (Record, error) {
	var rec Record
	err := r.pool.QueryRow(ctx,
		`SELECT id::text, org_id::text, name, revoked_at IS NOT NULL
		   FROM service_tokens WHERE token_hash = $1 AND hash_alg = 'sha256'`, hash,
	).Scan(&rec.ID, &rec.OrgID, &rec.Name, &rec.Revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrInvalid
	}
	return rec, err
}

// LegacyByPrefix implements Store.
func (r *Repository) LegacyByPrefix(ctx context.Context, prefix string) ([]Record, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id::text, org_id::text, name, revoked_at IS NOT NULL, token_hash
		   FROM service_tokens WHERE hash_alg = 'bcrypt' AND token_prefix = $1`, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var rec Record
		if err := rows.Scan(&rec.ID, &rec.OrgID, &rec.Name, &rec.Revoked, &rec.Hash); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// TouchLastUsed implements Store.
func (r *Repository) TouchLastUsed(ctx context.Context, tokenID string) error {
	_, err := r.pool.Exec(ctx, `UPDATE service_tokens SET last_used_at = NOW() WHERE id = $1`, tokenID)
	return err
}
