DROP TABLE IF EXISTS provider_rate_limits;
ALTER TABLE provider_models DROP COLUMN IF EXISTS avg_latency_ms;
ALTER TABLE provider_models DROP COLUMN IF EXISTS error_rate;
ALTER TABLE provider_models DROP COLUMN IF EXISTS score;
