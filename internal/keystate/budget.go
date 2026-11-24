package keystate

import (
	"context"
	"sync"
	"time"
)

// fetchSQL returns every active state row for the keys plus one synthetic
// budget_exhausted row per key whose month-to-date spend (provider_key_spend,
// current UTC month) has reached its monthly_budget_usd. The synthetic row lasts
// until 00:00 UTC on the 1st of next month, when a new spend row starts at 0.
// Keeping it one statement means a check is still a single query.
const fetchSQL = `
SELECT provider_key_id::text, kind, scope_model, until_at
  FROM provider_key_state
 WHERE provider_key_id = ANY($1::uuid[]) AND until_at > NOW()
UNION ALL
SELECT k.id::text, 'budget_exhausted', '',
       (date_trunc('month', NOW() AT TIME ZONE 'UTC') + INTERVAL '1 month') AT TIME ZONE 'UTC'
  FROM provider_keys k
  LEFT JOIN provider_key_spend sp
    ON sp.provider_key_id = k.id AND sp.month = (date_trunc('month', NOW() AT TIME ZONE 'UTC'))::date
 WHERE k.id = ANY($1::uuid[])
   AND k.monthly_budget_usd IS NOT NULL
   AND COALESCE(sp.spend_micro_usd, 0) >= round(k.monthly_budget_usd * 1000000)`

// MonthStart is the first instant of t's UTC month (the provider_key_spend.month key).
func MonthStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// MicroUSD converts a USD amount to integer micro-USD (rounded).
func MicroUSD(usd float64) int64 {
	if usd >= 0 {
		return int64(usd*1e6 + 0.5)
	}
	return -int64(-usd*1e6 + 0.5)
}

// spendCache is the 5 s month-spend read cache (data.md › In-Memory State).
type spendCache struct {
	mu sync.Mutex
	m  map[string]spendEntry
}

type spendEntry struct {
	month time.Time
	micro int64
	at    time.Time
}

const spendCacheTTL = 5 * time.Second

func (c *spendCache) get(keyID string, month, now time.Time) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[keyID]
	if !ok || !e.month.Equal(month) || now.Sub(e.at) > spendCacheTTL {
		return 0, false
	}
	return e.micro, true
}

func (c *spendCache) set(keyID string, month time.Time, micro int64, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]spendEntry{}
	}
	if len(c.m) > 10000 {
		c.m = map[string]spendEntry{}
	}
	c.m[keyID] = spendEntry{month: month, micro: micro, at: now}
}

func (c *spendCache) drop(keyID string) {
	c.mu.Lock()
	delete(c.m, keyID)
	c.mu.Unlock()
}

// AddSpend adds microUSD to the key's current-month spend (upsert) and drops the
// cached state so the next check re-evaluates the budget.
func (s *PGStore) AddSpend(ctx context.Context, keyID string, microUSD int64) error {
	if keyID == "" || microUSD <= 0 {
		return nil
	}
	_, err := s.db.Exec(ctx, AddSpendSQL, keyID, MonthStart(s.now()), microUSD)
	s.SpendChanged(keyID)
	return err
}

// AddSpendSQL is the spend upsert; $1 key id, $2 month (date), $3 micro-USD.
// The metadata recorder queues the same statement in its request-log batch.
const AddSpendSQL = `INSERT INTO provider_key_spend (provider_key_id, month, spend_micro_usd)
 VALUES ($1::uuid, $2::date, $3)
 ON CONFLICT (provider_key_id, month) DO UPDATE
   SET spend_micro_usd = provider_key_spend.spend_micro_usd + EXCLUDED.spend_micro_usd, updated_at = NOW()`

// SpendChanged evicts the cached state and spend of a key after its spend was
// written elsewhere (the metadata recorder calls it after each spend upsert).
func (s *PGStore) SpendChanged(keyID string) {
	s.spend.drop(keyID)
	s.Invalidate(keyID)
}

// MonthSpend returns the key's current-month spend in micro-USD (5 s cache).
func (s *PGStore) MonthSpend(ctx context.Context, keyID string) (int64, error) {
	now := s.now()
	month := MonthStart(now)
	if v, ok := s.spend.get(keyID, month, now); ok {
		return v, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT spend_micro_usd FROM provider_key_spend WHERE provider_key_id = $1::uuid AND month = $2::date`, keyID, month)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var v int64
	if rows.Next() {
		if err := rows.Scan(&v); err != nil {
			return 0, err
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	s.spend.set(keyID, month, v, now)
	return v, nil
}
