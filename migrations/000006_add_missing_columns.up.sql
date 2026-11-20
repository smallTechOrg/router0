-- users: add status, invited_by
ALTER TABLE users ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE users ADD COLUMN IF NOT EXISTS invited_by UUID REFERENCES users(id);

-- invites: add accepted_at (alias for used_at semantics)
ALTER TABLE invites ADD COLUMN IF NOT EXISTS accepted_at TIMESTAMPTZ;
-- invited_by is same as created_by — add alias column
ALTER TABLE invites ADD COLUMN IF NOT EXISTS invited_by UUID REFERENCES users(id);

-- provider_keys: add missing columns
ALTER TABLE provider_keys ADD COLUMN IF NOT EXISTS key_hint TEXT NOT NULL DEFAULT '';
ALTER TABLE provider_keys ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE provider_keys ADD COLUMN IF NOT EXISTS last_test_result TEXT;
ALTER TABLE provider_keys ADD COLUMN IF NOT EXISTS model_id TEXT;

-- request_metadata: add missing columns  
ALTER TABLE request_metadata ADD COLUMN IF NOT EXISTS zer0_request_id TEXT;
ALTER TABLE request_metadata ADD COLUMN IF NOT EXISTS provider_key_id UUID REFERENCES provider_keys(id);
ALTER TABLE request_metadata ADD COLUMN IF NOT EXISTS pool TEXT NOT NULL DEFAULT 'active';
ALTER TABLE request_metadata ADD COLUMN IF NOT EXISTS retry_count INT NOT NULL DEFAULT 0;
ALTER TABLE request_metadata ADD COLUMN IF NOT EXISTS error_class TEXT;
ALTER TABLE request_metadata ADD COLUMN IF NOT EXISTS estimated_cost_usd NUMERIC(14,8) NOT NULL DEFAULT 0;

-- pool_scores: add missing columns
ALTER TABLE pool_scores ADD COLUMN IF NOT EXISTS provider_key_id UUID REFERENCES provider_keys(id);
ALTER TABLE pool_scores ADD COLUMN IF NOT EXISTS last_health_check_at TIMESTAMPTZ;
ALTER TABLE pool_scores ADD COLUMN IF NOT EXISTS error_rate_1h NUMERIC(6,4) NOT NULL DEFAULT 0;
