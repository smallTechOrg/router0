CREATE TABLE org_model_catalog (
  id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id               UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  canonical_model      TEXT NOT NULL,
  provider             TEXT NOT NULL,
  provider_model_id    TEXT NOT NULL,
  kind                 TEXT NOT NULL DEFAULT 'chat' CHECK (kind IN ('chat','embedding')),
  source               TEXT NOT NULL DEFAULT 'discovered' CHECK (source IN ('discovered','on_demand','manual')),
  context_window       INT,
  input_cost_per_mtok  NUMERIC(20,8),
  output_cost_per_mtok NUMERIC(20,8),
  is_available         BOOLEAN NOT NULL DEFAULT true,
  first_seen_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_seen_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (org_id, provider, provider_model_id)
);
CREATE INDEX org_model_catalog_lookup ON org_model_catalog(org_id, canonical_model) WHERE is_available;

CREATE TABLE org_catalog_refreshes (
  org_id            UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  provider          TEXT NOT NULL,
  last_refreshed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_status       TEXT NOT NULL,
  last_error_class  TEXT,
  model_count       INT NOT NULL DEFAULT 0,
  PRIMARY KEY (org_id, provider)
);
