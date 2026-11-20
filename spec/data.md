# Data: router0

Existing migrations `000001`–`000013` are applied and are not edited. All changes are forward migrations numbered from `000014` (Phase 1: 000014–000019, Phase 2: 000020, Phase 3: 000021–000022). Each has an `.up.sql` and a `.down.sql`; down migrations drop what the up added (backfilled data in existing tables is left in place).

## Entity Relationships (after Phase 1)

```
orgs (1) ──< org_members >── (1) users
orgs (1) ──< service_tokens
orgs (1) ──< provider_keys          (position = key order within provider)
orgs (1) ──< org_provider_priority  (position = global provider order)
orgs (1) ──< org_model_catalog      (canonical_model → provider + provider_model_id)
orgs (1) ──< org_catalog_refreshes
provider_keys (1) ──< provider_key_state (cooldowns; Phase 1)
orgs (1) ──< request_metadata (1) ──< request_attempts   [joined on zer0_request_id]
orgs (1) ──< org_model_aliases      (Phase 2)
provider_keys (1) ──< provider_key_balances, provider_key_spend (Phase 3)
users (1) ──< invites [created_by]; invites.org_id → orgs (optional)
```

## Existing Entities (unchanged unless noted)

| Table | Status in router0 |
|-------|-------------------|
| `users` | Kept. `zer0_key_hash` / `zer0_key_prefix` become nullable (000015); new users get no per-user key. `role` = platform role (`user`/`admin`); org role lives in `org_members`. |
| `invites` | Kept. Gains `org_id`, `org_role` (000014). |
| `provider_keys` | Kept. Gains `org_id`, `position` (000016). `user_id` now means "created by". Soft delete via `status='disabled'` remains. |
| `request_metadata` | Kept. Gains org/token/canonical/trail columns (000018). `zer0_request_id` keeps its legacy name and stores `r0-…` ids. |
| `request_attempts` (000013) | Kept. Gains outcome/skip columns (000018). |
| `pool_scores`, `model_perf_stats`, `rate_limit_observations` | Legacy; no longer written on the request path; their writer code is deleted in Phase 1; tables dropped in Phase 3 (000022). |
| `provider_models`, `admin_provider_keys`, `provider_rate_limits` | Legacy global admin discovery used by `/dashboard/admin/models`; untouched; not used for routing. |

## Phase 1 Migrations

### 000014_create_orgs

```sql
CREATE TABLE orgs (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE org_members (
  org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (org_id, user_id)
);
CREATE UNIQUE INDEX org_members_one_org_per_user ON org_members(user_id);
ALTER TABLE invites ADD COLUMN org_id UUID REFERENCES orgs(id) ON DELETE CASCADE,
                    ADD COLUMN org_role TEXT CHECK (org_role IN ('admin','member'));
-- Backfill: one org per existing user, user is owner.
-- (PL/pgSQL DO block: for each user, INSERT orgs(name = users.email) RETURNING id; INSERT org_members(org_id, user_id, 'owner'))
```

Assumed: one org per user in Phase 1–3 (enforced by the unique index on `org_members.user_id`). Multi-org membership would drop that index and add an org switcher; not planned.

### 000015_create_service_tokens

```sql
CREATE TABLE service_tokens (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id       UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,                       -- e.g. "billing-worker"
  token_prefix TEXT NOT NULL,                       -- first 12 chars after the scheme prefix; display + legacy lookup
  token_hash   TEXT NOT NULL UNIQUE,                -- sha256 hex (new) or bcrypt (legacy)
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
-- Backfill legacy keys so existing zer0:sk- callers keep working:
INSERT INTO service_tokens (org_id, name, token_prefix, token_hash, hash_alg, created_by)
SELECT m.org_id, 'legacy-default', u.zer0_key_prefix, u.zer0_key_hash, 'bcrypt', u.id
FROM users u JOIN org_members m ON m.user_id = u.id
WHERE u.zer0_key_hash IS NOT NULL AND u.zer0_key_hash <> '$2a$12$dummy';
```

Assumed: existing `zer0:sk-` keys are migrated into `service_tokens` as `legacy-default` (bcrypt) and keep working until revoked in the dashboard; no new bcrypt tokens are ever created.

