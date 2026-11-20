-- Rows written by router0 (no user_id) cannot satisfy the restored NOT NULL; remove them.
DELETE FROM request_attempts WHERE user_id IS NULL;
ALTER TABLE request_attempts
  DROP COLUMN IF EXISTS provider_error_code,
  DROP COLUMN IF EXISTS retry_after_s,
  DROP COLUMN IF EXISTS skip_reason,
  DROP COLUMN IF EXISTS outcome,
  DROP COLUMN IF EXISTS provider_key_label,
  DROP COLUMN IF EXISTS org_id;
ALTER TABLE request_attempts ALTER COLUMN user_id SET NOT NULL;

DROP INDEX IF EXISTS request_metadata_request_id;
DROP INDEX IF EXISTS request_metadata_org_created;
DELETE FROM request_metadata WHERE user_id IS NULL;
ALTER TABLE request_metadata
  DROP COLUMN IF EXISTS stream,
  DROP COLUMN IF EXISTS error_code,
  DROP COLUMN IF EXISTS http_status,
  DROP COLUMN IF EXISTS attempt_count,
  DROP COLUMN IF EXISTS provider_key_label,
  DROP COLUMN IF EXISTS canonical_model,
  DROP COLUMN IF EXISTS service_token_id,
  DROP COLUMN IF EXISTS org_id;
ALTER TABLE request_metadata ALTER COLUMN user_id SET NOT NULL;
