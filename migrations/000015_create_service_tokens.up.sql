CREATE TABLE service_tokens (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id       UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  token_prefix TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  hash_alg     TEXT NOT NULL DEFAULT 'sha256' CHECK (hash_alg IN ('sha256','bcrypt')),
  created_by   UUID REFERENCES users(id) ON DELETE SET NULL,
  last_used_at TIMESTAMPTZ,
  revoked_at   TIMESTAMPTZ,
  revoked_by   UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX service_tokens_org_name_live ON service_tokens(org_id, name) WHERE revoked_at IS NULL;
CREATE INDEX service_tokens_prefix_legacy ON service_tokens(token_prefix) WHERE hash_alg = 'bcrypt';
CREATE INDEX service_tokens_org ON service_tokens(org_id, created_at DESC);

ALTER TABLE users ALTER COLUMN zer0_key_hash DROP NOT NULL,
                  ALTER COLUMN zer0_key_prefix DROP NOT NULL;

-- Backfill legacy per-user keys so existing zer0:sk- callers keep working.
-- Idempotent: skips orgs that already have a legacy-default token or a row with the same hash.
INSERT INTO service_tokens (org_id, name, token_prefix, token_hash, hash_alg, created_by)
SELECT m.org_id, 'legacy-default', u.zer0_key_prefix, u.zer0_key_hash, 'bcrypt', u.id
FROM users u
JOIN org_members m ON m.user_id = u.id
WHERE u.zer0_key_hash IS NOT NULL
  AND u.zer0_key_prefix IS NOT NULL
  AND u.zer0_key_hash <> '$2a$12$dummy'
  AND NOT EXISTS (
    SELECT 1 FROM service_tokens t
    WHERE (t.org_id = m.org_id AND t.name = 'legacy-default' AND t.revoked_at IS NULL)
       OR t.token_hash = u.zer0_key_hash
  );
