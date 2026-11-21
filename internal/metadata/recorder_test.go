package metadata

import (
	"context"
	"fmt"
	"os"
	"strings"
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
