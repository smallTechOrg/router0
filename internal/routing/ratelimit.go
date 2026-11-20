package routing

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type RateLimiter struct {
	redis *redis.Client
}

func NewRateLimiter(r *redis.Client) *RateLimiter {
	return &RateLimiter{redis: r}
}

type Window string

const (
	WindowMinute Window = "minute"
	WindowHour   Window = "hour"
	WindowDay    Window = "day"
)

func rlKey(userID, provider, model string, w Window) string {
	return fmt.Sprintf("rl:%s:%s:%s:%s", userID, provider, model, w)
}

func rlRemKey(userID, provider, model string, w Window) string {
	return fmt.Sprintf("rl:%s:%s:%s:rem:%s", userID, provider, model, w)
}

func windowTTL(w Window) time.Duration {
	now := time.Now().UTC()
	switch w {
	case WindowMinute:
		next := now.Truncate(time.Minute).Add(time.Minute)
		return next.Sub(now)
	case WindowHour:
		next := now.Truncate(time.Hour).Add(time.Hour)
		return next.Sub(now)
	case WindowDay:
		nextDay := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
		return nextDay.Sub(now)
	}
	return time.Minute
}

// Increment increments all three rate-limit windows and returns the counts.
func (rl *RateLimiter) Increment(ctx context.Context, userID, provider, model string) error {
	pipe := rl.redis.Pipeline()
	for _, w := range []Window{WindowMinute, WindowHour, WindowDay} {
		key := rlKey(userID, provider, model, w)
		pipe.Incr(ctx, key)
		pipe.Expire(ctx, key, windowTTL(w))
	}
	_, err := pipe.Exec(ctx)
	return err
}

// GetCounts returns the current usage counts for all windows.
func (rl *RateLimiter) GetCounts(ctx context.Context, userID, provider, model string) map[Window]int64 {
	out := map[Window]int64{}
	for _, w := range []Window{WindowMinute, WindowHour, WindowDay} {
		v, err := rl.redis.Get(ctx, rlKey(userID, provider, model, w)).Int64()
		if err == nil {
			out[w] = v
		}
	}
	return out
}

// SetRemaining stores provider-reported remaining quota.
func (rl *RateLimiter) SetRemaining(ctx context.Context, userID, provider, model string, w Window, remaining int64) error {
	key := rlRemKey(userID, provider, model, w)
	return rl.redis.Set(ctx, key, remaining, windowTTL(w)).Err()
}

// GetRemaining returns provider-reported remaining quota (or -1 if unknown).
func (rl *RateLimiter) GetRemaining(ctx context.Context, userID, provider, model string, w Window) int64 {
	v, err := rl.redis.Get(ctx, rlRemKey(userID, provider, model, w)).Int64()
	if err != nil {
		return -1
	}
	return v
}

// IsExhausted checks if the daily quota is known to be exhausted.
func (rl *RateLimiter) IsExhausted(ctx context.Context, userID, provider, model string) bool {
	rem := rl.GetRemaining(ctx, userID, provider, model, WindowDay)
	return rem == 0
}

// SetBroadThrottle sets a provider-level throttle flag.
func (rl *RateLimiter) SetBroadThrottle(ctx context.Context, provider string) error {
	return rl.redis.Set(ctx, fmt.Sprintf("throttle:%s", provider), "1", 5*time.Minute).Err()
}

// IsBroadThrottled checks if a provider has a broad throttle.
func (rl *RateLimiter) IsBroadThrottled(ctx context.Context, provider string) bool {
	v, err := rl.redis.Get(ctx, fmt.Sprintf("throttle:%s", provider)).Result()
	return err == nil && v == "1"
}
