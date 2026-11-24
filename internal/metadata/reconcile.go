package metadata

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zer0-router/zer0/internal/keystate"
)

// Beginner is the subset of *pgxpool.Pool Reconcile needs.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// spendLockSQL / spendLockSharedSQL serialise reconciliation against live spend
// writes. Every request-log batch that adds spend takes the shared lock, and
// reconcile takes it exclusively before its statement. Its snapshot therefore
// includes every committed increment, and no increment can commit between that
// snapshot and its overwrite.
const (
	spendLockSQL       = `SELECT pg_advisory_xact_lock(hashtext('router0-spend'))`
	spendLockSharedSQL = `SELECT pg_advisory_xact_lock_shared(hashtext('router0-spend'))`
)

// reconcileSQL rewrites the month's provider_key_spend rows from the request log,
// which is authoritative: every key with successful requests in the month gets
// round(SUM(estimated_cost_usd) × 1e6), and counters of keys without any are
// reset to 0. One statement, so it sees one snapshot of request_metadata.
const reconcileSQL = `
WITH actual AS (
  SELECT rm.provider_key_id AS id, round(SUM(rm.estimated_cost_usd) * 1000000)::bigint AS micro
    FROM request_metadata rm
    JOIN provider_keys k ON k.id = rm.provider_key_id
   WHERE rm.status = 'success' AND rm.provider_key_id IS NOT NULL
     AND rm.created_at >= $1 AND rm.created_at < $2
     AND ($4 = '' OR k.org_id::text = $4)
   GROUP BY rm.provider_key_id
), zeroed AS (
  UPDATE provider_key_spend sp SET spend_micro_usd = 0, updated_at = NOW()
   WHERE sp.month = $3::date AND sp.spend_micro_usd <> 0
     AND NOT EXISTS (SELECT 1 FROM actual a WHERE a.id = sp.provider_key_id)
     AND ($4 = '' OR sp.provider_key_id IN (SELECT id FROM provider_keys WHERE org_id::text = $4))
)
INSERT INTO provider_key_spend (provider_key_id, month, spend_micro_usd)
SELECT id, $3::date, micro FROM actual
ON CONFLICT (provider_key_id, month) DO UPDATE
  SET spend_micro_usd = EXCLUDED.spend_micro_usd, updated_at = NOW()
  WHERE provider_key_spend.spend_micro_usd <> EXCLUDED.spend_micro_usd`

// Reconcile rebuilds the spend counters of the UTC month containing now from
// request_metadata and returns how many key rows it inserted or corrected
// (zeroed rows are not counted). orgID limits it to one org ("" = all orgs;
// tests use the filter so parallel packages do not reset each other's keys).
func Reconcile(ctx context.Context, db Beginner, now time.Time, orgID string) (int64, error) {
	start := keystate.MonthStart(now)
	return reconcileTx(ctx, db, start, orgID)
}

func reconcileTx(ctx context.Context, db Beginner, start time.Time, orgID string) (int64, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(ctx, spendLockSQL); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, reconcileSQL, start, start.AddDate(0, 1, 0), start.Format("2006-01-02"), orgID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
