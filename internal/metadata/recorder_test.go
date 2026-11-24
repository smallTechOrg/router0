package metadata

import (
	"context"
	"fmt"
	"github.com/zer0-router/zer0/internal/testutil/testdb"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	dto "github.com/prometheus/client_model/go"
	"github.com/zer0-router/zer0/internal/metrics"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	testdb.Migrate(t, dsn)
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestRecorderWritesRequestAndAttempts(t *testing.T) {
	db := testPool(t)
	ctx := context.Background()
	org, tok, key := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO orgs (id, name) VALUES ($1,$2)`, org, "rec-"+org); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(ctx, `DELETE FROM orgs WHERE id=$1`, org) })
	if _, err := db.Exec(ctx, `INSERT INTO service_tokens (id, org_id, name, token_prefix, token_hash) VALUES ($1,$2,'t','p',$3)`, tok, org, "h-"+tok); err != nil {
		t.Fatal(err)
	}
	_ = key

	rid := "r0-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	rec := NewRecorder(db)
	rec.Record(Request{
		RequestID: rid, OrgID: org, TokenID: tok, CanonicalModel: "llama-3.3-70b", Provider: "groq", Model: "llama-3.3-70b-versatile",
		KeyLabel: "groq-b", InputTokens: 12, OutputTokens: 7, LatencyMs: 812, CostUSD: 0.0000126, Status: "success", HTTPStatus: 200,
		Stream: true,
		Attempts: []Attempt{
			{Order: 0, Provider: "groq", Model: "llama-3.3-70b-versatile", KeyLabel: "groq-a", Outcome: "error", ErrorClass: "rate_limit",
				ProviderErrorCode: strings.Repeat("c", 300), HTTPStatus: 429, RetryAfterS: 12, LatencyMs: 95},
			{Order: 1, Provider: "groq", Model: "llama-3.3-70b-versatile", KeyLabel: "groq-c", Outcome: "skipped", ErrorClass: "rate_limit", SkipReason: "rate_limited", RetryAfterS: 5},
			{Order: 2, Provider: "groq", Model: "llama-3.3-70b-versatile", KeyLabel: "groq-b", Outcome: "success", LatencyMs: 717},
		},
	})
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rec.Close(cctx); err != nil {
		t.Fatal(err)
	}

	var gotOrg, label, status, canon string
	var attempts, retry, http int
	var stream bool
	err := db.QueryRow(ctx, `SELECT org_id::text, provider_key_label, status, canonical_model, attempt_count, retry_count, http_status, stream
		FROM request_metadata WHERE zer0_request_id=$1`, rid).Scan(&gotOrg, &label, &status, &canon, &attempts, &retry, &http, &stream)
	if err != nil {
		t.Fatal(err)
	}
	if gotOrg != org || label != "groq-b" || status != "success" || canon != "llama-3.3-70b" || attempts != 3 || retry != 2 || http != 200 || !stream {
		t.Fatalf("row mismatch: %v %v %v %v %v %v %v %v", gotOrg, label, status, canon, attempts, retry, http, stream)
	}
	rows, err := db.Query(ctx, `SELECT attempt_order, outcome, COALESCE(error_class,''), COALESCE(skip_reason,''), COALESCE(retry_after_s,0),
		COALESCE(length(provider_error_code),0), COALESCE(length(error_message),0), provider_key_label
		FROM request_attempts WHERE zer0_request_id=$1 ORDER BY attempt_order`, rid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var ord, ra, l1, l2 int
		var out, cl, sr, lbl string
		if err := rows.Scan(&ord, &out, &cl, &sr, &ra, &l1, &l2, &lbl); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d|%s|%s|%s|%d|%d|%d|%s", ord, out, cl, sr, ra, l1, l2, lbl))
	}
	want := []string{"0|error|rate_limit||12|200|200|groq-a", "1|skipped|rate_limit|rate_limited|5|0|0|groq-c", "2|success|||0|0|0|groq-b"}
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attempt %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestRecorderNoMatchOfNoneProvider(t *testing.T) {
	db := testPool(t)
	ctx := context.Background()
	org := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO orgs (id, name) VALUES ($1,$2)`, org, "rec-"+org); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(ctx, `DELETE FROM orgs WHERE id=$1`, org) })
	rid := "r0-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	rec := NewRecorder(db)
	rec.Record(Request{RequestID: rid, OrgID: org, CanonicalModel: "nope", Status: "error", HTTPStatus: 404, ErrorCode: "model_not_found"})
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = rec.Close(cctx)
	var prov, code string
	var n int
	if err := db.QueryRow(ctx, `SELECT provider, error_code, attempt_count FROM request_metadata WHERE zer0_request_id=$1`, rid).Scan(&prov, &code, &n); err != nil {
		t.Fatal(err)
	}
	if prov != "none" || code != "model_not_found" || n != 0 {
		t.Fatalf("%s %s %d", prov, code, n)
	}
}