### 000016_org_provider_routing

```sql
ALTER TABLE provider_keys ADD COLUMN org_id UUID REFERENCES orgs(id) ON DELETE CASCADE,
                          ADD COLUMN position INT NOT NULL DEFAULT 0;
UPDATE provider_keys k SET org_id = m.org_id FROM org_members m WHERE m.user_id = k.user_id;
ALTER TABLE provider_keys ALTER COLUMN org_id SET NOT NULL;
-- position backfill: row_number() OVER (PARTITION BY org_id, provider ORDER BY created_at) - 1
CREATE INDEX provider_keys_org_provider_pos ON provider_keys(org_id, provider, position);
CREATE UNIQUE INDEX provider_keys_org_provider_label ON provider_keys(org_id, provider, label) WHERE status <> 'disabled';

CREATE TABLE org_provider_priority (
  org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  provider   TEXT NOT NULL,
  position   INT  NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (org_id, provider),
  UNIQUE (org_id, position)
);
-- Backfill: per org, distinct providers of non-disabled keys, alphabetical, positions 0..n-1.
```

Rules:
- Priority and key order are replaced as a whole by `PUT /api/routing/priority` in one transaction (DELETE then INSERT for priority; UPDATE positions for keys), so the unique constraint never conflicts mid-update.
- A provider that has keys but no priority row is appended after ranked providers in alphabetical order. Assumed.
- A newly added key gets `position = max(position)+1` within its org+provider.

### 000017_org_model_catalog

```sql
CREATE TABLE org_model_catalog (
  id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id               UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  canonical_model      TEXT NOT NULL,
  provider             TEXT NOT NULL,
  provider_model_id    TEXT NOT NULL,
  kind                 TEXT NOT NULL DEFAULT 'chat' CHECK (kind IN ('chat','embedding')),
  source               TEXT NOT NULL DEFAULT 'discovered' CHECK (source IN ('discovered','on_demand','manual')),
  context_window       INT,
  input_cost_per_mtok  NUMERIC(20,8),
  output_cost_per_mtok NUMERIC(20,8),
  is_available         BOOLEAN NOT NULL DEFAULT true,
  first_seen_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_seen_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (org_id, provider, provider_model_id)
);
CREATE INDEX org_model_catalog_lookup ON org_model_catalog(org_id, canonical_model) WHERE is_available;

CREATE TABLE org_catalog_refreshes (
  org_id            UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  provider          TEXT NOT NULL,
  last_refreshed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_status       TEXT NOT NULL,          -- ok | error
  last_error_class  TEXT,                   -- providers.ErrorClass on error
  model_count       INT NOT NULL DEFAULT 0,
  PRIMARY KEY (org_id, provider)
);
```

Rules:
- A discovery run for (org, provider) upserts every listed model (`is_available = true, last_seen_at = NOW()`) and sets `is_available = false` for that org+provider's rows not in the listing.
- The catalog is per org because model visibility depends on the key (e.g. OpenRouter free vs paid, Groq org-specific models).
- When an attempt returns `model_unavailable`, that single row is set `is_available = false` until the next discovery run.

### 000018_request_log_org

```sql
ALTER TABLE request_metadata
  ALTER COLUMN user_id DROP NOT NULL,
  ADD COLUMN org_id             UUID REFERENCES orgs(id) ON DELETE CASCADE,
  ADD COLUMN service_token_id   UUID REFERENCES service_tokens(id) ON DELETE SET NULL,
  ADD COLUMN canonical_model    TEXT,
  ADD COLUMN provider_key_label TEXT,
  ADD COLUMN attempt_count      SMALLINT NOT NULL DEFAULT 0,
  ADD COLUMN http_status        SMALLINT,
  ADD COLUMN error_code         TEXT,           -- final router0 code: rate_limited, insufficient_credits, ...
  ADD COLUMN stream             BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX request_metadata_org_created ON request_metadata(org_id, created_at DESC);
CREATE UNIQUE INDEX request_metadata_request_id ON request_metadata(zer0_request_id) WHERE zer0_request_id IS NOT NULL;

ALTER TABLE request_attempts
  ALTER COLUMN user_id DROP NOT NULL,
  ADD COLUMN org_id              UUID REFERENCES orgs(id) ON DELETE CASCADE,
  ADD COLUMN provider_key_label  TEXT,
  ADD COLUMN outcome             TEXT NOT NULL DEFAULT 'error' CHECK (outcome IN ('success','error','skipped')),
  ADD COLUMN skip_reason         TEXT,          -- rate_limited | credits_exhausted | transient_cooldown | budget_exhausted
  ADD COLUMN retry_after_s       INT,
  ADD COLUMN provider_error_code TEXT;          -- provider's structured code/type, ≤200 chars
UPDATE request_attempts SET outcome = CASE WHEN status = 'success' THEN 'success' ELSE 'error' END;
```

