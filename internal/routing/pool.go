package routing

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Pool key format: pool:{userID}:{tier}
func tierPoolKey(userID string, tier Tier) string {
	return fmt.Sprintf("pool:%s:%s", userID, tier.String())
}

type Pool struct {
	redis       *redis.Client
	scorer      ScoreFunc
	rateLimiter *RateLimiter
}

func NewPool(r *redis.Client) *Pool {
	return &Pool{
		redis:       r,
		scorer:      &WeightedScoreFunc{},
		rateLimiter: NewRateLimiter(r),
	}
}

func (p *Pool) Redis() *redis.Client { return p.redis }

// AddCombo adds a combo to the appropriate tier pool with default score.
func (p *Pool) AddCombo(ctx context.Context, userID, provider, model, keyID string, tier Tier) error {
	key := tierPoolKey(userID, tier)
	member := fmt.Sprintf("%s:%s:%s", provider, model, keyID)
	return p.redis.ZAddNX(ctx, key, redis.Z{Score: 0.5, Member: member}).Err()
}

// AddComboToAll adds a combo to all tier pools it qualifies for.
func (p *Pool) AddComboToAll(ctx context.Context, userID, provider, model, keyID string, paramB float64, ctxWindow int) error {
	member := fmt.Sprintf("%s:%s:%s", provider, model, keyID)
	for _, tier := range []Tier{TierFlashLite, TierFlash, TierPro} {
		f := tier.Filter()
		if paramB >= f.MinParamB && float64(ctxWindow) >= float64(f.MinContext) {
			key := tierPoolKey(userID, tier)
			p.redis.ZAddNX(ctx, key, redis.Z{Score: 0.5, Member: member})
		}
	}
	return nil
}

// RemoveCombo removes a combo from all tier pools.
func (p *Pool) RemoveCombo(ctx context.Context, userID, provider, model, keyID string) error {
	member := fmt.Sprintf("%s:%s:%s", provider, model, keyID)
	for _, tier := range []Tier{TierFlashLite, TierFlash, TierPro} {
		p.redis.ZRem(ctx, tierPoolKey(userID, tier), member)
	}
	return nil
}

// RemoveModelFromPools removes ALL combos for a provider:model (any keyID) from a user's pools.
func (p *Pool) RemoveModelFromPools(ctx context.Context, userID, provider, model string) {
	prefix := fmt.Sprintf("%s:%s:", provider, model)
	for _, tier := range []Tier{TierFlashLite, TierFlash, TierPro} {
		key := tierPoolKey(userID, tier)
		members, err := p.redis.ZRange(ctx, key, 0, -1).Result()
		if err != nil {
			continue
		}
		for _, m := range members {
			if strings.HasPrefix(m, prefix) {
				p.redis.ZRem(ctx, key, m)
			}
		}
	}
}

// LoadPool loads combos for a specific tier.
func (p *Pool) LoadPool(ctx context.Context, userID string, tier Tier) ([]Combo, error) {
	return p.loadFromKey(ctx, tierPoolKey(userID, tier), tier.String())
}

