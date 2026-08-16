-- Accumulates all-time performance stats per provider+model.
-- Updated by the promoter on each run; provides historical data
-- independent of the rolling window used for pool scoring.
CREATE TABLE IF NOT EXISTS model_perf_stats (
    provider        text NOT NULL,
    model_id        text NOT NULL,
    total_requests  bigint NOT NULL DEFAULT 0,
    total_errors    bigint NOT NULL DEFAULT 0,
    total_latency_ms bigint NOT NULL DEFAULT 0,
    p50_latency_ms  integer,
    p95_latency_ms  integer,
    p99_latency_ms  integer,
    min_latency_ms  integer,
    max_latency_ms  integer,
    last_updated_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (provider, model_id)
);
