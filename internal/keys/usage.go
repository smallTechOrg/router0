package keys

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Balance is the last polled provider balance of a key (provider_key_balances).
type Balance struct {
	Status       string // ok | exhausted | error
	RemainingUSD *float64
	LimitUSD     *float64
	UsageUSD     *float64
	PolledAt     time.Time
}

// Usage is a key's month-to-date spend and last polled balance.
type Usage struct {
	MonthSpendMicroUSD int64
	Balance            *Balance // nil = never polled
}

// LoadUsage reads current-UTC-month spend and balances for the keys in one query.
func LoadUsage(ctx context.Context, pool *pgxpool.Pool, keyIDs []string) (map[string]Usage, error) {
	out := make(map[string]Usage, len(keyIDs))
	if len(keyIDs) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT k.id::text, COALESCE(sp.spend_micro_usd, 0),
		       b.status, b.remaining_usd::float8, b.limit_usd::float8, b.usage_usd::float8, b.polled_at
		  FROM provider_keys k
		  LEFT JOIN provider_key_spend sp
		    ON sp.provider_key_id = k.id AND sp.month = (date_trunc('month', NOW() AT TIME ZONE 'UTC'))::date
		  LEFT JOIN provider_key_balances b ON b.provider_key_id = k.id
		 WHERE k.id = ANY($1::uuid[])`, keyIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var u Usage
		var status *string
		var b Balance
		var polled *time.Time
		if err := rows.Scan(&id, &u.MonthSpendMicroUSD, &status, &b.RemainingUSD, &b.LimitUSD, &b.UsageUSD, &polled); err != nil {
			return nil, err
		}
		if status != nil {
			b.Status = *status
			if polled != nil {
				b.PolledAt = *polled
			}
			u.Balance = &b
		}
		out[id] = u
	}
	return out, rows.Err()
}