func (p *Pool) loadFromKey(ctx context.Context, redisKey, pool string) ([]Combo, error) {
	members, err := p.redis.ZRevRangeWithScores(ctx, redisKey, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	combos := make([]Combo, 0, len(members))
	for _, m := range members {
		if m.Score == 0 {
			continue
		}
		provider, model, keyID, ok := parseMember(m.Member.(string))
		if !ok {
			continue
		}
		combos = append(combos, Combo{
			Provider:      provider,
			Model:         model,
			ProviderKeyID: keyID,
			Pool:          pool,
		})
	}
	return combos, nil
}

const sessionComboTTL = 10 * time.Minute

func sessionComboKey(userID, conversationID string) string {
	return fmt.Sprintf("session:%s:%s:combo", userID, conversationID)
}

// SelectComboForSession pins a conversation to one combo for its lifetime.
func (p *Pool) SelectComboForSession(ctx context.Context, userID, conversationID string, tier Tier) ([]ScoredCombo, error) {
	if conversationID != "" {
		key := sessionComboKey(userID, conversationID)
		pinned, err := p.redis.Get(ctx, key).Result()
		if err == nil && pinned != "" {
			p.redis.Expire(ctx, key, sessionComboTTL)
			parts := strings.SplitN(pinned, ":", 4) // provider:model:keyID:pool
			if len(parts) == 4 {
				combo := Combo{Provider: parts[0], Model: parts[1], ProviderKeyID: parts[2], Pool: parts[3]}
				return []ScoredCombo{{Combo: combo, Score: 1.0}}, nil
			}
		}
	}

	scored, err := p.SelectCombo(ctx, userID, tier)
	if err != nil {
		return nil, err
	}

	if conversationID != "" && len(scored) > 0 {
		top := scored[0]
		pinned := fmt.Sprintf("%s:%s:%s:%s", top.Provider, top.Model, top.ProviderKeyID, top.Pool)
		key := sessionComboKey(userID, conversationID)
		p.redis.Set(ctx, key, pinned, sessionComboTTL)
	}

	return scored, nil
}

// SelectCombo picks combos from the tier pool.
func (p *Pool) SelectCombo(ctx context.Context, userID string, tier Tier) ([]ScoredCombo, error) {
	key := tierPoolKey(userID, tier)
	members, err := p.redis.ZRevRangeWithScores(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("no combos in %s pool for user %s", tier.String(), userID)
	}

	// Build combos with their Redis scores (health signal)
	type candidate struct {
		combo Combo
		health float64 // Redis score: 0 = dead, 0.5 = untested, higher = healthy
	}
	var alive []candidate
	var dead []candidate
	for _, m := range members {
		provider, model, keyID, ok := parseMember(m.Member.(string))
		if !ok {
			continue
		}
		c := candidate{
			combo: Combo{Provider: provider, Model: model, ProviderKeyID: keyID, Pool: tier.String()},
			health: m.Score,
		}
		if m.Score == 0 {
			dead = append(dead, c)
			continue
		}
		alive = append(alive, c)
	}
	if len(alive) == 0 {
		// All dead — return them anyway so the caller can attempt and get a proper error
		for _, d := range dead {
			alive = append(alive, d)
		}
	}

	// Read per-combo usage counts for this tier (round-robin tracking)
	rrKey := fmt.Sprintf("rr:%s:%s", userID, tier.String())
	usageCounts, _ := p.redis.HGetAll(ctx, rrKey).Result()

	// Fair share: total requests / number of alive combos
	totalUsage := int64(0)
	for _, v := range usageCounts {
		n, _ := fmt.Sscanf(v, "%d", new(int64))
		if n > 0 {
			var val int64
			fmt.Sscanf(v, "%d", &val)
			totalUsage += val
		}
	}
	fairShare := float64(totalUsage+1) / float64(len(alive))

	// Score each combo: lower usage ratio = higher priority
	// Also factor in exhaustion status and health
	type scoredCandidate struct {
		combo    Combo
		priority float64
		exhausted bool
	}
	scored := make([]scoredCandidate, 0, len(alive))
	for _, c := range alive {
		memberStr := fmt.Sprintf("%s:%s:%s", c.combo.Provider, c.combo.Model, c.combo.ProviderKeyID)
		var usage int64
		if v, ok := usageCounts[memberStr]; ok {
			fmt.Sscanf(v, "%d", &usage)
		}

		exhausted := p.rateLimiter.IsExhausted(ctx, userID, c.combo.Provider, c.combo.Model)

		// Priority: inverse of usage ratio, boosted by health
		usageRatio := float64(usage+1) / (fairShare + 1)
		priority := (1.0 / (usageRatio + 0.1)) * c.health

		// Small jitter (±5%) to break ties
		jitter := 0.95 + rand.Float64()*0.10
		priority *= jitter

		scored = append(scored, scoredCandidate{combo: c.combo, priority: priority, exhausted: exhausted})
	}

	// Sort: non-exhausted first, then by priority descending
	for i := 0; i < len(scored); i++ {
		for j := i + 1; j < len(scored); j++ {
			swap := false
			if scored[i].exhausted && !scored[j].exhausted {
				swap = true
			} else if scored[i].exhausted == scored[j].exhausted && scored[j].priority > scored[i].priority {
				swap = true
			}
			if swap {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	result := make([]ScoredCombo, 0, len(scored))
	for _, s := range scored {
		result = append(result, ScoredCombo{Combo: s.combo, Score: s.priority})
	}
	return result, nil
}

// UpdateScore updates the Redis sorted-set score for a combo in a specific tier pool.
func (p *Pool) UpdateScore(ctx context.Context, userID, provider, model, keyID string, tier Tier, score float64) error {
	member := fmt.Sprintf("%s:%s:%s", provider, model, keyID)
	return p.redis.ZAdd(ctx, tierPoolKey(userID, tier), redis.Z{Score: score, Member: member}).Err()
}

// IncrementUsage increments the round-robin usage counter for a combo in a tier.
// The counters expire after 1 hour so usage naturally rebalances.
func (p *Pool) IncrementUsage(ctx context.Context, userID string, tier Tier, provider, model, keyID string) {
	rrKey := fmt.Sprintf("rr:%s:%s", userID, tier.String())
	member := fmt.Sprintf("%s:%s:%s", provider, model, keyID)
	p.redis.HIncrBy(ctx, rrKey, member, 1)
	p.redis.Expire(ctx, rrKey, time.Hour)
}

// parseMember splits a Redis sorted-set member "provider:model:keyID" where
// model may contain colons (e.g. "openrouter:nvidia/foo:free:uuid").
// KeyID is always the last colon-delimited segment; provider is the first.
func parseMember(member string) (provider, model, keyID string, ok bool) {
	lastColon := strings.LastIndex(member, ":")
	if lastColon <= 0 {
		return "", "", "", false
	}
	keyID = member[lastColon+1:]
	rest := member[:lastColon]
	firstColon := strings.Index(rest, ":")
	if firstColon <= 0 {
		return "", "", "", false
	}
	provider = rest[:firstColon]
	model = rest[firstColon+1:]
	if model == "" || keyID == "" {
		return "", "", "", false
	}
	return provider, model, keyID, true
}
