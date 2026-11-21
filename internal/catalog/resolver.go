package catalog

import (
	"context"
	"sort"
	"strings"
)

// Resolver maps a requested model name to the org's reachable provider models.
type Resolver interface {
	Resolve(ctx context.Context, orgID, requested string) ([]Entry, error) // nil, nil = not found
}

// CatalogResolver implements Resolver on top of the repository: org alias
// step, kind-filtered lookup, and debounced on-demand discovery on a miss.
type CatalogResolver struct {
	repo *Repository
	od   *onDemand
}

func NewResolver(repo *Repository, disc *Discovery) *CatalogResolver {
	return &CatalogResolver{repo: repo, od: newOnDemand(repo, disc)}
}

// Resolve returns one Entry per provider (chat kind) for the requested name.
// Entry.Canonical is the canonical model (the alias target for an alias).
func (r *CatalogResolver) Resolve(ctx context.Context, orgID, requested string) ([]Entry, error) {
	return r.ResolveKind(ctx, orgID, requested, KindChat)
}

// ResolveKind is Resolve for an explicit kind (inference uses "chat").
func (r *CatalogResolver) ResolveKind(ctx context.Context, orgID, requested, kind string) ([]Entry, error) {
	exact := strings.ToLower(strings.TrimSpace(requested))
	canonical := ""
	if target, err := r.repo.AliasTarget(ctx, orgID, exact); err != nil {
		return nil, err
	} else if target != "" {
		canonical, exact = target, "" // one level, no chains; exact id match does not apply
	} else {
		canonical = Canonicalize("", requested)
	}

	entries, err := r.lookup(ctx, orgID, canonical, exact, kind)
	if err != nil || len(entries) > 0 {
		return entries, err
	}
	// A model that exists only as another kind is a clear not-found; never
	// re-list for it (and never resurrect it as chat).
	if other, err := r.repo.HasOtherKind(ctx, orgID, canonical, kind); err != nil || other {
		return nil, err
	}
	if !r.od.discover(ctx, orgID, canonical) {
		return nil, nil
	}
	return r.lookup(ctx, orgID, canonical, exact, kind)
}

func (r *CatalogResolver) lookup(ctx context.Context, orgID, canonical, exact, kind string) ([]Entry, error) {
	rows, err := r.repo.Lookup(ctx, orgID, canonical, kind)
	if err != nil {
		return nil, err
	}
	best := map[string]Row{}
	for _, row := range rows {
		cur, ok := best[row.Provider]
		if !ok || better(row, cur, exact) {
			best[row.Provider] = row
		}
	}
	var out []Entry
	for _, row := range best {
		out = append(out, Entry{Canonical: row.Canonical, Provider: row.Provider, ProviderModelID: row.ProviderModelID, Kind: row.Kind})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out, nil
}

// better reports whether a beats b: exact match on requested, then shortest, then lexicographic.
func better(a, b Row, exact string) bool {
	ae, be := strings.ToLower(a.ProviderModelID) == exact, strings.ToLower(b.ProviderModelID) == exact
	if ae != be {
		return ae
	}
	if len(a.ProviderModelID) != len(b.ProviderModelID) {
		return len(a.ProviderModelID) < len(b.ProviderModelID)
	}
	return a.ProviderModelID < b.ProviderModelID
}
