package keys

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// KeyState is the live cooldown/credit state of a key (from provider_key_state).
type KeyState struct {
	Blocked    bool
	Reason     string // rate_limited | credits_exhausted | transient_cooldown | budget_exhausted
	RetryAfter time.Duration
	Models     []string // affected provider_model_ids (rate_limited only)
}

// StateReader reads active (until_at > now) key state for a set of keys.
// Keys with no active state are absent from the map.
type StateReader interface {
	States(ctx context.Context, keyIDs []string) (map[string]KeyState, error)
}

// PGStateReader reads provider_key_state directly (authoritative; no cache).
type PGStateReader struct{ pool *pgxpool.Pool }

func NewPGStateReader(pool *pgxpool.Pool) *PGStateReader { return &PGStateReader{pool: pool} }

var statePrecedence = map[string]int{
	"credits_exhausted": 0, "budget_exhausted": 1, "rate_limited": 2, "transient_cooldown": 3,
}

func (r *PGStateReader) States(ctx context.Context, keyIDs []string) (map[string]KeyState, error) {
	out := map[string]KeyState{}
	if len(keyIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT provider_key_id::text, kind, scope_model, GREATEST(EXTRACT(EPOCH FROM (until_at - NOW())), 0)
		 FROM provider_key_state WHERE provider_key_id = ANY($1::uuid[]) AND until_at > NOW()`, keyIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind, scope string
		var secs float64
		if err := rows.Scan(&id, &kind, &scope, &secs); err != nil {
			return nil, err
		}
		ra := time.Duration(secs * float64(time.Second))
		cur, ok := out[id]
		switch {
		case !ok || statePrecedence[kind] < statePrecedence[cur.Reason]:
			cur = KeyState{Blocked: true, Reason: kind, RetryAfter: ra}
			if kind == "rate_limited" && scope != "" {
				cur.Models = []string{scope}
			}
		case kind == cur.Reason:
			if ra > cur.RetryAfter {
				cur.RetryAfter = ra
			}
			if kind == "rate_limited" && scope != "" {
				cur.Models = append(cur.Models, scope)
			}
		}
		out[id] = cur
	}
	for id, st := range out {
		sort.Strings(st.Models)
		out[id] = st
	}
	return out, rows.Err()
}
