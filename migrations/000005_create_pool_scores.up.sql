CREATE TABLE pool_scores (
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
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(user_id, provider, model)
);
CREATE INDEX idx_pool_scores_user_id ON pool_scores(user_id);
CREATE INDEX idx_pool_scores_user_pool ON pool_scores(user_id, pool);
