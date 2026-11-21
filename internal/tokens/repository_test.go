package tokens

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/zer0-router/zer0/internal/auth"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newOrg creates a unique user + org (+ owner membership) and cleans up after the test.
func newOrg(t *testing.T, pool *pgxpool.Pool) (orgID, userID string) {
	t.Helper()
	ctx := context.Background()
	email := fmt.Sprintf("tok-%d@test.local", time.Now().UnixNano())
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, role, status) VALUES ($1, 'x', 'user', 'active') RETURNING id::text`,
		email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO orgs (name) VALUES ($1) RETURNING id::text`, email).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO org_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM orgs WHERE id = $1`, orgID)
		pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	return
}

func TestRepositoryLifecycleAndVerifier(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	orgA, userA := newOrg(t, pool)
	orgB, _ := newOrg(t, pool)
	repo := NewRepository(pool)
	v := NewVerifier(repo, 30*time.Second, 100)

	tok, full, err := repo.Create(ctx, orgA, userA, "svc-a")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^r0-sk-[1-9A-HJ-NP-Za-km-z]{32}$`).MatchString(full) {
		t.Fatalf("bad token %q", full)
	}
	var hash, alg string
	if err := pool.QueryRow(ctx, `SELECT token_hash, hash_alg FROM service_tokens WHERE id=$1`, tok.ID).Scan(&hash, &alg); err != nil {
		t.Fatal(err)
	}
	if hash != auth.HashToken(full) || len(hash) != 64 || alg != "sha256" || hash == full {
		t.Fatalf("stored hash wrong: %q %q", hash, alg)
	}
	if _, _, err := repo.Create(ctx, orgA, userA, "svc-a"); !errors.Is(err, ErrNameExists) {
		t.Fatalf("want ErrNameExists, got %v", err)
	}
	if _, _, err := repo.Create(ctx, orgB, "", "svc-a"); err != nil {
		t.Fatalf("same name in another org must work: %v", err)
	}
	if _, _, err := repo.Create(ctx, orgA, userA, "bad name!"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("want ErrInvalidName, got %v", err)
	}

	p, err := v.Verify(ctx, full)
	if err != nil || p.OrgID != orgA || p.TokenID != tok.ID || p.TokenName != "svc-a" {
		t.Fatalf("verify: %+v %v", p, err)
	}
	// last_used_at gets set asynchronously
	var used *time.Time
	for i := 0; i < 100 && used == nil; i++ {
		time.Sleep(10 * time.Millisecond)
		pool.QueryRow(ctx, `SELECT last_used_at FROM service_tokens WHERE id=$1`, tok.ID).Scan(&used)
	}
	if used == nil {
		t.Fatal("last_used_at not set")
	}

	// random token with right shape is rejected
	rnd, _, _ := auth.GenerateServiceToken()
	if _, err := v.Verify(ctx, rnd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}

	// cross-org revoke -> not found, token still works
	if err := repo.Revoke(ctx, orgB, tok.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := v.Verify(ctx, full); err != nil {
		t.Fatalf("token must still work: %v", err)
	}
	if err := repo.Revoke(ctx, orgA, "not-a-uuid", userA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound for malformed id, got %v", err)
	}

	// revoke evicts the cache immediately (no 30 s wait)
	if err := repo.Revoke(ctx, orgA, tok.ID, userA); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(ctx, full); !errors.Is(err, ErrRevoked) {
		t.Fatalf("want ErrRevoked right after revoke, got %v", err)
	}
	if err := repo.Revoke(ctx, orgA, tok.ID, userA); err != nil {
		t.Fatalf("revoke must be idempotent: %v", err)
	}

	// name reusable after revoke; list: live first, revoked after, no secrets
	if _, _, err := repo.Create(ctx, orgA, userA, "svc-a"); err != nil {
		t.Fatalf("name reuse after revoke: %v", err)
	}
	list, err := repo.List(ctx, orgA)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %+v", err, list)
	}
	if list[0].RevokedAt != nil || list[1].RevokedAt == nil {
		t.Fatalf("live tokens must sort before revoked: %+v", list)
	}
	if list[0].CreatedBy == nil || *list[0].CreatedBy == "" || list[0].Legacy {
		t.Fatalf("list metadata wrong: %+v", list[0])
	}
}

func TestLegacyBcryptTokenEndToEnd(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	orgID, userID := newOrg(t, pool)
	repo := NewRepository(pool)
	v := NewVerifier(repo, 30*time.Second, 100)

	raw, prefix, _ := auth.GenerateServiceToken()
	body := raw[len(auth.ServiceTokenScheme):]
	legacyRaw := auth.LegacyTokenScheme + body
	hash, _ := bcrypt.GenerateFromPassword([]byte(legacyRaw), bcrypt.MinCost)
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO service_tokens (org_id, name, token_prefix, token_hash, hash_alg, created_by)
		 VALUES ($1,'legacy-default',$2,$3,'bcrypt',$4) RETURNING id::text`,
		orgID, prefix, string(hash), userID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	p, err := v.Verify(ctx, legacyRaw)
	if err != nil || p.OrgID != orgID || p.TokenID != id || p.TokenName != "legacy-default" {
		t.Fatalf("legacy verify: %+v %v", p, err)
	}
	list, _ := repo.List(ctx, orgID)
	if len(list) != 1 || !list[0].Legacy || list[0].Prefix != auth.LegacyTokenScheme+prefix {
		t.Fatalf("legacy listing: %+v", list)
	}
	if err := repo.Revoke(ctx, orgID, id, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(ctx, legacyRaw); !errors.Is(err, ErrRevoked) {
		t.Fatalf("want ErrRevoked, got %v", err)
	}
}
