package keys

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrInvalidPriority: providers is not a permutation of the org's providers with keys.
	ErrInvalidPriority = errors.New("providers must be a permutation of the providers with keys")
)

// KeyRef is a key within a provider's ordered list.
type KeyRef struct {
	ID       string
	Label    string
	Position int
}

// ProviderPriority is one provider in the org's ranking with its ordered keys.
type ProviderPriority struct {
	Provider string
	Position int
	Keys     []KeyRef
}

// PriorityStore owns org_provider_priority.
type PriorityStore struct {
	pool          *pgxpool.Pool
	validProvider func(string) bool
}

// NewPriorityStore builds a store. valid returns the registered provider ids
// (e.g. providers.ValidProviders); nil disables the registry check.
func NewPriorityStore(pool *pgxpool.Pool, valid func() []string) *PriorityStore {
	s := &PriorityStore{pool: pool}
	if valid != nil {
		s.validProvider = func(p string) bool {
			for _, v := range valid() {
				if v == p {
					return true
				}
			}
			return false
		}
	}
	return s
}

// ProviderOrder returns the providers the org has non-disabled keys for: ranked
// providers first (by position), then the remainder alphabetically.
func (s *PriorityStore) ProviderOrder(ctx context.Context, orgID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT k.provider FROM (SELECT DISTINCT provider FROM provider_keys WHERE org_id=$1 AND status<>'disabled') k
		 LEFT JOIN org_provider_priority p ON p.org_id=$1 AND p.provider=k.provider
		 ORDER BY (p.position IS NULL), p.position, k.provider`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// View returns the GET /api/routing/priority body: providers with at least one
// non-disabled key, in ranked order, each with its keys by position.
func (s *PriorityStore) View(ctx context.Context, orgID string) ([]ProviderPriority, error) {
	order, err := s.ProviderOrder(ctx, orgID)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT provider, id, label, position FROM provider_keys WHERE org_id=$1 AND status<>'disabled'
		 ORDER BY position, created_at`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byProv := map[string][]KeyRef{}
	for rows.Next() {
		var p string
		var k KeyRef
		if err := rows.Scan(&p, &k.ID, &k.Label, &k.Position); err != nil {
			return nil, err
		}
		byProv[p] = append(byProv[p], k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ProviderPriority, 0, len(order))
	for i, p := range order {
		out = append(out, ProviderPriority{Provider: p, Position: i, Keys: byProv[p]})
	}
	return out, nil
}

// Replace transactionally sets the provider ranking and (optionally) per-provider
// key order. Nothing changes if any part is invalid. Errors: ErrInvalidPriority,
// ErrInvalidKeyOrder.
func (s *PriorityStore) Replace(ctx context.Context, orgID string, providers []string, keyOrder map[string][]string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockOrg(ctx, tx, orgID); err != nil {
		return err
	}

	rows, err := tx.Query(ctx,
		`SELECT DISTINCT provider FROM provider_keys WHERE org_id=$1 AND status<>'disabled'`, orgID)
	if err != nil {
		return err
	}
	current := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		current[p] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if len(providers) != len(current) {
		return ErrInvalidPriority
	}
	seen := map[string]bool{}
	for _, p := range providers {
		if !current[p] || seen[p] {
			return ErrInvalidPriority
		}
		if s.validProvider != nil && !s.validProvider(p) {
			return ErrInvalidPriority
		}
		seen[p] = true
	}
	for p := range keyOrder {
		if !current[p] {
			return ErrInvalidKeyOrder
		}
	}

	if _, err := tx.Exec(ctx, `DELETE FROM org_provider_priority WHERE org_id=$1`, orgID); err != nil {
		return err
	}
	for i, p := range providers {
		if _, err := tx.Exec(ctx,
			`INSERT INTO org_provider_priority (org_id, provider, position) VALUES ($1,$2,$3)`, orgID, p, i); err != nil {
			return err
		}
	}
	provs := make([]string, 0, len(keyOrder))
	for p := range keyOrder {
		provs = append(provs, p)
	}
	sort.Strings(provs)
	for _, p := range provs {
		if err := reorderKeysTx(ctx, tx, orgID, p, keyOrder[p]); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
