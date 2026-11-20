-- Restore NOT NULL on users key columns; rows without a key get the same placeholders the app used before.
UPDATE users SET zer0_key_hash = '$2a$12$dummy' WHERE zer0_key_hash IS NULL;
UPDATE users SET zer0_key_prefix = 'none' WHERE zer0_key_prefix IS NULL;
ALTER TABLE users ALTER COLUMN zer0_key_hash SET NOT NULL,
                  ALTER COLUMN zer0_key_prefix SET NOT NULL;
DROP INDEX IF EXISTS service_tokens_org;
DROP INDEX IF EXISTS service_tokens_prefix_legacy;
DROP INDEX IF EXISTS service_tokens_org_name_live;
DROP TABLE IF EXISTS service_tokens;
