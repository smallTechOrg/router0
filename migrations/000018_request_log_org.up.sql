ALTER TABLE request_metadata
  ALTER COLUMN user_id DROP NOT NULL,
  ADD COLUMN org_id             UUID REFERENCES orgs(id) ON DELETE CASCADE,
  ADD COLUMN service_token_id   UUID REFERENCES service_tokens(id) ON DELETE SET NULL,
  ADD COLUMN canonical_model    TEXT,
  ADD COLUMN provider_key_label TEXT,
  ADD COLUMN attempt_count      SMALLINT NOT NULL DEFAULT 0,
  ADD COLUMN http_status        SMALLINT,
  ADD COLUMN error_code         TEXT,
  ADD COLUMN stream             BOOLEAN NOT NULL DEFAULT false;

-- Legacy rows may contain duplicate request ids; keep the earliest, clear the rest
-- so the unique index can be created.
UPDATE request_metadata rm SET zer0_request_id = NULL
FROM (
  SELECT id, row_number() OVER (PARTITION BY zer0_request_id ORDER BY created_at, id) AS rn
  FROM request_metadata
  WHERE zer0_request_id IS NOT NULL
) d
WHERE d.id = rm.id AND d.rn > 1;

CREATE INDEX request_metadata_org_created ON request_metadata(org_id, created_at DESC);
CREATE UNIQUE INDEX request_metadata_request_id ON request_metadata(zer0_request_id) WHERE zer0_request_id IS NOT NULL;

ALTER TABLE request_attempts
  ALTER COLUMN user_id DROP NOT NULL,
  ADD COLUMN org_id              UUID REFERENCES orgs(id) ON DELETE CASCADE,
  ADD COLUMN provider_key_label  TEXT,
  ADD COLUMN outcome             TEXT NOT NULL DEFAULT 'error' CHECK (outcome IN ('success','error','skipped')),
  ADD COLUMN skip_reason         TEXT,
  ADD COLUMN retry_after_s       INT,
  ADD COLUMN provider_error_code TEXT;

UPDATE request_attempts SET outcome = CASE WHEN status = 'success' THEN 'success' ELSE 'error' END;
