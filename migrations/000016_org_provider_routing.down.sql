DROP TABLE IF EXISTS org_provider_priority;
DROP INDEX IF EXISTS provider_keys_org_provider_label;
DROP INDEX IF EXISTS provider_keys_org_provider_pos;
ALTER TABLE provider_keys DROP COLUMN IF EXISTS position,
                          DROP COLUMN IF EXISTS org_id;
