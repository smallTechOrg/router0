-- Phase 3: the legacy pool/scorer tables have had no writers since Phase 1.
DROP TABLE IF EXISTS pool_scores;
DROP TABLE IF EXISTS model_perf_stats;
DROP TABLE IF EXISTS rate_limit_observations;
