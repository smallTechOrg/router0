# Capability: Service Tokens

Phase 1.

## What It Does

Lets an org issue many router0 API tokens (`r0-sk-…`), one per backend service, each named, independently revocable, and with a visible last-used time. Authenticates every `/v1/*` request and attaches the org and token to the request context.

## Inputs

| Input | Type | Source | Required |
|-------|------|--------|----------|
| Token name | string, 1–64 chars `[A-Za-z0-9._-]` | `POST /api/tokens` | Yes |
| Token id | UUID | `DELETE /api/tokens/{id}` | Yes |
| Bearer token | `r0-sk-<32 base58>` or legacy `zer0:sk-<32 base58>` | `Authorization` header on `/v1/*` | Yes |

## Outputs

| Output | Destination |
|--------|-------------|
| Full token (exactly once) | `POST /api/tokens` response |
| `service_tokens` row (sha256 hash + 12-char prefix) | PostgreSQL |
| `auth.Principal{OrgID, TokenID, TokenName}` | Request context |
| Token cache entry `sha256 → principal` (30 s TTL, LRU 10 000) | Process memory (`internal/cache`) |

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| PostgreSQL | SELECT by `token_hash`; legacy SELECT by `token_prefix`; UPDATE `last_used_at` | 503 `upstream_error/auth_unavailable` if DB unreachable and cache miss |

## Business Rules

- Assumed: the token scheme prefix is `r0-sk-` (from the brief), with `zer0:sk-` still accepted for legacy keys.
- Format: `r0-sk-` + 32 characters from the existing base58 alphabet (`auth/keygen.go`), from `crypto/rand`. Unbiased sampling (rejection sampling instead of the current `byte % 58`). Assumed: the modulo bias fix applies to new tokens only.
- Storage: `token_hash = hex(sha256(full token))`, `hash_alg = 'sha256'`, `token_prefix` = first 12 chars after `r0-sk-`. The display prefix in the UI is `r0-sk-` + `token_prefix`.
- Verification order: (1) in-process cache hit on `sha256(token)` → principal; (2) SELECT `service_tokens WHERE token_hash = $1 AND revoked_at IS NULL`; (3) if the token starts with `zer0:sk-`, SELECT rows with `hash_alg='bcrypt' AND token_prefix = <12 chars after zer0:sk->` and bcrypt-compare. Successful verification writes the cache entry. Legacy bcrypt results are cached the same way, keyed by sha256 of the presented token.
- Failure → 401 `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"Invalid or revoked router0 token"}}`, OpenAI shape. The same response for unknown, malformed, and revoked tokens (no existence oracle).
- Revocation sets `revoked_at`, `revoked_by` and evicts every cache entry for that token id from the local cache, so it takes effect on the next request on this instance. Assumed: other instances (if any) pick it up within the 30 s cache TTL, which is acceptable for a single-instance target.
- `last_used_at` is updated at most once per minute per token (guarded in-process), asynchronously.
- Names are unique among an org's live tokens; a revoked token's name can be reused.
- Tokens are never logged; logs carry `token_id` only.
- Legacy: migration 000015 copies every user's `zer0:sk-` key into `service_tokens` as `legacy-default` (`legacy: true` in the API). New legacy tokens are never created.

## Success Criteria

- [ ] `POST /api/tokens {"name":"svc-a"}` returns 201 with a `token` matching `^r0-sk-[1-9A-HJ-NP-Za-km-z]{32}$`; the DB row has a 64-hex-char `token_hash` and no plaintext.
- [ ] `GET /api/tokens` never contains a `token` field.
- [ ] A chat completion with the new token succeeds; one with `r0-sk-` + 32 random chars returns 401 `invalid_api_key`.
- [ ] After `DELETE /api/tokens/{id}`, the very next request with that token returns 401 (local cache invalidated, no 30 s wait).
- [ ] Unit: a cached principal expires after the TTL (injected clock) and the next call re-queries Postgres.
- [ ] Two tokens in one org both work concurrently; revoking one does not affect the other; request log rows carry the correct `token_name`.
- [ ] A legacy `zer0:sk-` key present before migration 000015 authenticates afterwards and is listed as `legacy-default`.
- [ ] Creating a second live token named `svc-a` returns 409 `token_name_exists`.
- [ ] Token verification with a cache hit does no DB query (unit test with a counting fake repository).
