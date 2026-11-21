package catalog

import (
	"context"
	"sync"
	"time"
)

const (
	missDebounceTTL    = 60 * time.Second
	refreshStaleAfter  = 60 * time.Second
	onDemandTotalLimit = 10 * time.Second
)

// missEntry is one debounce slot for (org, canonical). done is closed when the
// on-demand refresh that claimed the slot has finished, so concurrent requests
// for the same unknown model wait for it instead of failing early.
type missEntry struct {
	expiry time.Time
	done   chan struct{}
}

// onDemand implements request-time discovery with a per-(org, canonical)
// 60 s miss debounce (model-catalog.md, Resolution step 5).
type onDemand struct {
	repo *Repository
	disc *Discovery
	now  func() time.Time

	mu     sync.Mutex
	misses map[string]*missEntry
}

func newOnDemand(repo *Repository, disc *Discovery) *onDemand {
	return &onDemand{repo: repo, disc: disc, now: time.Now, misses: map[string]*missEntry{}}
}

// claim returns (entry, true) if the caller owns the refresh for key, or the
// existing live entry and false when the miss is debounced.
func (o *onDemand) claim(key string) (*missEntry, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	for k, e := range o.misses {
		if !e.expiry.After(now) {
			delete(o.misses, k)
		}
	}
	if e, ok := o.misses[key]; ok {
		return e, false
	}
	e := &missEntry{expiry: now.Add(missDebounceTTL), done: make(chan struct{})}
	o.misses[key] = e
	return e, true
}

// discover runs on-demand discovery for a missed (org, canonical). It returns
// true when the caller should look the model up again, false if ctx ended.
func (o *onDemand) discover(ctx context.Context, orgID, canonical string) bool {
	if o.disc == nil {
		return false
	}
	e, owner := o.claim(orgID + "|" + canonical)
	if !owner {
		// Debounced: never call a provider. Wait for an in-flight refresh (a
		// finished one returns at once), then re-check the catalog, since the
		// caller's first lookup may predate that refresh.
		select {
		case <-e.done:
			return true
		case <-ctx.Done():
			return false
		}
	}
	defer close(e.done)
	o.refreshStale(ctx, orgID)
	return true
}

// refreshStale re-lists the org's providers whose last refresh is older than
// 60 s, in parallel, capped at 10 s in total. Inserted rows get source='on_demand'.
func (o *onDemand) refreshStale(ctx context.Context, orgID string) {
	ctx, cancel := context.WithTimeout(ctx, onDemandTotalLimit)
	defer cancel()
	provs, err := o.repo.OrgProviders(ctx, orgID)
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	for _, p := range provs {
		last, err := o.repo.LastRefreshed(ctx, orgID, p)
		if err != nil || (!last.IsZero() && o.now().Sub(last) < refreshStaleAfter) {
			continue
		}
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			_, _ = o.disc.refresh(ctx, orgID, p, "on_demand")
		}(p)
	}
	wg.Wait()
}
