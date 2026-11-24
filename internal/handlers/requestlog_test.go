package handlers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type seedOrg struct{ id, tokenID string }

func seedOrgWithToken(t *testing.T, db *pgxpool.Pool, tokenName string) seedOrg {
	t.Helper()
	ctx := context.Background()
	s := seedOrg{id: uuid.NewString(), tokenID: uuid.NewString()}
	if _, err := db.Exec(ctx, `INSERT INTO orgs (id, name) VALUES ($1, $2)`, s.id, "rl-"+s.id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO service_tokens (id, org_id, name, token_prefix, token_hash) VALUES ($1,$2,$3,'p',$4)`,
		s.tokenID, s.id, tokenName, "h-"+s.tokenID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(ctx, `DELETE FROM orgs WHERE id = $1`, s.id) })
	return s
}

func seedReq(t *testing.T, db *pgxpool.Pool, org seedOrg, rid, model, provider, status string, age time.Duration, withToken bool) {
	t.Helper()
	var tok any
	if withToken {
		tok = org.tokenID
	}
	_, err := db.Exec(context.Background(), `
		INSERT INTO request_metadata (zer0_request_id, org_id, service_token_id, provider, model, canonical_model,
			provider_key_label, status, attempt_count, input_tokens, output_tokens, estimated_cost_usd, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,'k-b',$7,2,10,5,0.5, NOW() - $8::interval)`,
		rid, org.id, tok, provider, model+"-ver", model, status, fmt.Sprintf("%d milliseconds", age.Milliseconds()))
	if err != nil {
		t.Fatal(err)
	}
}

func seedAttempt(t *testing.T, db *pgxpool.Pool, org seedOrg, rid string, order int, label, outcome string, class *string) {
	t.Helper()
	_, err := db.Exec(context.Background(), `
		INSERT INTO request_attempts (zer0_request_id, org_id, provider, model, provider_key_label, attempt_order,
			status, outcome, error_class, http_status, latency_ms)
		VALUES ($1,$2,'groq','m',$3,$4,$5,$5,$6,$7,50)`,
		rid, org.id, label, order, outcome, class, map[bool]any{true: 429, false: nil}[class != nil])
	if err != nil {
		t.Fatal(err)
	}
}

func TestRequestLog_IsolationFiltersCursorTrail(t *testing.T) {
	db := testPool(t)
	ctx := context.Background()
	a := seedOrgWithToken(t, db, "billing-worker")
	b := seedOrgWithToken(t, db, "other")
	p := uuid.NewString()[:8]

	for i := 0; i < 5; i++ {
		status := "success"
		if i == 4 {
			status = "error"
		}
		seedReq(t, db, a, fmt.Sprintf("r0-%s-a%d", p, i), "llama", "groq", status, time.Duration(5-i)*time.Second, i != 3)
	}
	seedReq(t, db, a, "r0-"+p+"-other-model", "qwen", "openrouter", "success", 10*time.Second, true)
	seedReq(t, db, b, "r0-"+p+"-b0", "llama", "groq", "success", time.Second, true)

	// trail ordering: insert out of order
	rl := "r0-" + p + "-a0"
	rate := "rate_limit"
	seedAttempt(t, db, a, rl, 1, "groq-b", "success", nil)
	seedAttempt(t, db, a, rl, 0, "groq-a", "error", &rate)

	// isolation + all
	items, _, err := listRequests(ctx, db, a.id, requestFilter{Limit: 200})
	if err != nil || len(items) != 6 {
		t.Fatalf("org a: err=%v n=%d", err, len(items))
	}
	for _, it := range items {
		if it.RequestID == "r0-"+p+"-b0" {
			t.Fatal("org B row leaked to org A")
		}
	}
	if it, _ := getRequest(ctx, db, a.id, "r0-"+p+"-b0"); it != nil {
		t.Fatal("org A fetched org B request by id")
	}
	if it, _ := getRequest(ctx, db, b.id, "r0-"+p+"-b0"); it == nil {
		t.Fatal("org B cannot fetch own request")
	}

	// trail + token_name
	got, err := getRequest(ctx, db, a.id, rl)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if len(got.Trail) != 2 || got.Trail[0].Order != 0 || got.Trail[1].Order != 1 ||
		got.Trail[0].Class == nil || *got.Trail[0].Class != "rate_limit" ||
		*got.Trail[0].KeyLabel != "groq-a" || got.Trail[1].Outcome != "success" {
		t.Fatalf("bad trail: %+v", got.Trail)
	}
	if got.TokenName == nil || *got.TokenName != "billing-worker" || got.AttemptCount != 2 {
		t.Fatalf("bad item: %+v", got)
	}

	// filters
	if items, _, _ = listRequests(ctx, db, a.id, requestFilter{Model: "qwen"}); len(items) != 1 {
		t.Fatalf("model filter: %d", len(items))
	}
	if items, _, _ = listRequests(ctx, db, a.id, requestFilter{Provider: "openrouter"}); len(items) != 1 {
		t.Fatalf("provider filter: %d", len(items))
	}
	if items, _, _ = listRequests(ctx, db, a.id, requestFilter{Status: "error"}); len(items) != 1 {
		t.Fatalf("status filter: %d", len(items))
	}
	if items, _, _ = listRequests(ctx, db, a.id, requestFilter{TokenID: a.tokenID}); len(items) != 5 {
		t.Fatalf("token filter: %d", len(items))
	}
	if items, _, _ = listRequests(ctx, db, b.id, requestFilter{TokenID: a.tokenID}); len(items) != 0 {
		t.Fatalf("cross-org token filter leaked: %d", len(items))
	}
	since := time.Now().Add(-3500 * time.Millisecond)
	if items, _, _ = listRequests(ctx, db, a.id, requestFilter{Since: &since}); len(items) != 3 {
		t.Fatalf("since filter: %d", len(items))
	}

	// cursor paging: 6 rows, limit 4 then rest, no overlap, desc order
	page1, next, err := listRequests(ctx, db, a.id, requestFilter{Limit: 4})
	if err != nil || len(page1) != 4 || next == "" {
		t.Fatalf("page1: %v n=%d next=%q", err, len(page1), next)
	}
	before, _ := parseTime(next)
	page2, next2, err := listRequests(ctx, db, a.id, requestFilter{Limit: 4, Before: before})
	if err != nil || len(page2) != 2 || next2 != "" {
		t.Fatalf("page2: %v n=%d next=%q", err, len(page2), next2)
	}
	seen := map[string]bool{}
	for _, it := range append(page1, page2...) {
		if seen[it.RequestID] {
			t.Fatalf("dup across pages: %s", it.RequestID)
		}
		seen[it.RequestID] = true
	}
	if page1[0].CreatedAt < page1[3].CreatedAt {
		t.Fatal("not descending")
	}

	// costs
	rows, total, err := costs(ctx, db, a.id)
	if err != nil || len(rows) != 2 || total < 2.99 || total > 3.01 {
		t.Fatalf("costs: %v rows=%+v total=%v", err, rows, total)
	}
	orgRows := rows
	if rows, _, _ = costs(ctx, db, uuid.NewString()); len(rows) != 0 {
		t.Fatal("unknown org has costs")
	}
	byToken, err := costsByToken(ctx, db, a.id)
	if err != nil || len(byToken) == 0 {
		t.Fatalf("costsByToken: %v %+v", err, byToken)
	}
	var tokTotal float64
	var reqs int64
	for _, c := range byToken {
		tokTotal += c.EstimatedCostUSD
		reqs += c.RequestCount
	}
	var rowReqs int64
	for _, c := range orgRows {
		rowReqs += c.RequestCount
	}
	if tokTotal < total-0.001 || tokTotal > total+0.001 || reqs != rowReqs {
		t.Fatalf("by_token does not add up: cost %v vs %v, requests %d vs %d", tokTotal, total, reqs, rowReqs)
	}
	if other, _ := costsByToken(ctx, db, uuid.NewString()); len(other) != 0 {
		t.Fatal("unknown org has token costs")
	}
}
