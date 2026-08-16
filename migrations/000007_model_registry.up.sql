-- Model registry: discovered models per provider with metadata
CREATE TABLE provider_models (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  provider     TEXT NOT NULL,
  model_id     TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  context_window      INT NOT NULL DEFAULT 0,
  max_output_tokens   INT NOT NULL DEFAULT 0,
  input_cost_per_mtok  NUMERIC(14,8) NOT NULL DEFAULT 0,
  output_cost_per_mtok NUMERIC(14,8) NOT NULL DEFAULT 0,
  supports_streaming  BOOLEAN NOT NULL DEFAULT true,
  is_available        BOOLEAN NOT NULL DEFAULT true,
  extra               JSONB NOT NULL DEFAULT '{}',
  discovered_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(provider, model_id)
);
CREATE INDEX idx_provider_models_provider ON provider_models(provider);
CREATE INDEX idx_provider_models_available ON provider_models(provider, is_available);

-- Admin-owned provider keys for discovery and enrichment (separate from user keys)
CREATE TABLE admin_provider_keys (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  provider   TEXT NOT NULL UNIQUE,
  ciphertext BYTEA NOT NULL,
  nonce      BYTEA NOT NULL,
  label      TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'active',
  last_discovery_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Rate limit observations learned from response headers
CREATE TABLE rate_limit_observations (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  provider     TEXT NOT NULL,
  model_id     TEXT NOT NULL,
  limit_rpm    INT,
  limit_tpm    INT,
  limit_tpd    INT,
  observed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  source       TEXT NOT NULL DEFAULT 'header'
);
CREATE INDEX idx_rlo_provider_model ON rate_limit_observations(provider, model_id, observed_at DESC);
