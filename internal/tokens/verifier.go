package tokens

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/cache"
)

const (
	DefaultCacheTTL     = 30 * time.Second
	DefaultCacheEntries = 10000
	touchInterval       = time.Minute
)

// Verifier authenticates raw service tokens: in-process cache (sha256 -> principal),
// then sha256 lookup, then (for zer0:sk- tokens) the legacy bcrypt path.
// It implements auth.TokenVerifier.
type Verifier struct {
	store Store
	cache *cache.TTL[string, auth.Principal]
	now   func() time.Time

	gen atomic.Uint64 // bumped on every eviction; guards cache fills racing a revoke

	mu        sync.Mutex
	byID      map[string]map[string]struct{} // token id -> cache keys
	lastTouch map[string]time.Time
}

// NewVerifier builds a Verifier. ttl<=0 uses 30 s, maxEntries<=0 uses 10 000.
// If store has an OnRevoke(func(string)) method (the *Repository does) the
// verifier registers its Evict there so revocation takes effect immediately.
func NewVerifier(store Store, ttl time.Duration, maxEntries int) *Verifier {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	if maxEntries <= 0 {
		maxEntries = DefaultCacheEntries
	}
	return newVerifier(store, cache.New[string, auth.Principal](ttl, maxEntries), time.Now)
}

// NewVerifierWithClock is NewVerifier with an injectable clock (tests).
func NewVerifierWithClock(store Store, ttl time.Duration, maxEntries int, now func() time.Time) *Verifier {
	return newVerifier(store, cache.NewWithClock[string, auth.Principal](ttl, maxEntries, now), now)
}

func newVerifier(store Store, c *cache.TTL[string, auth.Principal], now func() time.Time) *Verifier {
	v := &Verifier{
		store:     store,
		cache:     c,
		now:       now,
		byID:      map[string]map[string]struct{}{},
		lastTouch: map[string]time.Time{},
	}
	if h, ok := store.(interface{ OnRevoke(func(string)) }); ok {
		h.OnRevoke(v.Evict)
	}
	return v
}

// Verify implements auth.TokenVerifier.
func (v *Verifier) Verify(ctx context.Context, raw string) (auth.Principal, error) {
	scheme, body, ok := auth.ParseServiceToken(raw)
	if !ok {
		return auth.Principal{}, ErrInvalid
	}
	key := auth.HashToken(raw)
	if p, hit := v.cache.Get(key); hit {
		v.touch(p.TokenID)
		return p, nil
	}
	gen := v.gen.Load()

	var rec Record
	var err error
	if scheme == auth.ServiceTokenScheme {
		rec, err = v.store.FindByHash(ctx, key)
		if err != nil {
			return auth.Principal{}, err
		}
	} else {
		rec, err = v.verifyLegacy(ctx, raw, body[:auth.TokenPrefixLen])
		if err != nil {
			return auth.Principal{}, err
		}
	}
	if rec.Revoked {
		return auth.Principal{}, ErrRevoked
	}
	p := auth.Principal{OrgID: rec.OrgID, TokenID: rec.ID, TokenName: rec.Name}
	v.fill(key, p, gen)
	v.touch(p.TokenID)
	return p, nil
}

func (v *Verifier) verifyLegacy(ctx context.Context, raw, prefix string) (Record, error) {
	recs, err := v.store.LegacyByPrefix(ctx, prefix)
	if err != nil {
		return Record{}, err
	}
	for _, rec := range recs {
		if bcrypt.CompareHashAndPassword([]byte(rec.Hash), []byte(raw)) == nil {
			return rec, nil
		}
	}
	return Record{}, ErrInvalid
}

// fill caches p unless an eviction happened since the lookup started.
func (v *Verifier) fill(key string, p auth.Principal, gen uint64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.gen.Load() != gen {
		return
	}
	v.cache.Set(key, p)
	set := v.byID[p.TokenID]
	if set == nil {
		set = map[string]struct{}{}
		v.byID[p.TokenID] = set
	}
	set[key] = struct{}{}
}

// Evict removes every cache entry for the token id (called on revoke).
func (v *Verifier) Evict(tokenID string) {
	v.mu.Lock()
	v.gen.Add(1)
	for key := range v.byID[tokenID] {
		v.cache.Delete(key)
	}
	delete(v.byID, tokenID)
	delete(v.lastTouch, tokenID)
	v.mu.Unlock()
}

// touch updates last_used_at asynchronously, at most once per minute per token.
func (v *Verifier) touch(tokenID string) {
	now := v.now()
	v.mu.Lock()
	if last, ok := v.lastTouch[tokenID]; ok && now.Sub(last) < touchInterval {
		v.mu.Unlock()
		return
	}
	v.lastTouch[tokenID] = now
	v.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = v.store.TouchLastUsed(ctx, tokenID)
	}()
}
