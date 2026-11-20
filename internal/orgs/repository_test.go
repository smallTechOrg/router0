package orgs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOrgForUserAndTx(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := NewRepository(pool)

	n := time.Now().UnixNano()
	var uid, loner string
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1,'x') RETURNING id::text`, fmt.Sprintf("o1-%d@test.local", n)).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1,'x') RETURNING id::text`, fmt.Sprintf("o2-%d@test.local", n)).Scan(&loner); err != nil {
		t.Fatal(err)
	}
	var orgID string
	t.Cleanup(func() {
		if orgID != "" {
			pool.Exec(ctx, `DELETE FROM orgs WHERE id=$1`, orgID)
		}
		pool.Exec(ctx, `DELETE FROM users WHERE id IN ($1,$2)`, uid, loner)
	})

	if _, _, err := repo.OrgForUser(ctx, loner); !errors.Is(err, ErrNoOrg) {
		t.Fatalf("want ErrNoOrg, got %v", err)
	}

	// rolled-back transaction leaves nothing behind
	tx, _ := pool.Begin(ctx)
	if _, err := CreateWithOwner(ctx, tx, "rolled back", uid); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(ctx)
	if _, _, err := repo.OrgForUser(ctx, uid); !errors.Is(err, ErrNoOrg) {
		t.Fatalf("rollback must leave no membership, got %v", err)
	}

	tx, _ = pool.Begin(ctx)
	orgID, err = CreateWithOwner(ctx, tx, "Acme", uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := AddMember(ctx, tx, orgID, loner, RoleMember); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	gotOrg, role, err := repo.OrgForUser(ctx, uid)
	if err != nil || gotOrg != orgID || role != "owner" {
		t.Fatalf("OrgForUser: %q %q %v", gotOrg, role, err)
	}
	if _, role, _ := repo.OrgForUser(ctx, loner); role != "member" {
		t.Fatalf("member role: %q", role)
	}
	o, err := repo.GetForUser(ctx, uid)
	if err != nil || o.Name != "Acme" || o.Role != "owner" {
		t.Fatalf("GetForUser: %+v %v", o, err)
	}
	// one org per user
	tx, _ = pool.Begin(ctx)
	if _, err := CreateWithOwner(ctx, tx, "second", uid); err == nil {
		t.Fatal("second org for same user must fail")
	}
	tx.Rollback(ctx)

	pool.Exec(ctx, `UPDATE users SET status='disabled' WHERE id=$1`, uid)
	if _, _, err := repo.OrgForUser(ctx, uid); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("want ErrUserDisabled, got %v", err)
	}

	sums, err := repo.ListSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range sums {
		if s.ID == orgID {
			found = true
			if s.MemberCount != 2 || s.TokenCount != 0 || s.KeyCount != 0 {
				t.Fatalf("summary counts: %+v", s)
			}
		}
	}
	if !found {
		t.Fatal("org missing from ListSummaries")
	}
}
