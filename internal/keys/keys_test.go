package keys

import (
	"context"
	"errors"
	"fmt"
	"github.com/zer0-router/zer0/internal/testutil/testdb"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	testdb.Migrate(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newOrg creates a unique org plus a user; removes both on cleanup.
func newOrg(t *testing.T, pool *pgxpool.Pool) (orgID, userID string) {
	t.Helper()
	ctx := context.Background()
	sfx := fmt.Sprintf("%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx, `INSERT INTO orgs (name) VALUES ($1) RETURNING id`, "t-"+sfx).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`, "keys-"+sfx+"@test.local").Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM orgs WHERE id=$1`, orgID)
		pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	return
}

func add(t *testing.T, r *Repository, org, user, provider, label string) *ProviderKey {
	t.Helper()
	k, err := r.Create(context.Background(), org, user, provider, label, "hint…", "", []byte("ct"), []byte("nonce"))
	if err != nil {
		t.Fatalf("create %s/%s: %v", provider, label, err)
	}
	return k
}

func ids(ks []*ProviderKey) []string {
	out := []string{}
	for _, k := range ks {
		out = append(out, k.ID)
	}
	return out
}

func TestPositionsAndActiveOrder(t *testing.T) {
	pool := testPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	org, user := newOrg(t, pool)

	k1 := add(t, r, org, user, "groq", "a")
	k2 := add(t, r, org, user, "groq", "")
	k3 := add(t, r, org, user, "groq", "c")
	if k1.Position != 0 || k2.Position != 1 || k3.Position != 2 {
		t.Fatalf("positions = %d %d %d", k1.Position, k2.Position, k3.Position)
	}
	if k2.Label != "groq-2" {
		t.Fatalf("default label = %q", k2.Label)
	}
	if _, err := r.Create(ctx, org, user, "groq", "a", "h", "", []byte("c"), []byte("n")); !errors.Is(err, ErrDuplicateLabel) {
		t.Fatalf("want ErrDuplicateLabel, got %v", err)
	}

	got, _ := r.ActiveByOrgProvider(ctx, org, "groq")
	if !reflect.DeepEqual(ids(got), []string{k1.ID, k2.ID, k3.ID}) {
		t.Fatalf("active order %v", ids(got))
	}

	if err := r.ReorderKeys(ctx, org, "groq", []string{k3.ID, k1.ID, k2.ID}); err != nil {
		t.Fatal(err)
	}
	got, _ = r.ActiveByOrgProvider(ctx, org, "groq")
	if !reflect.DeepEqual(ids(got), []string{k3.ID, k1.ID, k2.ID}) {
		t.Fatalf("after reorder %v", ids(got))
	}
	if err := r.ReorderKeys(ctx, org, "groq", []string{k3.ID, k1.ID}); !errors.Is(err, ErrInvalidKeyOrder) {
		t.Fatalf("partial reorder: %v", err)
	}
	if err := r.MoveKey(ctx, org, k2.ID, 0); err != nil {
		t.Fatal(err)
	}
	got, _ = r.ActiveByOrgProvider(ctx, org, "groq")
	if !reflect.DeepEqual(ids(got), []string{k2.ID, k3.ID, k1.ID}) {
		t.Fatalf("after move %v", ids(got))
	}

	// Disable excludes from active; label reusable.
	if err := r.Disable(ctx, k3.ID, org); err != nil {
		t.Fatal(err)
	}
	got, _ = r.ActiveByOrgProvider(ctx, org, "groq")
	if !reflect.DeepEqual(ids(got), []string{k2.ID, k1.ID}) {
		t.Fatalf("after disable %v", ids(got))
	}
	add(t, r, org, user, "groq", "c")
	if _, err := r.GetByID(ctx, k3.ID, org); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled get: %v", err)
	}
}

func TestMarkAuthErrorAndRecordTest(t *testing.T) {
	pool := testPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	org, user := newOrg(t, pool)
	k := add(t, r, org, user, "groq", "a")
	other := add(t, r, org, user, "groq", "b")

	if err := r.MarkAuthError(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := r.ActiveByOrgProvider(ctx, org, "groq")
	if !reflect.DeepEqual(ids(got), []string{other.ID}) {
		t.Fatalf("active after auth error %v", ids(got))
	}
	g, _ := r.GetByID(ctx, k.ID, org)
	if g.Status != "error" || g.LastTestResult != "auth_error" {
		t.Fatalf("status=%s result=%s", g.Status, g.LastTestResult)
	}
	if err := r.RecordTest(ctx, k.ID, org, "ok", true, false); err != nil {
		t.Fatal(err)
	}
	g, _ = r.GetByID(ctx, k.ID, org)
	if g.Status != "active" || g.LastTestedAt == nil {
		t.Fatalf("not reactivated: %+v", g)
	}
	if err := r.RecordTest(ctx, k.ID, org, "error:auth", false, true); err != nil {
		t.Fatal(err)
	}
	g, _ = r.GetByID(ctx, k.ID, org)
	if g.Status != "error" {
		t.Fatalf("status=%s", g.Status)
	}
}

func TestOrgIsolation(t *testing.T) {
	pool := testPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	orgA, userA := newOrg(t, pool)
	orgB, userB := newOrg(t, pool)
	ka := add(t, r, orgA, userA, "groq", "a")
	add(t, r, orgB, userB, "groq", "a") // same label in other org is fine

	if _, err := r.GetByID(ctx, ka.ID, orgB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org get: %v", err)
	}
	if err := r.Disable(ctx, ka.ID, orgB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org disable: %v", err)
	}
	if err := r.MoveKey(ctx, orgB, ka.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org move: %v", err)
	}
	if err := r.ReorderKeys(ctx, orgB, "groq", []string{ka.ID}); !errors.Is(err, ErrInvalidKeyOrder) {
		t.Fatalf("cross-org reorder: %v", err)
	}
	listB, _ := r.ListByOrg(ctx, orgB)
	for _, k := range listB {
		if k.ID == ka.ID || k.OrgID != orgB {
			t.Fatal("org B sees org A key")
		}
	}
	a, _ := r.ActiveByOrgProvider(ctx, orgA, "groq")
	if len(a) != 1 || a[0].ID != ka.ID {
		t.Fatalf("org A active %v", ids(a))
	}
}

func TestProviderOrderAndReplace(t *testing.T) {
	pool := testPool(t)
	r := NewRepository(pool)
	ps := NewPriorityStore(pool, func() []string { return []string{"groq", "openrouter", "google", "nvidia"} })
	ctx := context.Background()
	org, user := newOrg(t, pool)
	other, otherUser := newOrg(t, pool)

	g1 := add(t, r, org, user, "groq", "a")
	g2 := add(t, r, org, user, "groq", "b")
	add(t, r, org, user, "openrouter", "o")

	order, err := ps.ProviderOrder(ctx, org)
	if err != nil || !reflect.DeepEqual(order, []string{"groq", "openrouter"}) {
		t.Fatalf("order=%v err=%v", order, err)
	}

	if err := ps.Replace(ctx, org, []string{"openrouter", "groq"}, map[string][]string{"groq": {g2.ID, g1.ID}}); err != nil {
		t.Fatal(err)
	}
	order, _ = ps.ProviderOrder(ctx, org)
	if !reflect.DeepEqual(order, []string{"openrouter", "groq"}) {
		t.Fatalf("order after replace=%v", order)
	}
	act, _ := r.ActiveByOrgProvider(ctx, org, "groq")
	if !reflect.DeepEqual(ids(act), []string{g2.ID, g1.ID}) {
		t.Fatalf("key order %v", ids(act))
	}

	// Invalid requests change nothing.
	for name, tc := range map[string]struct {
		provs []string
		ko    map[string][]string
		want  error
	}{
		"omit":       {[]string{"groq"}, nil, ErrInvalidPriority},
		"dup":        {[]string{"groq", "groq"}, nil, ErrInvalidPriority},
		"unknown":    {[]string{"groq", "nvidia"}, nil, ErrInvalidPriority},
		"foreignKey": {[]string{"groq", "openrouter"}, map[string][]string{"groq": {g1.ID, "00000000-0000-0000-0000-000000000000"}}, ErrInvalidKeyOrder},
		"badProv":    {[]string{"groq", "openrouter"}, map[string][]string{"nvidia": {}}, ErrInvalidKeyOrder},
	} {
		if err := ps.Replace(ctx, org, tc.provs, tc.ko); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v want %v", name, err, tc.want)
		}
	}
	order, _ = ps.ProviderOrder(ctx, org)
	act, _ = r.ActiveByOrgProvider(ctx, org, "groq")
	if !reflect.DeepEqual(order, []string{"openrouter", "groq"}) || !reflect.DeepEqual(ids(act), []string{g2.ID, g1.ID}) {
		t.Fatalf("state changed by invalid PUT: %v %v", order, ids(act))
	}

	// Foreign org's key id is rejected.
	fk := add(t, r, other, otherUser, "groq", "x")
	if err := ps.Replace(ctx, org, []string{"groq", "openrouter"}, map[string][]string{"groq": {g1.ID, g2.ID, fk.ID}}); !errors.Is(err, ErrInvalidKeyOrder) {
		t.Fatalf("foreign key: %v", err)
	}

	// Remainder alphabetical: new provider "google" appended at end of ranking;
	// delete its priority row -> still listed, alphabetically after ranked ones.
	add(t, r, org, user, "google", "g")
	add(t, r, org, user, "nvidia", "n")
	pool.Exec(ctx, `DELETE FROM org_provider_priority WHERE org_id=$1 AND provider IN ('google','nvidia')`, org)
	order, _ = ps.ProviderOrder(ctx, org)
	if !reflect.DeepEqual(order, []string{"openrouter", "groq", "google", "nvidia"}) {
		t.Fatalf("remainder order=%v", order)
	}

	// Provider whose keys are all disabled drops out.
	n, _ := r.ListByOrg(ctx, org)
	for _, k := range n {
		if k.Provider == "nvidia" {
			r.Disable(ctx, k.ID, org)
		}
	}
	order, _ = ps.ProviderOrder(ctx, org)
	if !reflect.DeepEqual(order, []string{"openrouter", "groq", "google"}) {
		t.Fatalf("after disable order=%v", order)
	}

	// Other org unaffected and independent.
	oo, _ := ps.ProviderOrder(ctx, other)
	if !reflect.DeepEqual(oo, []string{"groq"}) {
		t.Fatalf("other org order=%v", oo)
	}
	v, _ := ps.View(ctx, org)
	if len(v) != 3 || v[0].Provider != "openrouter" || v[1].Provider != "groq" || len(v[1].Keys) != 2 || v[1].Keys[0].ID != g2.ID {
		t.Fatalf("view=%+v", v)
	}
}

func TestPGStateReader(t *testing.T) {
	pool := testPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	org, user := newOrg(t, pool)
	k := add(t, r, org, user, "groq", "a")
	clean := add(t, r, org, user, "groq", "b")
	if _, err := pool.Exec(ctx,
		`INSERT INTO provider_key_state (provider_key_id, org_id, kind, scope_model, until_at) VALUES
		 ($1,$2,'rate_limited','m1', NOW()+INTERVAL '30 seconds'),
		 ($1,$2,'rate_limited','m2', NOW()+INTERVAL '10 seconds'),
		 ($1,$2,'transient_cooldown','', NOW()-INTERVAL '1 minute')`, k.ID, org); err != nil {
		t.Fatal(err)
	}
	st, err := NewPGStateReader(pool).States(ctx, []string{k.ID, clean.ID})
	if err != nil {
		t.Fatal(err)
	}
	s := st[k.ID]
	if !s.Blocked || s.Reason != "rate_limited" || s.RetryAfter < 25*time.Second || !reflect.DeepEqual(s.Models, []string{"m1", "m2"}) {
		t.Fatalf("state=%+v", s)
	}
	if _, ok := st[clean.ID]; ok {
		t.Fatal("clean key has state")
	}
}
