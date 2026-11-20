# Capability: Provider Key Management (Keys, Key Order, Provider Priority)

Phase 1. Budgets, reset-state, and drag-and-drop polish are Phase 3.

## What It Does

Org owners/admins add their own provider API keys (BYOK, several per provider), order keys inside each provider, and set one global provider priority for the org. Keys are AES-256-GCM encrypted at rest and decrypted only in memory for an attempt. The dashboard shows each key's live state (rate-limited, out of credits, error).

## Inputs

| Input | Type | Source | Required |
|-------|------|--------|----------|
| Provider id | one of the registered adapters | `POST /api/keys` | Yes |
| Raw key | string | `POST /api/keys` | Yes |
| Label | string ≤ 64 chars | `POST /api/keys` | No (default `<provider>-<n>`) |
| Provider order, key order | arrays of provider ids / key ids | `PUT /api/routing/priority` | Yes |
| Monthly budget USD (P3) | decimal ≥ 0 or null | `PATCH /api/keys/{id}` | No |

## Outputs

| Output | Destination |
|--------|-------------|
| `provider_keys` row (`org_id`, `position`, ciphertext, nonce, `key_hint`) | PostgreSQL |
| `org_provider_priority` rows | PostgreSQL |
| Catalog discovery trigger for (org, provider) | catalog.Discoverer (async) |
| Key list with `state` | `GET /api/keys` |

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| PostgreSQL | CRUD keys; transactional priority replace | 500; never log key material |
| Provider API | `ListModels` for `POST /api/keys/{id}/test` | 200 with `{"status":"error","class":…}` |
| PostgreSQL | Read `provider_key_state` via keystate for `GET /api/keys` | `state: null` (page still renders) |

## Business Rules

- All queries are scoped by `org_id` from the JWT context. The existing `user_id` column is kept as "created by".
- Encryption is unchanged: AES-256-GCM, `AES_KEY` = 64 hex chars, unique 12-byte nonce per row, and the process refuses to start without a valid key.
- `key_hint` = first 8 chars + `…`, stored at write time. Raw keys are never returned.
- **Key order:** `provider_keys.position` (0 = first) within (org, provider). A new key goes last. Disabled keys keep their position but are excluded from plans and from priority views.
- **Provider priority:** `org_provider_priority.position` (0 = first). Adding the first key of a new provider appends that provider. Providers with keys but no row are appended alphabetically when building plans.
- `PUT /api/routing/priority` validates that it got full permutations, then replaces priority and updates key positions in one transaction. Plans are built per request from the DB, so changes apply to the next request. Assumed: no caching of priority. It is one indexed query per request; a 5-second in-process cache may be added if load requires it.
- **Statuses:** `active`; `error` (auth failure from live traffic or a failed test; excluded from plans); `disabled` (soft-deleted). A successful `POST /api/keys/{id}/test` sets `error` → `active`.
- `POST /api/keys/{id}/test` uses `ListModels`, which costs no tokens, instead of a 1-token completion. On success it also refreshes that provider's catalog for the org.
- `state` in `GET /api/keys` comes from keystate. `rate_limited` lists the affected `provider_model_id`s with the largest remaining TTL. `credits_exhausted` shows the TTL. `transient_cooldown` shows the TTL.
- (P3) `monthly_budget_usd`: see credit-tracking.md. (P3) `POST /api/keys/{id}/reset-state` deletes the key's `provider_key_state` rows.
- Valid providers come from `providers.ValidProviders()`, which is derived from the registry and no longer a hard-coded list.

## Success Criteria

- [ ] Adding two Groq keys gives them positions 0 and 1; `GET /api/routing/priority` lists `groq` with both keys in that order.
- [ ] `PUT /api/routing/priority {"providers":["openrouter","groq"],"key_order":{"groq":[k2,k1]}}` returns 200, and the next chat completion attempts OpenRouter first (visible in the trail).
- [ ] A `PUT` that omits a provider or includes a foreign key id returns 422 and changes nothing.
- [ ] `DELETE /api/keys/{id}` makes the key disappear from the next request's plan.
- [ ] After a mock 429 on key k1, `GET /api/keys` shows k1 `state.reason = "rate_limited"` with `retry_after_s > 0`.
- [ ] After a mock 401 on key k1 during a request, k1 has `status = "error"` and is not attempted on the next request; `POST /api/keys/{id}/test` against a mock that now returns 200 sets it back to `active`.
- [ ] A member-role user gets 403 on `POST /api/keys` and `PUT /api/routing/priority`.
- [ ] No API response or log line contains the raw key (integration test greps responses for the raw mock key string).
