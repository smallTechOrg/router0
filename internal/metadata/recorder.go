package metadata

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Record struct {
	UserID          string
	Zer0RequestID   string
	Provider        string
	Model           string
	ProviderKeyID   string
	Pool            string
	InputTokens     int
	OutputTokens    int
	LatencyMs       int64
	EstimatedCostUSD float64
	Status          string
	RetryCount      int
	ErrorClass      string
}

type Recorder struct {
	pool *pgxpool.Pool
}

func NewRecorder(pool *pgxpool.Pool) *Recorder {
	return &Recorder{pool: pool}
}

// RecordAsync writes request metadata asynchronously (fire and forget).
func (r *Recorder) RecordAsync(rec Record) {
	go func() {
		ctx := context.Background()
		if err := r.record(ctx, rec); err != nil {
			slog.Error("metadata: record failed", "error", err)
		}
	}()
}

func (r *Recorder) record(ctx context.Context, rec Record) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO request_metadata 
		 (user_id, zer0_request_id, provider, model, provider_key_id, pool, input_tokens, output_tokens, latency_ms, estimated_cost_usd, cost_usd, status, retry_count, error_class, error_type)
		 VALUES ($1, $2, $3, $4, NULLIF($5,'')::uuid, $6, $7, $8, $9, $10, $10, $11, $12, NULLIF($13,''), NULLIF($13,''))`,
		rec.UserID, rec.Zer0RequestID, rec.Provider, rec.Model, rec.ProviderKeyID,
		rec.Pool, rec.InputTokens, rec.OutputTokens, rec.LatencyMs,
		rec.EstimatedCostUSD, rec.Status, rec.RetryCount, rec.ErrorClass,
	)
	return err
}

// EstimateCost estimates cost for the given provider/model/tokens.
func EstimateCost(provider, model string, inputTokens, outputTokens int) float64 {
	type pricing struct{ in, out float64 }
	table := map[string]pricing{
		"google:gemini-1.5-flash":           {0.075e-6, 0.30e-6},
		"google:gemini-1.5-pro":             {3.50e-6, 10.50e-6},
		"groq:llama-3.1-70b-versatile":      {0.59e-6, 0.79e-6},
		"groq:mixtral-8x7b-32768":           {0.24e-6, 0.24e-6},
		"nvidia:meta/llama-3.1-70b-instruct": {0.35e-6, 0.40e-6},
		"nvidia:meta/llama-3.1-8b-instruct":  {0.10e-6, 0.10e-6},
		"together:meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo": {0.88e-6, 0.88e-6},
		"together:mistralai/Mixtral-8x7B-Instruct-v0.1":          {0.60e-6, 0.60e-6},
	}
	key := provider + ":" + model
	if p, ok := table[key]; ok {
		return float64(inputTokens)*p.in + float64(outputTokens)*p.out
	}
	return 0
}
