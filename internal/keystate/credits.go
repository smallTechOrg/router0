package keystate

import (
	"context"
	"time"
)

// SetCredits upserts a whole-key credits_exhausted row lasting ttl with the
// given reason (the balance poller uses reason "balance_poll"). Unlike Mark it
// replaces until_at, so a poll can both extend and shorten its own row.
func (s *PGStore) SetCredits(ctx context.Context, keyID, reason string, ttl time.Duration) error {
	if keyID == "" || ttl <= 0 {
		return nil
	}
	now := s.now()
	until := now.Add(ttl)
	s.gen.Add(1)
	s.applyLocal(keyID, row{kind: ReasonCreditsExhausted, until: until}, now)
	_, err := s.db.Exec(ctx,
		`INSERT INTO provider_key_state (provider_key_id, org_id, kind, scope_model, until_at, reason, last_error_class)
		 SELECT id, org_id, 'credits_exhausted', '', $2, $3, 'insufficient_credits' FROM provider_keys WHERE id = $1
		 ON CONFLICT (provider_key_id, kind, scope_model) DO UPDATE SET
		   -- Our own (or an expired) row is replaced outright. A live row set by
		   -- something else (a provider 402) keeps its reason, so a later
		   -- ClearCredits(reason) cannot remove it; the poll may only extend it.
		   until_at = CASE WHEN provider_key_state.reason IS NOT DISTINCT FROM EXCLUDED.reason
		                     OR provider_key_state.until_at <= NOW()
		                   THEN EXCLUDED.until_at
		                   ELSE GREATEST(provider_key_state.until_at, EXCLUDED.until_at) END,
		   reason = CASE WHEN provider_key_state.until_at > NOW()
		                   AND provider_key_state.reason IS DISTINCT FROM EXCLUDED.reason
		                 THEN provider_key_state.reason ELSE EXCLUDED.reason END,
		   updated_at = NOW()`,
		keyID, until, reason)
	s.Invalidate(keyID)
	return err
}

// ClearCredits deletes the key's credits_exhausted row. When onlyReason is not
// empty the row is deleted only if it was set with that reason, so a poll never
// clears a state set by a live 402.
func (s *PGStore) ClearCredits(ctx context.Context, keyID, onlyReason string) error {
	if keyID == "" {
		return nil
	}
	_, err := s.db.Exec(ctx,
		`DELETE FROM provider_key_state
		  WHERE provider_key_id = $1 AND kind = 'credits_exhausted' AND scope_model = ''
		    AND ($2 = '' OR reason = $2)`, keyID, onlyReason)
	s.Invalidate(keyID)
	return err
}

// Reset deletes every provider_key_state row of the key (cooldowns and credits)
// and evicts its cached state. A budget is not a stored state: a key over
// budget stays blocked until the budget is raised or cleared.
func (s *PGStore) Reset(ctx context.Context, keyID string) error {
	if keyID == "" {
		return nil
	}
	_, err := s.db.Exec(ctx, `DELETE FROM provider_key_state WHERE provider_key_id = $1`, keyID)
	s.Invalidate(keyID)
	return err
}
