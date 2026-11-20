package catalog

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Resolver maps a requested model name to the org's reachable provider models.
type Resolver interface {
	Resolve(ctx context.Context, orgID, requested string) ([]Entry, error) // nil, nil = not found
}

const (
	missDebounceTTL    = 60 * time.Second
	refreshStaleAfter  = 60 * time.Second
	onDemandTotalLimit = 10 * time.Second
)

// CatalogResolver implements Resolver on top of the repository, with on-demand
// discovery (debounced) on a miss.
type CatalogResolver struct {
	repo *Repository
	disc *Discovery

	mu     sync.Mutex
	misses map[string]time.Time // "org|canonical" -> expiry
	now    func() time.Time
}

func NewResolver(repo *Repository, disc *Discovery) *CatalogResolver {
	return &CatalogResolver{repo: repo, disc: disc, misses: map[string]time.Time{}, now: time.Now}
}

// Resolve returns one Entry per provider (chat kind) for the requested name.
func (r *CatalogResolver) Resolve(ctx context.Context, orgID, requested string) ([]Entry, error) {
	return r.ResolveKind(ctx, orgID, requested, "chat")
}

// ResolveKind is Resolve for an explicit endpoint kind ("chat" | "embedding").
func (r *CatalogResolver) ResolveKind(ctx context.Context, orgID, requested, kind string) ([]Entry, error) {
	canonical := Canonicalize("", requested)
	exact := strings.ToLower(strings.TrimSpace(requested))

	entries, err := r.lookup(ctx, orgID, canonical, exact, kind)
	if err != nil || len(entries) > 0 {
		return entries, err
	}

	if !r.claimMiss(orgID + "|" + canonical) {
		return nil, nil // debounced
	}
	r.onDemandRefresh(ctx, orgID)
	entries, err = r.lookup(ctx, orgID, canonical, exact, kind)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	return entries, nil
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

// claimMiss returns true if this caller may trigger on-demand discovery.
func (r *CatalogResolver) claimMiss(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for k, exp := range r.misses {
		if !exp.After(now) {
			delete(r.misses, k)
		}
	}
	if _, ok := r.misses[key]; ok {
		return false
	}
	r.misses[key] = now.Add(missDebounceTTL)
	return true
}

// onDemandRefresh re-lists the org's providers whose last refresh is stale, in parallel.
func (r *CatalogResolver) onDemandRefresh(ctx context.Context, orgID string) {
	ctx, cancel := context.WithTimeout(ctx, onDemandTotalLimit)
	defer cancel()
	provs, err := r.repo.OrgProviders(ctx, orgID)
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	for _, p := range provs {
		last, err := r.repo.LastRefreshed(ctx, orgID, p)
		if err != nil || (!last.IsZero() && r.now().Sub(last) < refreshStaleAfter) {
			continue
		}
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			_, _ = r.disc.refresh(ctx, orgID, p, "on_demand")
		}(p)
	}
	wg.Wait()
}
