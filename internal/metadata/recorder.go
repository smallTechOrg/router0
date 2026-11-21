// Package metadata writes the org request log (request_metadata + request_attempts).
// It stores metadata only: never prompts, responses, tool arguments, provider
// error bodies, provider keys or router0 tokens.
package metadata

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zer0-router/zer0/internal/metrics"
)

const (
	defaultBuffer  = 10000
	defaultWorkers = 4
	maxCodeLen     = 200
)

// DB is the subset of *pgxpool.Pool the recorder needs.
type DB interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// Attempt is one considered candidate of a request.
type Attempt struct {
	Order             int
	Provider          string
	Model             string // provider model id
	KeyID             string
	KeyLabel          string
	Outcome           string // success | error | skipped
	ErrorClass        string
	SkipReason        string
	ProviderErrorCode string // provider's structured code only, truncated to 200
	HTTPStatus        int
	RetryAfterS       int
	LatencyMs         int64
}

// Request is one inference request.
type Request struct {
	RequestID      string // r0-...
	OrgID          string
	TokenID        string
	CanonicalModel string
	Provider       string // winner or "none"
	Model          string // provider model id, or canonical name when none
	KeyID          string
	KeyLabel       string
	InputTokens    int
	OutputTokens   int
	LatencyMs      int64
	CostUSD        float64
	Status         string // success | error
	HTTPStatus     int
	ErrorCode      string
	Stream         bool
	Attempts       []Attempt
}

// Options tune the recorder.
type Options struct {
	Buffer  int
	Workers int
	// Sync writes each record inline in Record (no background workers). Use it on
	// platforms that only grant CPU while a request is in flight (Cloud Run
	// request-based billing), where async writers would be starved between
	// requests. Also enabled by ROUTER0_RECORDER_SYNC=true.
	Sync bool
}

// Recorder is a buffered async writer. Methods are safe on a nil *Recorder.
type Recorder struct {
	db      DB
	ch      chan Request
	wg      sync.WaitGroup
	mu      sync.RWMutex
	closed  bool
	workers int
	sync    bool
}

// NewRecorder starts the worker pool (4 workers, 10 000 buffered records).
func NewRecorder(db DB, opts ...Options) *Recorder {
	o := Options{Buffer: defaultBuffer, Workers: defaultWorkers}
	if len(opts) > 0 {
		if opts[0].Buffer > 0 {
			o.Buffer = opts[0].Buffer
		}
		if opts[0].Workers > 0 {
			o.Workers = opts[0].Workers
		}
	}
	if len(opts) > 0 && opts[0].Sync {
		o.Sync = true
	}
	if os.Getenv("ROUTER0_RECORDER_SYNC") == "true" {
		o.Sync = true
	}
	r := &Recorder{db: db, ch: make(chan Request, o.Buffer), workers: o.Workers, sync: o.Sync}
	if o.Sync {
		return r // no workers: Record writes inline
	}
	for i := 0; i < o.Workers; i++ {
		r.wg.Add(1)
		go r.worker()
	}
	return r
}

// Record enqueues a request. It never blocks: when the buffer is full the record
// is dropped and router0_metadata_dropped_total is incremented.
func (r *Recorder) Record(req Request) {
	if r == nil {
		return
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		metrics.MetadataDroppedTotal.Inc()
		return
	}
	if r.sync {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.write(ctx, req); err != nil {
			metrics.MetadataDroppedTotal.Inc()
			slog.Error("metadata write failed", "request_id", req.RequestID, "error", err.Error())
		}
		return
	}
	select {
	case r.ch <- req:
	default:
		metrics.MetadataDroppedTotal.Inc()
	}
}

// Close stops accepting records and flushes the buffer, waiting until ctx ends.
func (r *Recorder) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.ch)
	}
	r.mu.Unlock()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Recorder) worker() {
	defer r.wg.Done()
	for req := range r.ch {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := r.write(ctx, req); err != nil {
			slog.Error("metadata write failed", "request_id", req.RequestID, "error", err.Error())
		}
		cancel()
	}
}

