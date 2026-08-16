package health

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/routing"
)

const probeInterval = 10 * time.Minute

type Scheduler struct {
	prober     *Prober
	pgPool     *pgxpool.Pool
	workers    int
	mu         sync.Mutex
	stop       chan struct{}
	onProbesDone func() // called after all probes in a round complete
}

func NewScheduler(r *redis.Client, pg *pgxpool.Pool, workers int) *Scheduler {
	keyRepo := keys.NewRepository(pg)
	return &Scheduler{
		prober:  NewProber(r, keyRepo, pg),
		pgPool:  pg,
		workers: workers,
		stop:    make(chan struct{}),
	}
}

// OnProbesDone registers a callback invoked after each probe round completes.
func (s *Scheduler) OnProbesDone(fn func()) { s.onProbesDone = fn }

func (s *Scheduler) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(probeInterval)
		defer ticker.Stop()
		// Seed pool from DB so existing keys are routable immediately
		s.seedPool(ctx)
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

// seedPool ensures every active provider key has Redis pool entries so it's routable.
// Keys with known auth errors get a score of 0 so they're never selected.
func (s *Scheduler) seedPool(ctx context.Context) {
	keyRows, err := s.pgPool.Query(ctx,
		`SELECT id, user_id, provider, COALESCE(last_test_result,'') FROM provider_keys WHERE status = 'active'`,
	)
	if err != nil {
		slog.Error("health scheduler: seed pool query failed", "error", err)
		return
	}
	defer keyRows.Close()

	type keyInfo struct{ id, userID, provider, lastResult string }
	var providerKeys []keyInfo
	for keyRows.Next() {
		var k keyInfo
		if keyRows.Scan(&k.id, &k.userID, &k.provider, &k.lastResult) == nil {
			providerKeys = append(providerKeys, k)
		}
	}

	seeded := 0
	for _, k := range providerKeys {
		score := 0.5
		if k.lastResult == "error:auth_error" || k.lastResult == "error:auth" {
			score = 0.0
		}
		models := s.eligibleModelsWithMeta(ctx, k.provider)
		for _, m := range models {
			member := fmt.Sprintf("%s:%s:%s", k.provider, m.modelID, k.id)
			for _, tier := range []routing.Tier{routing.TierFlashLite, routing.TierFlash, routing.TierPro} {
				f := tier.Filter()
				if m.paramB >= f.MinParamB && float64(m.ctxWindow) >= float64(f.MinContext) {
					poolKey := fmt.Sprintf("pool:%s:%s", k.userID, tier.String())
					s.prober.redis.ZAddNX(ctx, poolKey, redis.Z{Score: score, Member: member})
				}
			}
		}
		seeded++
	}
	slog.Info("health scheduler: pool seeded", "keys", seeded)
}

type modelWithMeta struct {
	modelID   string
	paramB    float64
	ctxWindow int
}

func (s *Scheduler) eligibleModelsWithMeta(ctx context.Context, provider string) []modelWithMeta {
	rows, err := s.pgPool.Query(ctx,
		`SELECT model_id, COALESCE(parameter_count_b, 0), context_window
		 FROM provider_models
		 WHERE provider = $1 AND probe_status = 'probe_ok'
		   AND parameter_count_b > 0`, provider)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []modelWithMeta
	for rows.Next() {
		var m modelWithMeta
		if rows.Scan(&m.modelID, &m.paramB, &m.ctxWindow) == nil {
			out = append(out, m)
		}
	}
	return out
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
	var wg sync.WaitGroup
	for _, pr := range probes {
		pr := pr
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			models, err := lookupProvider(pr.provider)
			if err != nil || len(models) == 0 {
				return
			}
			// Probe one model to determine key health — if it fails auth, zero all combos for this key
			probeModel := models[0]
			probeErr := s.prober.Probe(ctx, pr.provider, probeModel, pr.keyID, pr.userID)
			if probeErr != nil {
				slog.Warn("health probe error", "provider", pr.provider, "model", probeModel, "error", probeErr)
				allModels := s.allModelsForProvider(ctx, pr.provider)
				for _, model := range allModels {
					member := fmt.Sprintf("%s:%s:%s", pr.provider, model, pr.keyID)
					for _, tier := range []routing.Tier{routing.TierFlashLite, routing.TierFlash, routing.TierPro} {
						poolKey := fmt.Sprintf("pool:%s:%s", pr.userID, tier.String())
						s.prober.redis.ZAdd(ctx, poolKey, redis.Z{Score: 0.0, Member: member})
					}
				}
			} else {
				// Probe remaining models too, but don't kill the key if secondary models fail
				for _, model := range models[1:] {
					s.prober.Probe(ctx, pr.provider, model, pr.keyID, pr.userID)
				}
			}
		}()
	}
	wg.Wait()
	if s.onProbesDone != nil {
		// Delay slightly so async observation inserts (probe goroutines) can land in DB
		go func() {
			time.Sleep(3 * time.Second)
			s.onProbesDone()
		}()
	}
}

