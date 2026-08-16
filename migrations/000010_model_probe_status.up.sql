-- Track whether each model has been successfully tested with a live API call.
-- Values: 'untested' (just discovered), 'probe_ok' (health probe succeeded), 'probe_error' (probe failed)
ALTER TABLE provider_models
    ADD COLUMN IF NOT EXISTS probe_status text NOT NULL DEFAULT 'untested',
    ADD COLUMN IF NOT EXISTS last_probed_at timestamptz;

-- Existing rows were discovered by a working key — mark them as untested (probe pending)
UPDATE provider_models SET probe_status = 'untested' WHERE probe_status = 'untested';
