package catalog

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"
)

// Scheduler periodically refreshes every (org, provider) pair with an active key.
type Scheduler struct {
	repo     *Repository
	disc     Discoverer
	Interval time.Duration
}

// IntervalFromEnv reads ROUTER0_CATALOG_REFRESH_HOURS (default 6).
func IntervalFromEnv() time.Duration {
	if h, err := strconv.Atoi(os.Getenv("ROUTER0_CATALOG_REFRESH_HOURS")); err == nil && h > 0 {
		return time.Duration(h) * time.Hour
	}
	return 6 * time.Hour
}

func NewScheduler(repo *Repository, disc Discoverer, interval time.Duration) *Scheduler {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	return &Scheduler{repo: repo, disc: disc, Interval: interval}
}

// Start runs the loop in a goroutine until ctx is cancelled: first a pass over
// pairs never refreshed (server startup), then every Interval over all pairs.
func (s *Scheduler) Start(ctx context.Context) {
	go func() {
		// Refresh every (org, provider) pair at startup, not only never-refreshed
		// ones, so an upgrade that changes classification rules (e.g. model kinds)
		// re-classifies stored rows immediately instead of after one interval.
		s.runPass(ctx, false)
		t := time.NewTicker(s.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.runPass(ctx, false)
			}
		}
	}()
}

func (s *Scheduler) runPass(ctx context.Context, neverOnly bool) {
	pairs, err := s.repo.AllOrgProviders(ctx, neverOnly)
	if err != nil {
		slog.Warn("catalog: scheduler list pairs failed", "err", err)
		return
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, p := range pairs {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(p OrgProvider) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := s.disc.RefreshProvider(ctx, p.OrgID, p.Provider); err != nil && err != ErrInProgress {
				slog.Warn("catalog: scheduled refresh failed", "org_id", p.OrgID, "provider", p.Provider)
			}
		}(p)
	}
	wg.Wait()
}