Column meanings in router0:
- `request_metadata.provider` / `model` = winning provider and its `provider_model_id` (`'none'` when all failed); `canonical_model` = resolved canonical name (or the raw requested name when not found).
- `request_metadata.status` = `success` | `error`.
- `request_attempts.model` = `provider_model_id`; `error_class` = `providers.ErrorClass`; `error_message` = provider error code/type only (never body text).
- Retention: indefinite. Assumed; a TTL archival job is out of scope.

### 000019_provider_key_state

Holds per-key cooldown / exhausted state. Postgres is the source of truth; `internal/keystate` keeps a 2 s write-through in-memory read cache in front of it.

```sql
CREATE TABLE provider_key_state (
  provider_key_id   UUID NOT NULL REFERENCES provider_keys(id) ON DELETE CASCADE,
  org_id            UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  kind              TEXT NOT NULL CHECK (kind IN ('rate_limited','credits_exhausted','transient_cooldown','budget_exhausted')),
  scope_model       TEXT NOT NULL DEFAULT '',   -- provider_model_id for rate_limited; '' = whole key
  until_at          TIMESTAMPTZ NOT NULL,       -- cooldown_until / exhausted_until
  reason            TEXT,                       -- provider_429, provider_402, provider_code:<code>, balance_poll, budget
  last_error_class  TEXT,                       -- providers.ErrorClass that caused it
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (provider_key_id, kind, scope_model)
);
CREATE INDEX provider_key_state_until ON provider_key_state(until_at);
```

Rules:
- `Mark` = `INSERT … ON CONFLICT (provider_key_id, kind, scope_model) DO UPDATE SET until_at = GREATEST(provider_key_state.until_at, EXCLUDED.until_at), reason = EXCLUDED.reason, last_error_class = EXCLUDED.last_error_class, updated_at = NOW()` (PG 9.5+), then update the local cache.
- `CheckMany` = one `SELECT … WHERE provider_key_id = ANY($1) AND until_at > NOW()` per request (cache hit → no query).
- A row is active only while `until_at > NOW()`; expired rows are ignored and deleted by `keystate.Sweep` (`DELETE … WHERE until_at < NOW() - INTERVAL '1 hour'`), which `main.go` runs hourly.
- Rate-limit state is per key **and model**, because providers such as Groq limit per model. Credits and transient states are per key (`scope_model = ''`).
- Auth failures are not stored here. They set `provider_keys.status = 'error'`, `last_test_result = 'auth_error'`, which removes the key from plans until a successful `POST /api/keys/{id}/test`.
- (P3) Reset-state = `DELETE FROM provider_key_state WHERE provider_key_id = $1` + cache invalidation.

## Phase 2 Migration

### 000020_org_model_aliases

```sql
CREATE TABLE org_model_aliases (
  org_id          UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  alias           TEXT NOT NULL,          -- stored lowercased, e.g. "fast", "smart", "gpt-4o"
  canonical_model TEXT NOT NULL,
  created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (org_id, alias)
);
```

Phase 2 also rewrites nothing in existing data; Gemini keeps provider id `google` (see provider-adapters.md).

## Phase 3 Migrations

### 000021_key_budgets_balances

