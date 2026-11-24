-- Recreates the legacy tables empty (their data is not restored).
CREATE TABLE IF NOT EXISTS pool_scores (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  score NUMERIC(10, 6) NOT NULL DEFAULT 0,
  p95_latency_ms INT NOT NULL DEFAULT 0,
  error_rate NUMERIC(5, 4) NOT NULL DEFAULT 0,
  cost_per_token NUMERIC(12, 8) NOT NULL DEFAULT 0,
  sample_count INT NOT NULL DEFAULT 0,
  pool TEXT NOT NULL DEFAULT 'common',
  provider_key_id UUID REFERENCES provider_keys(id),
  last_health_check_at TIMESTAMPTZ,
  error_rate_1h NUMERIC(6,4) NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(user_id, provider, model)
);
CREATE INDEX IF NOT EXISTS idx_pool_scores_user_id ON pool_scores(user_id);
CREATE INDEX IF NOT EXISTS idx_pool_scores_user_pool ON pool_scores(user_id, pool);

CREATE TABLE IF NOT EXISTS model_perf_stats (
  provider         TEXT NOT NULL,
  model_id         TEXT NOT NULL,
  total_requests   BIGINT NOT NULL DEFAULT 0,
  total_errors     BIGINT NOT NULL DEFAULT 0,
  total_latency_ms BIGINT NOT NULL DEFAULT 0,
  p50_latency_ms   INTEGER,
  p95_latency_ms   INTEGER,
  p99_latency_ms   INTEGER,
  min_latency_ms   INTEGER,
  max_latency_ms   INTEGER,
  last_updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (provider, model_id)
);

CREATE TABLE IF NOT EXISTS rate_limit_observations (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  provider    TEXT NOT NULL,
  model_id    TEXT NOT NULL,
  limit_rpm   INT,
  limit_tpm   INT,
  limit_tpd   INT,
  observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  source      TEXT NOT NULL DEFAULT 'header'
);
CREATE INDEX IF NOT EXISTS idx_rlo_provider_model ON rate_limit_observations(provider, model_id, observed_at DESC);
