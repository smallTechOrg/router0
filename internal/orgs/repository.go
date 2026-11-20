// Package orgs holds org (tenant) and membership persistence.
package orgs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zer0-router/zer0/internal/auth"
)

var (
	// ErrNoOrg is returned when a user has no org_members row (same value as auth.ErrNoOrg).
	ErrNoOrg = auth.ErrNoOrg
	// ErrUserDisabled is returned when the user's status is 'disabled' (same value as auth.ErrUserDisabled).
	ErrUserDisabled = auth.ErrUserDisabled
)

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Org is an org as seen by one of its members.
type Org struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// Summary is one row of the platform-admin org listing.
type Summary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
	MemberCount int       `json:"member_count"`
	TokenCount  int       `json:"token_count"` // live (non-revoked) service tokens
	KeyCount    int       `json:"key_count"`   // active provider keys
}

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// OrgForUser returns the user's org id and org role.
// ErrNoOrg if the user has no membership; ErrUserDisabled if the user is disabled.
func (r *Repository) OrgForUser(ctx context.Context, userID string) (orgID, role string, err error) {
	var oid, rl *string
	var status string
	err = r.pool.QueryRow(ctx,
		`SELECT m.org_id::text, m.role, u.status
		   FROM users u LEFT JOIN org_members m ON m.user_id = u.id
		  WHERE u.id = $1`, userID).Scan(&oid, &rl, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNoOrg
	}
	if err != nil {
		return "", "", err
	}
	if status == "disabled" {
		return "", "", ErrUserDisabled
	}
	if oid == nil || rl == nil {
		return "", "", ErrNoOrg
	}
	return *oid, *rl, nil
}

// GetForUser returns the user's org (id, name) and org role.
func (r *Repository) GetForUser(ctx context.Context, userID string) (*Org, error) {
	var o Org
	err := r.pool.QueryRow(ctx,
		`SELECT o.id::text, o.name, m.role
		   FROM org_members m JOIN orgs o ON o.id = m.org_id
		  WHERE m.user_id = $1`, userID).Scan(&o.ID, &o.Name, &o.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoOrg
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// Exists reports whether an org with the id exists.
func (r *Repository) Exists(ctx context.Context, orgID string) (bool, error) {
	var ok bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM orgs WHERE id = $1)`, orgID).Scan(&ok)
	return ok, err
}

// CreateWithOwner inserts an org and makes userID its owner, inside tx.
func CreateWithOwner(ctx context.Context, tx pgx.Tx, name, userID string) (orgID string, err error) {
	if err = tx.QueryRow(ctx, `INSERT INTO orgs (name) VALUES ($1) RETURNING id::text`, name).Scan(&orgID); err != nil {
		return "", err
	}
	if err = AddMember(ctx, tx, orgID, userID, RoleOwner); err != nil {
		return "", err
	}
	return orgID, nil
}

// AddMember adds userID to orgID with role, inside tx.
func AddMember(ctx context.Context, tx pgx.Tx, orgID, userID, role string) error {
	_, err := tx.Exec(ctx, `INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, $3)`, orgID, userID, role)
	return err
}

// ListSummaries lists every org with member, live-token and active-key counts (platform admin).
func (r *Repository) ListSummaries(ctx context.Context) ([]Summary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT o.id::text, o.name, o.created_at,
		       (SELECT COUNT(*) FROM org_members m WHERE m.org_id = o.id),
		       (SELECT COUNT(*) FROM service_tokens t WHERE t.org_id = o.id AND t.revoked_at IS NULL),
		       (SELECT COUNT(*) FROM provider_keys k WHERE k.org_id = o.id AND k.status = 'active')
		  FROM orgs o ORDER BY o.created_at DESC, o.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var s Summary
		if err := rows.Scan(&s.ID, &s.Name, &s.CreatedAt, &s.MemberCount, &s.TokenCount, &s.KeyCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
