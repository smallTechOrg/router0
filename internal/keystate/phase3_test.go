package keystate

import (
	"context"
	"testing"
	"time"

	"github.com/zer0-router/zer0/internal/providers"
)

func TestBudgetBlocksAtLimitAndClears(t *testing.T) {
	pool := testPool(t)
	_, key := seedKey(t, pool)
	ctx := context.Background()
	s, _, _ := newTestStore(t, pool)

	if st, _ := s.Check(ctx, key, "m"); st.Blocked {
		t.Fatalf("no budget: want unblocked, got %+v", st)
	}
	if _, err := pool.Exec(ctx, `UPDATE provider_keys SET monthly_budget_usd = 0.001 WHERE id = $1`, key); err != nil {
		t.Fatal(err)
	}
	s.Invalidate(key)
	if err := s.AddSpend(ctx, key, 600); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Check(ctx, key, "m"); st.Blocked {
		t.Fatalf("600/1000 µUSD: want unblocked, got %+v", st)
	}
	if got, _ := s.MonthSpend(ctx, key); got != 600 {
		t.Fatalf("MonthSpend = %d, want 600", got)
	}
	if err := s.AddSpend(ctx, key, 600); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Check(ctx, key, "m")
	if !st.Blocked || st.Reason != ReasonBudgetExhausted {
		t.Fatalf("1200/1000 µUSD: want budget_exhausted, got %+v", st)
	}
	if next := MonthStart(time.Now()).AddDate(0, 1, 0); st.RetryAfter > time.Until(next)+time.Minute {
		t.Fatalf("budget block should end at the next UTC month, retry_after=%s", st.RetryAfter)
	}
	if got, _ := s.MonthSpend(ctx, key); got != 1200 {
		t.Fatalf("MonthSpend after AddSpend = %d, want 1200 (cache must be dropped)", got)
	}

	// Reset does not lift a budget; clearing the budget does.
	if err := s.Reset(ctx, key); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Check(ctx, key, "m"); st.Reason != ReasonBudgetExhausted {
		t.Fatalf("after reset: budget should still block, got %+v", st)
	}
	if _, err := pool.Exec(ctx, `UPDATE provider_keys SET monthly_budget_usd = NULL WHERE id = $1`, key); err != nil {
		t.Fatal(err)
	}
	s.Invalidate(key)
	if st, _ := s.Check(ctx, key, "m"); st.Blocked {
		t.Fatalf("budget cleared: want unblocked, got %+v", st)
	}
}

func TestSetClearCreditsRespectsReason(t *testing.T) {
	pool := testPool(t)
	_, key := seedKey(t, pool)
	ctx := context.Background()
	s, _, _ := newTestStore(t, pool)

	if err := s.SetCredits(ctx, key, "balance_poll", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Check(ctx, key, ""); st.Reason != ReasonCreditsExhausted {
		t.Fatalf("after SetCredits: %+v", st)
	}
	// A clear restricted to another reason leaves it.
	if err := s.ClearCredits(ctx, key, "something_else"); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Check(ctx, key, ""); st.Reason != ReasonCreditsExhausted {
		t.Fatalf("clear with other reason removed the row: %+v", st)
	}
	if err := s.ClearCredits(ctx, key, "balance_poll"); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Check(ctx, key, ""); st.Blocked {
		t.Fatalf("after ClearCredits: %+v", st)
	}

	// A live 402 is not cleared by a poll.
	if err := s.Mark(ctx, key, "", &providers.ProviderError{Class: providers.ErrorClassInsufficientCredit, HTTP: 402}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearCredits(ctx, key, "balance_poll"); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Check(ctx, key, ""); st.Reason != ReasonCreditsExhausted {
		t.Fatalf("poll clear removed a live-402 state: %+v", st)
	}
	// A poll that says "exhausted" while the live 402 is active only extends it:
	// the row keeps its 402 reason, so a later "ok" poll still cannot clear it.
	if err := s.SetCredits(ctx, key, "balance_poll", 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := pool.QueryRow(ctx, `SELECT reason FROM provider_key_state WHERE provider_key_id=$1 AND kind='credits_exhausted'`, key).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "provider_402" {
		t.Fatalf("poll took over a live 402 row: reason=%q", reason)
	}
	if err := s.ClearCredits(ctx, key, "balance_poll"); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Check(ctx, key, ""); st.Reason != ReasonCreditsExhausted || st.RetryAfter < 2*time.Hour {
		t.Fatalf("live 402 (extended by the poll) should remain: %+v", st)
	}
	// Reset clears everything stored.
	if err := s.Reset(ctx, key); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Check(ctx, key, ""); st.Blocked {
		t.Fatalf("after Reset: %+v", st)
	}
}

func TestMicroUSD(t *testing.T) {
	for in, want := range map[float64]int64{0: 0, 0.0006: 600, 1.2345674: 1234567, 0.0000005: 1} {
		if got := MicroUSD(in); got != want {
			t.Errorf("MicroUSD(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestSpendChangedEvictsOnlyBudgetedKeys(t *testing.T) {
	pool := testPool(t)
	_, plain := seedKey(t, pool)
	_, budgeted := seedKey(t, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE provider_keys SET monthly_budget_usd = 1 WHERE id = $1`, budgeted); err != nil {
		t.Fatal(err)
	}
	s, cdb, _ := newTestStore(t, pool)
	for _, k := range []string{plain, budgeted} {
		if _, err := s.Check(ctx, k, "m"); err != nil {
			t.Fatal(err)
		}
	}
	base := cdb.count()

	s.SpendChanged(plain)
	if _, err := s.Check(ctx, plain, "m"); err != nil {
		t.Fatal(err)
	}
	if cdb.count() != base {
		t.Fatal("a spend write evicted the cached state of a key without a budget")
	}
	s.SpendChanged(budgeted)
	if _, err := s.Check(ctx, budgeted, "m"); err != nil {
		t.Fatal(err)
	}
	if cdb.count() != base+1 {
		t.Fatal("a spend write did not evict the cached state of a key with a budget")
	}
}