```sql
ALTER TABLE provider_keys ADD COLUMN monthly_budget_usd NUMERIC(12,4);   -- NULL = no budget
CREATE TABLE provider_key_balances (
  provider_key_id UUID PRIMARY KEY REFERENCES provider_keys(id) ON DELETE CASCADE,
  org_id          UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  remaining_usd   NUMERIC(14,6),          -- NULL = provider reports unlimited
  limit_usd       NUMERIC(14,6),
  usage_usd       NUMERIC(14,6),
  status          TEXT NOT NULL,          -- ok | exhausted | error | unsupported
  polled_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE provider_key_spend (
  provider_key_id UUID NOT NULL REFERENCES provider_keys(id) ON DELETE CASCADE,
  month           DATE NOT NULL,          -- first day of the UTC month
  spend_micro_usd BIGINT NOT NULL DEFAULT 0,
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (provider_key_id, month)
);
ALTER TABLE request_metadata ADD COLUMN endpoint TEXT NOT NULL DEFAULT 'chat' CHECK (endpoint IN ('chat','embeddings'));
```

### 000022_drop_legacy_pool

Drops `pool_scores`, `model_perf_stats`, `rate_limit_observations` (legacy code that wrote them is deleted in Phase 1). Down migration recreates them empty.

## PostgreSQL Compatibility

Target: PostgreSQL 14 (developer's local Homebrew `postgresql@14`) and newer. All existing migrations (000001–000013) and every planned one (000014–000022) were checked. They use only: `gen_random_uuid()` (core since PG 13), partial and unique indexes, `CHECK`, `ON CONFLICT`, `ADD COLUMN IF NOT EXISTS`, `DO $$ … $$` PL/pgSQL blocks, and window functions. No PG 15+ features are used (`MERGE`, `UNIQUE NULLS NOT DISTINCT`, SQL/JSON `JSON_TABLE`/`IS JSON`, `security_invoker` views). Nothing was flagged. Rule for future migrations: do not use PG 15+ syntax.

## In-Memory State (per process, not persisted)

| State | Where | TTL / bound | Phase |
|-------|-------|-------------|-------|
| Service-token cache `sha256 → {org_id, token_id, name}` | `internal/tokens` (uses `internal/cache`) | 30 s TTL, 10 000 entries LRU; entry deleted immediately on revoke by this process | 1 |
| Key-state read cache | `internal/keystate` | 2 s TTL; write-through on `Mark` | 1 |
| Catalog refresh mutex per (org, provider) | `internal/catalog` | held for the refresh (≤ 30 s); skips if already running | 1 |
| `last_used_at` write throttle per token | `internal/tokens` | 1 min | 1 |
| On-demand miss debounce per (org, canonical) | `internal/catalog` | 60 s | 2 |
| Month spend cache per key | `internal/keystate` | 5 s; source of truth `provider_key_spend` | 3 |

Assumed: single instance is the deployment target. With several instances, Postgres stays correct. In-memory staleness is bounded by the TTLs above: key state ≤ 2 s; token revocation ≤ 30 s on instances other than the one that handled the revoke. Two instances may refresh the same catalog concurrently; the upserts are idempotent.

The legacy pool-router rate-limit counters and pool state are dropped, not ported. Fixed-priority routing does not need them; anything that must persist (cooldowns, spend) is in Postgres.

## Data Lifecycle

- **Org:** created at registration (invite without `org_id`) with the registrant as `owner`. Never deleted through the UI.
- **Service token:** created by an org owner/admin; full token shown once; revocation sets `revoked_at` and evicts the token from this process's cache, so it takes effect on the next request on this instance and within 30 s on any other instance.
- **Provider key:** created/disabled by org owner/admin; disabling removes it from plans immediately (plans are built per request).
- **Catalog rows:** written by discovery (on key add, on `POST /api/catalog/refresh`, every 6 h, and in Phase 2 on demand); flagged unavailable, never hard-deleted.
- **Request metadata / attempts:** insert-only, async after the response completes; never contain prompt or response content.

## Cost Estimation

`estimated_cost_usd` = tokens × the catalog row's `input_cost_per_mtok` / `output_cost_per_mtok` when present (OpenRouter `/models` returns pricing; Groq uses the static `groqPricing` table already in `groq.go`), else the existing `metadata.EstimateCost` table, else 0. For streaming, adapters send `stream_options: {"include_usage": true}` so token counts come from the provider's final usage chunk; the 4-chars-per-token approximation is used only when the provider sends no usage.
