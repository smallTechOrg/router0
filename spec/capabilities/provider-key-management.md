# Capability: Provider Key Management

## What It Does

Allows authenticated users to add, remove, and test their LLM provider API keys. Keys are stored AES-256-GCM encrypted in PostgreSQL and decrypted only in memory at request time. A basic liveness probe can be triggered manually from the dashboard.

## Inputs

| Input | Type | Source | Required |
|-------|------|---------|----------|
| Provider identifier | Enum (google \| groq \| openrouter \| nvidia \| together \| baseten) | API request body | Yes |
| Raw provider API key | String | API request body | Yes |
| Label | String | API request body | No (defaults to provider name) |
| Key ID | UUID | URL path parameter | Yes (delete / test) |

## Outputs

| Output | Type | Destination |
|--------|------|-------------|
| Key record (sans ciphertext) | JSON object | HTTP response body |
| Encrypted key ciphertext + nonce | BYTEA pair | PostgreSQL `provider_keys` |
| Probe result (latency, status) | JSON | HTTP response body (test endpoint) |

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| PostgreSQL | INSERT / SELECT / DELETE `provider_keys` | Return 500; log error (never log key material) |
| Provider API | Minimal 1-token probe on `POST /api/keys/:id/test` | Return `{"status":"error","message":"<provider error code>"}` |

## Business Rules

- **Encryption scheme:** AES-256-GCM. The encryption key (`AES_KEY`) is a 32-byte value set via environment variable and never stored in the database. Each provider key record stores a unique 12-byte random nonce alongside the ciphertext. Decryption happens in-process at request dispatch time; the plaintext key is never written to disk, logs, or any external system.
- The raw provider API key is never returned after storage. The `GET /api/keys` response returns `id`, `provider`, `label`, `status`, `created_at`, and a redacted key hint (first 8 chars + `…`) derived at write time — the hint is stored as a plain text column, not derived from the ciphertext.
- A user may have multiple keys per provider (e.g. two Groq keys). Each key participates in routing independently. There is no enforced maximum in Phase 1.
- Key status values: `active` (healthy, in-pool), `error` (last probe or live-traffic response indicated a hard error), `disabled` (manually removed from pool by user or admin, key record retained).
- Deleting a key (`DELETE /api/keys/:id`) sets `status = 'disabled'` and removes it from the Redis pool structures. The encrypted record is retained for audit/cost history. Hard deletion is not exposed in Phase 1.
- `POST /api/keys/:id/test` fires a minimal-token probe (see Provider Adapters capability for probe request format) and returns the HTTP status + latency from the provider. It does NOT update the pool score — that is the Routing Engine's responsibility.
- Keys belonging to a user whose account is disabled are not loaded for any request.
- The AES_KEY environment variable must be exactly 32 bytes (hex-encoded 64 chars or raw 32 bytes depending on implementation — see Architecture Conventions). If missing at startup, the process must refuse to start with a clear error message.
- Provider key records are scoped to the owning user (`user_id` FK). A user can only read, test, or delete their own keys. Admin users can disable any key via the Admin API (Phase 2).

## Success Criteria

- [ ] `POST /api/keys` with a valid Groq API key stores an encrypted ciphertext in PostgreSQL and returns HTTP 201 with a redacted key hint, never the raw key.
- [ ] The stored `key_ciphertext` cannot be decrypted without the correct `AES_KEY` — verified by attempting decryption with a wrong key and confirming failure.
- [ ] `GET /api/keys` returns all of the authenticated user's key records with `key_hint` populated and no ciphertext field.
- [ ] `DELETE /api/keys/:id` sets the key's status to `disabled`; subsequent inference requests do not use that key (verified via `X-Zer0-Provider` / `X-Zer0-Model` response headers on a follow-up request).
- [ ] `POST /api/keys/:id/test` with a valid Groq key returns HTTP 200 and `{"status":"ok","latency_ms":<N>}` where N > 0 within 10 seconds.
- [ ] `POST /api/keys/:id/test` with an intentionally invalid key returns HTTP 200 (the test endpoint itself succeeds) with `{"status":"error","message":"<provider error>"}`.
- [ ] A key belonging to another user returns HTTP 404 when accessed by a different authenticated user (not 403 — do not leak existence).
- [ ] Process startup with a missing or malformed `AES_KEY` env var exits immediately with a non-zero code and a clear log message.
