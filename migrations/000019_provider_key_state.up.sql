CREATE TABLE provider_key_state (
  provider_key_id   UUID NOT NULL REFERENCES provider_keys(id) ON DELETE CASCADE,
  org_id            UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  kind              TEXT NOT NULL CHECK (kind IN ('rate_limited','credits_exhausted','transient_cooldown','budget_exhausted')),
  scope_model       TEXT NOT NULL DEFAULT '',
  until_at          TIMESTAMPTZ NOT NULL,
  reason            TEXT,
  last_error_class  TEXT,
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (provider_key_id, kind, scope_model)
);
CREATE INDEX provider_key_state_until ON provider_key_state(until_at);
