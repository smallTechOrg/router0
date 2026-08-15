package health

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/zer0-router/zer0/internal/keys"
)

const probeInterval = 10 * time.Minute

type Scheduler struct {
	prober  *Prober
	pgPool  *pgxpool.Pool
	workers int
	mu      sync.Mutex
	stop    chan struct{}
}

func NewScheduler(r *redis.Client, pg *pgxpool.Pool, workers int) *Scheduler {
	keyRepo := keys.NewRepository(pg)
	return &Scheduler{
		prober:  NewProber(r, keyRepo),
		pgPool:  pg,
		workers: workers,
		stop:    make(chan struct{}),
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(probeInterval)
		defer ticker.Stop()
		// Run once at startup
		s.runProbes(ctx)
		for {
			select {
			case <-ticker.C:
				s.runProbes(ctx)
			case <-s.stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *Scheduler) Stop() {
	close(s.stop)
}

func (s *Scheduler) runProbes(ctx context.Context) {
	// Load all active keys from DB
	rows, err := s.pgPool.Query(ctx,
		`SELECT pk.id, pk.user_id, pk.provider FROM provider_keys pk WHERE pk.status = 'active'`,
	)
	if err != nil {
		slog.Error("health scheduler: query failed", "error", err)
		return
	}
	defer rows.Close()

	type probe struct {
		keyID    string
		userID   string
		provider string
	}
	var probes []probe
	for rows.Next() {
		var p probe
		if err := rows.Scan(&p.keyID, &p.userID, &p.provider); err != nil {
			continue
		}
		probes = append(probes, p)
	}

	sem := make(chan struct{}, s.workers)
	for _, pr := range probes {
		pr := pr
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			prov, err := lookupProvider(pr.provider)
			if err != nil || len(prov) == 0 {
				return
			}
			for _, model := range prov {
				if err := s.prober.Probe(ctx, pr.provider, model, pr.keyID, pr.userID); err != nil {
					slog.Warn("health probe error", "provider", pr.provider, "model", model, "error", err)
				}
			}
		}()
	}
}

func lookupProvider(name string) ([]string, error) {
	models := map[string][]string{
		"groq":       {"llama-3.1-70b-versatile"},
		"google":     {"gemini-1.5-flash"},
		"openrouter": {"openai/gpt-4o-mini"},
		"nvidia":     {"meta/llama-3.1-8b-instruct"},
		"together":   {"meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo"},
		"baseten":    {},
	}
	return models[name], nil
}