const insertRequest = `INSERT INTO request_metadata
 (org_id, service_token_id, zer0_request_id, canonical_model, provider, model, provider_key_id, provider_key_label,
  input_tokens, output_tokens, latency_ms, estimated_cost_usd, cost_usd, status, http_status, error_code, stream,
  attempt_count, retry_count)
 VALUES (NULLIF($1,'')::uuid, NULLIF($2,'')::uuid, $3, NULLIF($4,''), $5, $6, NULLIF($7,'')::uuid, NULLIF($8,''),
  $9, $10, $11, $12, $12, $13, NULLIF($14,0), NULLIF($15,''), $16, $17, $18)`

const insertAttempt = `INSERT INTO request_attempts
 (zer0_request_id, org_id, provider, model, provider_key_id, provider_key_label, attempt_order, status, outcome,
  error_class, skip_reason, error_message, provider_error_code, http_status, retry_after_s, latency_ms)
 VALUES ($1, NULLIF($2,'')::uuid, $3, $4, NULLIF($5,'')::uuid, NULLIF($6,''), $7, $8, $8,
  NULLIF($9,''), NULLIF($10,''), NULLIF($11,''), NULLIF($11,''), NULLIF($12,0), NULLIF($13,0), $14)`

func (r *Recorder) write(ctx context.Context, q Request) error {
	attempts := len(q.Attempts)
	retry := attempts - 1
	if retry < 0 {
		retry = 0
	}
	provider, model := q.Provider, q.Model
	if provider == "" {
		provider = "none"
	}
	if model == "" {
		model = q.CanonicalModel
	}
	b := &pgx.Batch{}
	b.Queue(insertRequest, q.OrgID, q.TokenID, q.RequestID, q.CanonicalModel, provider, model, q.KeyID, q.KeyLabel,
		q.InputTokens, q.OutputTokens, q.LatencyMs, q.CostUSD, q.Status, q.HTTPStatus, trunc(q.ErrorCode, maxCodeLen), q.Stream,
		attempts, retry)
	for _, a := range q.Attempts {
		b.Queue(insertAttempt, q.RequestID, q.OrgID, a.Provider, a.Model, a.KeyID, a.KeyLabel, a.Order, a.Outcome,
			a.ErrorClass, a.SkipReason, trunc(a.ProviderErrorCode, maxCodeLen), a.HTTPStatus, a.RetryAfterS, a.LatencyMs)
	}
	br := r.db.SendBatch(ctx, b)
	defer br.Close()
	for i := 0; i < 1+len(q.Attempts); i++ {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// EstimateCost estimates cost for the given provider/model/tokens from a small
// static table; unknown models cost 0 (providers that report cost, e.g.
// OpenRouter, are preferred by the handler).
func EstimateCost(provider, model string, inputTokens, outputTokens int) float64 {
	type pricing struct{ in, out float64 }
	table := map[string]pricing{
		"google:gemini-1.5-flash":                               {0.075e-6, 0.30e-6},
		"google:gemini-1.5-pro":                                 {3.50e-6, 10.50e-6},
		"groq:llama-3.1-70b-versatile":                          {0.59e-6, 0.79e-6},
		"groq:llama-3.3-70b-versatile":                          {0.59e-6, 0.79e-6},
		"groq:mixtral-8x7b-32768":                               {0.24e-6, 0.24e-6},
		"nvidia:meta/llama-3.1-70b-instruct":                    {0.35e-6, 0.40e-6},
		"nvidia:meta/llama-3.1-8b-instruct":                     {0.10e-6, 0.10e-6},
		"together:meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo": {0.88e-6, 0.88e-6},
		"together:mistralai/Mixtral-8x7B-Instruct-v0.1":         {0.60e-6, 0.60e-6},
	}
	if p, ok := table[provider+":"+model]; ok {
		return float64(inputTokens)*p.in + float64(outputTokens)*p.out
	}
	return 0
}
