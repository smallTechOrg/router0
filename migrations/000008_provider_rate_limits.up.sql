-- Global per-provider, per-model rate limit metadata.
-- Source: 'published' (docs/known tier values) or 'observed' (inferred from response headers).
CREATE TABLE IF NOT EXISTS provider_rate_limits (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider    TEXT NOT NULL,
    model_id    TEXT NOT NULL,         -- '' means applies to all models for the provider
    plan        TEXT NOT NULL DEFAULT 'free',  -- 'free', 'paid', etc.
    limit_rpm   INT,
    limit_tpm   INT,
    limit_tpd   INT,
    limit_rpd   INT,
    source      TEXT NOT NULL DEFAULT 'published',  -- 'published' | 'observed'
    notes       TEXT,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (provider, model_id, plan)
);

-- Add score_breakdown column to provider_models so we can surface scoring factors
ALTER TABLE provider_models
    ADD COLUMN IF NOT EXISTS avg_latency_ms  INT,
    ADD COLUMN IF NOT EXISTS error_rate      NUMERIC(5,4),
    ADD COLUMN IF NOT EXISTS score           NUMERIC(6,4);

-- Seed known published rate limits
INSERT INTO provider_rate_limits (provider, model_id, plan, limit_rpm, limit_tpm, limit_tpd, limit_rpd, source, notes) VALUES
-- Groq free tier (https://console.groq.com/docs/rate-limits)
('groq', 'llama-3.3-70b-versatile',         'free', 30,   6000,   NULL, 1000, 'published', 'Groq free tier'),
('groq', 'llama-3.1-8b-instant',            'free', 30,  20000,   NULL, 14400,'published', 'Groq free tier'),
('groq', 'llama-3.1-70b-versatile',         'free', 30,   6000,   NULL, 1000, 'published', 'Groq free tier'),
('groq', 'mixtral-8x7b-32768',              'free', 30,   5000,   NULL, 14400,'published', 'Groq free tier'),
('groq', '',                                'free', 30,  NULL,   NULL, NULL,  'published', 'Groq free tier default'),
-- NVIDIA NIM free tier (https://build.nvidia.com/explore/discover)
('nvidia', '',                              'free', 40,  NULL,   NULL, NULL,  'published', 'NVIDIA NIM free tier; 1000 credits/month'),
('nvidia', 'meta/llama-3.1-8b-instruct',   'free', 40,  40000,  NULL, NULL,  'published', 'NVIDIA NIM free tier'),
('nvidia', 'meta/llama-3.1-70b-instruct',  'free', 10,  10000,  NULL, NULL,  'published', 'NVIDIA NIM free tier'),
('nvidia', 'nvidia/llama-3.1-nemotron-70b-instruct', 'free', 10, 10000, NULL, NULL, 'published', 'NVIDIA NIM free tier'),
-- OpenRouter (https://openrouter.ai/docs/limits)
('openrouter', '',                          'free', 20,  NULL,   NULL, 200,   'published', 'OpenRouter free tier'),
('openrouter', 'openai/gpt-4o-mini',       'free', 20,  NULL,   NULL, 200,   'published', 'OpenRouter free tier'),
-- Google AI (https://ai.google.dev/gemini-api/docs/rate-limits)
('google', 'gemini-2.0-flash',             'free', 15,  1000000,NULL, 1500,  'published', 'Google AI Studio free tier'),
('google', 'gemini-1.5-flash',             'free', 15,  1000000,NULL, 1500,  'published', 'Google AI Studio free tier'),
('google', 'gemini-1.5-pro',               'free', 2,   32000,  NULL, 50,    'published', 'Google AI Studio free tier'),
('google', '',                             'free', 15,  NULL,   NULL, NULL,  'published', 'Google AI Studio free tier default')
ON CONFLICT (provider, model_id, plan) DO UPDATE SET
    limit_rpm = EXCLUDED.limit_rpm,
    limit_tpm = EXCLUDED.limit_tpm,
    limit_tpd = EXCLUDED.limit_tpd,
    limit_rpd = EXCLUDED.limit_rpd,
    source = EXCLUDED.source,
    notes = EXCLUDED.notes,
    updated_at = NOW();
