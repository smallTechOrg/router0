CREATE TABLE IF NOT EXISTS org_model_aliases (
  org_id          UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  alias           TEXT NOT NULL,          -- stored lowercased, e.g. "fast", "smart", "gpt-4o"
  canonical_model TEXT NOT NULL,
  created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (org_id, alias)
);

-- Widen org_model_catalog.kind so non-chat models can be stored and filtered.
DO $$ DECLARE c TEXT; BEGIN
  FOR c IN SELECT conname FROM pg_constraint
   WHERE conrelid = 'org_model_catalog'::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%kind%'
  LOOP
    EXECUTE format('ALTER TABLE org_model_catalog DROP CONSTRAINT %I', c);
  END LOOP;
END $$;
ALTER TABLE org_model_catalog ADD CONSTRAINT org_model_catalog_kind_check
  CHECK (kind IN ('chat','embedding','image','audio','other'));
