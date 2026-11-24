-- Per-service-token model allowlist (canonical model names or alias names, lowercased).
ALTER TABLE service_tokens ADD COLUMN IF NOT EXISTS allowed_models TEXT[];   -- NULL = every model
