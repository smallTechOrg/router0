CREATE TABLE IF NOT EXISTS request_attempts (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    zer0_request_id text NOT NULL,
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider       text NOT NULL,
    model          text NOT NULL,
    provider_key_id uuid REFERENCES provider_keys(id),
    attempt_order  smallint NOT NULL DEFAULT 0,
    status         text NOT NULL,          -- 'success', 'error'
    error_class    text,                   -- 'rate_limit', 'auth_error', 'upstream', 'timeout', 'unavailable'
    error_message  text,
    http_status    smallint,
    latency_ms     integer NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_request_attempts_request ON request_attempts(zer0_request_id);
CREATE INDEX idx_request_attempts_provider_model ON request_attempts(provider, model, created_at DESC);
CREATE INDEX idx_request_attempts_error ON request_attempts(error_class, created_at DESC) WHERE error_class IS NOT NULL;
