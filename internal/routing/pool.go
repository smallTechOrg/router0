package routing

import (
	"context"
	"fmt"
	"math/rand"
	"strings"

	"github.com/redis/go-redis/v9"
)

const (
	activePoolKey = "pool:%s:active"
	commonPoolKey = "pool:%s:common"
)

type Pool struct {
	redis     *redis.Client
	scorer    ScoreFunc
	rateLimiter *RateLimiter
}

func NewPool(r *redis.Client) *Pool {
	return &Pool{
		redis:       r,
		scorer:      &WeightedScoreFunc{},
		rateLimiter: NewRateLimiter(r),
	}
}

// AddCombo adds a combo to the user's common pool with default score.
func (p *Pool) AddCombo(ctx context.Context, userID, provider, model, keyID string) error {
	key := fmt.Sprintf(commonPoolKey, userID)
	member := fmt.Sprintf("%s:%s:%s", provider, model, keyID)
	return p.redis.ZAdd(ctx, key, redis.Z{Score: 0.5, Member: member}).Err()
}

// RemoveCombo removes a combo from both pools.
func (p *Pool) RemoveCombo(ctx context.Context, userID, provider, model, keyID string) error {
	member := fmt.Sprintf("%s:%s:%s", provider, model, keyID)
	p.redis.ZRem(ctx, fmt.Sprintf(activePoolKey, userID), member)
	p.redis.ZRem(ctx, fmt.Sprintf(commonPoolKey, userID), member)
	return nil
}

// LoadPool loads active and common pool combos for a user.
func (p *Pool) LoadPool(ctx context.Context, userID string) (active, common []Combo, err error) {
	active, err = p.loadFromKey(ctx, userID, fmt.Sprintf(activePoolKey, userID), "active")
	if err != nil {
		return
	}
	common, err = p.loadFromKey(ctx, userID, fmt.Sprintf(commonPoolKey, userID), "common")
	return
}

func (p *Pool) loadFromKey(ctx context.Context, userID, redisKey, pool string) ([]Combo, error) {
	members, err := p.redis.ZRevRangeWithScores(ctx, redisKey, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	combos := make([]Combo, 0, len(members))
	for _, m := range members {
		parts := strings.SplitN(m.Member.(string), ":", 3)
		if len(parts) != 3 {
			continue
		}
		combos = append(combos, Combo{
			Provider:      parts[0],
			Model:         parts[1],
			ProviderKeyID: parts[2],
			Pool:          pool,
		})
	}
	return combos, nil
}

// SelectCombo picks a combo using the 80/20 active/common split.
func (p *Pool) SelectCombo(ctx context.Context, userID string) ([]ScoredCombo, error) {
	active, common, err := p.LoadPool(ctx, userID)
	if err != nil {
		return nil, err
	}

	// If active pool empty, fall back to common
	var candidates []Combo
	var poolLabel string
	if len(active) == 0 && len(common) == 0 {
		return nil, fmt.Errorf("no combos in pool for user %s", userID)
	}
	useActive := rand.Float64() < 0.80
	if useActive && len(active) > 0 {
		candidates = active
		poolLabel = "active"
	} else if len(common) > 0 {
		candidates = common
		poolLabel = "common"
	} else {
		candidates = active
		poolLabel = "active"
	}
	_ = poolLabel

	// Build metrics (basic — in Phase 2 we read from Redis metrics hash)
	metrics := buildBasicMetrics(candidates)

	// Filter exhausted
	filtered := make([]Combo, 0, len(candidates))
	for _, c := range candidates {
		if !p.rateLimiter.IsExhausted(ctx, userID, c.Provider, c.Model) {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) == 0 {
		filtered = candidates // fall back to all if all exhausted
	}

	scored := p.scorer.Score(ctx, userID, filtered, metrics)
	return scored, nil
}

// UpdateScore updates the Redis sorted-set score for a combo.
func (p *Pool) UpdateScore(ctx context.Context, userID, provider, model, keyID, pool string, score float64) error {
	var redisKey string
	if pool == "active" {
		redisKey = fmt.Sprintf(activePoolKey, userID)
	} else {
		redisKey = fmt.Sprintf(commonPoolKey, userID)
	}
	member := fmt.Sprintf("%s:%s:%s", provider, model, keyID)
	return p.redis.ZAdd(ctx, redisKey, redis.Z{Score: score, Member: member}).Err()
}

// buildBasicMetrics builds minimal metrics for scoring (Phase 1 basic implementation).
func buildBasicMetrics(combos []Combo) map[string]ComboMetrics {
	m := make(map[string]ComboMetrics, len(combos))
	for _, c := range combos {
		m[comboKey(c)] = ComboMetrics{
			ErrorRate:    0,
			P95LatencyMs: 500,
			CostPerToken: costPerToken(c.Provider, c.Model),
			QuotaRatio:   1.0,
		}
	}
	return m
}

func costPerToken(provider, model string) float64 {
	costs := map[string]float64{
		"google:gemini-1.5-flash":        0.075 / 1e6,
		"google:gemini-1.5-pro":          3.50 / 1e6,
		"groq:llama-3.1-70b-versatile":   0.59 / 1e6,
		"groq:mixtral-8x7b-32768":        0.24 / 1e6,
		"nvidia:meta/llama-3.1-70b-instruct": 0.35 / 1e6,
		"nvidia:meta/llama-3.1-8b-instruct":  0.10 / 1e6,
		"together:meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo": 0.88 / 1e6,
		"together:mistralai/Mixtral-8x7B-Instruct-v0.1":          0.60 / 1e6,
	}
	if v, ok := costs[provider+":"+model]; ok {
		return v
	}
	return 0.50 / 1e6
}
