package catalog

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Alias errors returned by Repository.CreateAlias / DeleteAlias.
var (
	ErrAliasExists   = errors.New("catalog: alias already exists")
	ErrAliasShadows  = errors.New("catalog: alias equals an existing model name")
	ErrAliasInvalid  = errors.New("catalog: invalid alias or target")
	ErrAliasNotFound = errors.New("catalog: alias not found")
)

// Alias is a row of org_model_aliases.
type Alias struct {
	Alias          string
	CanonicalModel string
	CreatedAt      time.Time
}

var aliasNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,63}$`)

// NormalizeAlias lowercases and trims an alias name.
func NormalizeAlias(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ListAliases returns the org's aliases ordered by alias.
func (r *Repository) ListAliases(ctx context.Context, orgID string) ([]Alias, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT alias, canonical_model, created_at FROM org_model_aliases WHERE org_id=$1 ORDER BY alias`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alias{}
	for rows.Next() {
		var a Alias
		if err := rows.Scan(&a.Alias, &a.CanonicalModel, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AliasTarget returns the canonical target of an alias (already normalised), or "" if none.
func (r *Repository) AliasTarget(ctx context.Context, orgID, alias string) (string, error) {
	var t string
	err := r.pool.QueryRow(ctx,
		`SELECT canonical_model FROM org_model_aliases WHERE org_id=$1 AND alias=$2`, orgID, alias).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return t, err
}

// CreateAlias validates and stores alias -> canonical(target). The alias is
// lowercased; the target is canonicalised so provider-specific ids work. The
// target need not exist yet. Errors: ErrAliasInvalid (bad names, self/chain
// target), ErrAliasShadows (alias equals a chat model's canonical name or
// provider id in the org catalog), ErrAliasExists. createdBy may be "".
func (r *Repository) CreateAlias(ctx context.Context, orgID, alias, target, createdBy string) (*Alias, error) {
	alias = NormalizeAlias(alias)
	if !aliasNameRe.MatchString(alias) || strings.TrimSpace(target) == "" {
		return nil, ErrAliasInvalid
	}
	canonical := Canonicalize("", target)
	if canonical == alias {
		return nil, ErrAliasInvalid // alias of itself
	}
	// Chains are not resolved (one level): refuse a target that is itself an alias.
	if t, err := r.AliasTarget(ctx, orgID, canonical); err != nil {
		return nil, err
	} else if t != "" {
		return nil, ErrAliasInvalid
	}
	var shadows bool
	if err := r.pool.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM org_model_catalog
  WHERE org_id=$1 AND kind='chat'
    AND (canonical_model=$2 OR canonical_model=$3 OR lower(provider_model_id)=$2))`,
		orgID, alias, Canonicalize("", alias)).Scan(&shadows); err != nil {
		return nil, err
	}
	if shadows {
		return nil, ErrAliasShadows
	}
	var cb any
	if createdBy != "" {
		cb = createdBy
	}
	a := &Alias{Alias: alias, CanonicalModel: canonical}
	err := r.pool.QueryRow(ctx, `
INSERT INTO org_model_aliases (org_id, alias, canonical_model, created_by) VALUES ($1,$2,$3,$4)
RETURNING created_at`, orgID, alias, canonical, cb).Scan(&a.CreatedAt)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return nil, ErrAliasExists
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// DeleteAlias removes an alias; ErrAliasNotFound if absent.
func (r *Repository) DeleteAlias(ctx context.Context, orgID, alias string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM org_model_aliases WHERE org_id=$1 AND alias=$2`, orgID, NormalizeAlias(alias))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAliasNotFound
	}
	return nil
}
