ALTER TABLE provider_models
    DROP COLUMN IF EXISTS probe_status,
    DROP COLUMN IF EXISTS last_probed_at;