// allModelsForProvider returns all discovered model IDs for a provider from the DB,
// falling back to the static default list if none are found.
func (s *Scheduler) allModelsForProvider(ctx context.Context, provider string) []string {
	rows, err := s.pgPool.Query(ctx,
		`SELECT model_id FROM provider_models WHERE provider = $1`, provider,
	)
	if err != nil {
		defaults, _ := lookupProvider(provider)
		return defaults
	}
	defer rows.Close()
	var models []string
	for rows.Next() {
		var m string
		if rows.Scan(&m) == nil {
			models = append(models, m)
		}
	}
	if len(models) == 0 {
		defaults, _ := lookupProvider(provider)
		return defaults
	}
	return models
}

// ProbeUntestedModels finds all models with probe_status='untested', fetches a working
// key for each provider, and runs a live completion probe to verify actual accessibility.
// Models that respond get probe_status='probe_ok'; failures become 'probe_error'.
// Called after discovery so the registry only shows verified-working models.
func (s *Scheduler) ProbeUntestedModels(ctx context.Context) {
	rows, err := s.pgPool.Query(ctx,
		`SELECT model_id, provider FROM provider_models
		 WHERE probe_status = 'untested'
		   AND parameter_count_b > 0
		 ORDER BY provider, model_id`)
	if err != nil {
		slog.Error("probe untested models: query failed", "error", err)
		return
	}
	type entry struct{ model, provider string }
	var entries []entry
	for rows.Next() {
		var e entry
		if rows.Scan(&e.model, &e.provider) == nil {
			entries = append(entries, e)
		}
	}
	rows.Close()

	if len(entries) == 0 {
		return
	}
	slog.Info("probe untested models: starting", "count", len(entries))

	// Build a map of provider -> decrypted API key (prefer non-errored keys)
	keyRows, err := s.pgPool.Query(ctx,
		`SELECT DISTINCT ON (provider) provider, ciphertext, nonce
		 FROM provider_keys
		 WHERE status = 'active'
		   AND COALESCE(last_test_result,'ok') NOT LIKE 'error:%'
		 ORDER BY provider, created_at DESC`)
	if err != nil {
		return
	}
	providerKeys := map[string]string{}
	for keyRows.Next() {
		var provider string
		var ciphertext, nonce []byte
		if err := keyRows.Scan(&provider, &ciphertext, &nonce); err != nil {
			continue
		}
		dec, err := keys.Decrypt(ciphertext, nonce)
		if err == nil {
			providerKeys[provider] = dec
		}
	}
	keyRows.Close()

	// Probe with limited concurrency to avoid rate-limiting
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for _, e := range entries {
		apiKey, ok := providerKeys[e.provider]
		if !ok {
			continue
		}
		e := e
		key := apiKey
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			s.prober.ProbeModel(ctx, e.provider, e.model, key)
			time.Sleep(200 * time.Millisecond) // gentle rate-limiting between probes
		}()
	}
	wg.Wait()
	slog.Info("probe untested models: done", "count", len(entries))
}

func lookupProvider(name string) ([]string, error) {
	models := map[string][]string{
		"groq":       {"llama-3.3-70b-versatile", "openai/gpt-oss-120b"},
		"google":     {"gemini-3.7-flash"},
		"openrouter": {"nvidia/nemotron-3-ultra-550b-a55b:free"},
		"nvidia":     {"meta/llama-3.1-70b-instruct"},
		"together":   {"meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo"},
		"baseten":    {},
	}
	return models[name], nil
}
