package tokens

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// fakeBudgetDB answers the guard's query with a fixed budget/spend and counts
// queries; during can run code while a query is "in flight".
type fakeBudgetDB struct {
	budget  *float64
	spend   int64
	queries int
	during  func()
}

// fakeRow holds the values as of query start (the statement's snapshot).
type fakeRow struct {
	budget *float64
	spend  int64
}

func (r fakeRow) Scan(dest ...any) error {
	*(dest[0].(**float64)) = r.budget
	*(dest[1].(*int64)) = r.spend
	return nil
}

func (f *fakeBudgetDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	f.queries++
	row := fakeRow{budget: f.budget, spend: f.spend}
	if f.during != nil {
		f.during()
	}
	return row
}

func TestBudgetGuardSpendChangedOnlyEvictsBudgetedTokens(t *testing.T) {
	ctx := context.Background()
	db := &fakeBudgetDB{}
	g := NewBudgetGuard(db, 0)
	g.Check(ctx, "t1")
	g.SpendChanged("t1")
	g.Check(ctx, "t1")
	if db.queries != 1 {
		t.Fatalf("token without a budget re-queried after a spend write (%d queries)", db.queries)
	}

	b := 1.0
	db.budget = &b
	g.Evict("t1") // budget set
	g.Check(ctx, "t1")
	g.SpendChanged("t1")
	g.Check(ctx, "t1")
	if db.queries != 3 {
		t.Fatalf("token with a budget not re-read after a spend write (%d queries)", db.queries)
	}
}

func TestBudgetGuardDoesNotCacheReadThatRacedEviction(t *testing.T) {
	ctx := context.Background()
	b := 0.001
	db := &fakeBudgetDB{budget: &b, spend: 500}
	g := NewBudgetGuard(db, 0)
	db.during = func() { db.spend = 1200; g.SpendChanged("t1") } // spend committed mid-read
	if st := g.Check(ctx, "t1"); st.Exceeded() {
		t.Fatal("the in-flight read should still see the old spend")
	}
	db.during = nil
	if st := g.Check(ctx, "t1"); !st.Exceeded() || db.queries != 2 {
		t.Fatalf("stale read was cached: exceeded=%v queries=%d", st.Exceeded(), db.queries)
	}
}