// ---- no-DB tests ----

type blockingDB struct{ release chan struct{} }

func (b *blockingDB) SendBatch(ctx context.Context, bt *pgx.Batch) pgx.BatchResults {
	<-b.release
	return errResults{}
}

type errResults struct{ pgx.BatchResults }

func (errResults) Exec() (pgconn.CommandTag, error) { return pgconn.CommandTag{}, fmt.Errorf("fake") }
func (errResults) Close() error                     { return nil }

func TestRecorderDropsWhenBufferFull(t *testing.T) {
	db := &blockingDB{release: make(chan struct{})}
	rec := NewRecorder(db, Options{Buffer: 2, Workers: 1})
	before := counterVal()
	for i := 0; i < 20; i++ {
		rec.Record(Request{RequestID: fmt.Sprint("r", i)})
	}
	dropped := counterVal() - before
	if dropped < 15 {
		t.Fatalf("expected most records dropped, got %v", dropped)
	}
	close(db.release)
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rec.Close(cctx); err != nil {
		t.Fatal(err)
	}
	// after Close, Record never blocks or panics
	rec.Record(Request{RequestID: "late"})
}

func TestNilRecorderIsSafe(t *testing.T) {
	var r *Recorder
	r.Record(Request{})
	if err := r.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func counterVal() float64 {
	var m dto.Metric
	_ = metrics.MetadataDroppedTotal.Write(&m)
	return m.GetCounter().GetValue()
}

// Sync mode (Cloud Run request-based billing): the row must exist as soon as
// Record returns, with no background worker involved.
func TestRecorderSyncWritesBeforeReturn(t *testing.T) {
	db := testPool(t)
	ctx := context.Background()
	org := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO orgs (id, name) VALUES ($1,$2)`, org, "recsync-"+org); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(ctx, `DELETE FROM orgs WHERE id=$1`, org) })

	rec := NewRecorder(db, Options{Sync: true})
	rid := "r0-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	rec.Record(Request{RequestID: rid, OrgID: org, CanonicalModel: "m", Provider: "groq", Model: "m", Status: "success", HTTPStatus: 200,
		Attempts: []Attempt{{Order: 0, Provider: "groq", Model: "m", Outcome: "success"}}})

	var n, a int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM request_metadata WHERE zer0_request_id=$1`, rid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM request_attempts WHERE zer0_request_id=$1`, rid).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if n != 1 || a != 1 {
		t.Fatalf("sync record not visible immediately: request rows=%d attempt rows=%d", n, a)
	}
	// Close stays safe and Record after Close is a no-op, not a panic.
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := rec.Close(cctx); err != nil {
		t.Fatal(err)
	}
	rec.Record(Request{RequestID: "late"})
}

func TestRecorderSyncViaEnv(t *testing.T) {
	t.Setenv("ROUTER0_RECORDER_SYNC", "true")
	rec := NewRecorder(&blockingDB{release: make(chan struct{})})
	if !rec.sync {
		t.Fatal("ROUTER0_RECORDER_SYNC=true must enable sync mode")
	}
}

func TestRecorderSpendAndReconcile(t *testing.T) {
	db := testPool(t)
	ctx := context.Background()
	org, user := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO orgs (id, name) VALUES ($1,$2)`, org, "spend-"+org); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1,$2,'x')`, user, "spend-"+user+"@example.test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(ctx, `DELETE FROM request_metadata WHERE org_id=$1`, org)
		_, _ = db.Exec(ctx, `DELETE FROM orgs WHERE id=$1`, org)
		_, _ = db.Exec(ctx, `DELETE FROM users WHERE id=$1`, user)
	})
	var key string
	if err := db.QueryRow(ctx, `INSERT INTO provider_keys (user_id, org_id, provider, label, ciphertext, nonce)
		VALUES ($1,$2,'groq','spend','\x00','\x00') RETURNING id::text`, user, org).Scan(&key); err != nil {
		t.Fatal(err)
	}
	var notified []string
	rec := NewRecorder(db, Options{Sync: true, OnSpend: func(k string) { notified = append(notified, k) }})
	for i, st := range []string{"success", "success", "error"} {
		rec.Record(Request{RequestID: fmt.Sprintf("r0-spend-%s-%d", key[:8], i), OrgID: org, CanonicalModel: "m", Provider: "groq",
			Model: "m", KeyID: key, CostUSD: 0.0006, Status: st, HTTPStatus: 200})
	}
	spend := func() int64 {
		var v int64
		_ = db.QueryRow(ctx, `SELECT COALESCE(SUM(spend_micro_usd),0) FROM provider_key_spend WHERE provider_key_id=$1`, key).Scan(&v)
		return v
	}
	if got := spend(); got != 1200 {
		t.Fatalf("spend = %d µUSD, want 1200 (errors are not billed)", got)
	}
	if len(notified) != 2 || notified[0] != key {
		t.Fatalf("OnSpend calls = %v", notified)
	}
	if _, err := db.Exec(ctx, `DELETE FROM provider_key_spend WHERE provider_key_id=$1`, key); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(ctx, db, time.Now(), org); err != nil {
		t.Fatal(err)
	}
	if got := spend(); got != 1200 {
		t.Fatalf("after reconcile spend = %d µUSD, want 1200", got)
	}
	// A drifted counter is corrected back to the request log.
	if _, err := db.Exec(ctx, `UPDATE provider_key_spend SET spend_micro_usd = 99999 WHERE provider_key_id=$1`, key); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(ctx, db, time.Now(), org); err != nil {
		t.Fatal(err)
	}
	if got := spend(); got != 1200 {
		t.Fatalf("after second reconcile spend = %d µUSD, want 1200", got)
	}
}

// TestReconcileDoesNotLoseConcurrentSpend runs reconciles while requests are
// recorded concurrently; with the advisory lock no increment is overwritten,
// so the counter ends equal to the request log.
func TestReconcileDoesNotLoseConcurrentSpend(t *testing.T) {
	db := testPool(t)
	ctx := context.Background()
	org, user := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO orgs (id, name) VALUES ($1,$2)`, org, "race-"+org); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1,$2,'x')`, user, "race-"+user+"@example.test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(ctx, `DELETE FROM request_metadata WHERE org_id=$1`, org)
		_, _ = db.Exec(ctx, `DELETE FROM orgs WHERE id=$1`, org)
		_, _ = db.Exec(ctx, `DELETE FROM users WHERE id=$1`, user)
	})
	var key string
	if err := db.QueryRow(ctx, `INSERT INTO provider_keys (user_id, org_id, provider, label, ciphertext, nonce)
		VALUES ($1,$2,'groq','race','\x00','\x00') RETURNING id::text`, user, org).Scan(&key); err != nil {
		t.Fatal(err)
	}
	rec := NewRecorder(db, Options{Sync: true})
	const writers, each = 8, 15
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < each; i++ {
					rec.Record(Request{RequestID: fmt.Sprintf("r0-race-%s-%d-%d", key[:8], w, i), OrgID: org, CanonicalModel: "m",
						Provider: "groq", Model: "m", KeyID: key, CostUSD: 0.0006, Status: "success", HTTPStatus: 200})
				}
			}(w)
		}
		wg.Wait()
	}()
	for running := true; running; {
		select {
		case <-done:
			running = false
		default:
			if _, err := Reconcile(ctx, db, time.Now(), org); err != nil {
				t.Fatal(err)
			}
		}
	}
	var spend int64
	if err := db.QueryRow(ctx, `SELECT COALESCE(SUM(spend_micro_usd),0) FROM provider_key_spend WHERE provider_key_id=$1`, key).Scan(&spend); err != nil {
		t.Fatal(err)
	}
	if want := int64(writers * each * 600); spend != want {
		t.Fatalf("spend = %d µUSD after concurrent writes and reconciles, want %d", spend, want)
	}
}
