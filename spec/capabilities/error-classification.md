# Capability: Error Classification & Key State

Phase 1 (generic rules, Groq, OpenRouter, Postgres key states, final error precedence). Phase 2 adds Anthropic, Gemini, and Mesh API rules. Phase 3 adds `budget_exhausted` and balance-poll states (credit-tracking.md).

## What It Does

Turns every provider failure into one of six classes, records a time-boxed state on the key (or key + model) in Postgres (`provider_key_state`, behind a 2 s in-memory cache) so later requests skip it, and, once a request has tried everything, picks the one error to return to the caller.

## Inputs

| Input | Source |
|-------|--------|
| HTTP status, headers, body (≤ 64 KiB read) of a failed provider response; network/timeout errors; SSE error events before first byte | Adapters |
| Attempt records of a finished request | Routing executor |

## Outputs

| Output | Destination |
|--------|-------------|
| `*providers.ProviderError{Class, HTTP, Code, RetryAfter, Message}` | Routing executor |
| `provider_key_state` rows (data.md › 000019) | keystate |
| `routing.GatewayError{HTTP, Type, Code, Message, RetryAfterSeconds, Attempts}` | Inference handler |

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| PostgreSQL | `INSERT … ON CONFLICT DO UPDATE` (Mark); `SELECT … WHERE provider_key_id = ANY($1) AND until_at > NOW()` (CheckMany) | Mark: log `warn`, continue. Check: fail open (not blocked) |

## Business Rules

### Classes (`providers.ErrorClass`)

| Class | Meaning | Key-state effect |
|-------|---------|------------------|
| `rate_limit` | Throttled; will recover by itself | row `kind='rate_limited', scope_model=<provider_model_id>`, `until_at = now + cooldown` |
| `insufficient_credits` | Balance, quota, or spend limit exhausted; needs a human | row `kind='credits_exhausted'`, key-wide, for `ROUTER0_CREDITS_COOLDOWN_SECONDS` (3600) |
| `auth` | Key invalid, revoked, or lacks permission | `provider_keys.status='error'`, `last_test_result='auth_error'` (on the key row, not in `provider_key_state`) |
| `model_unavailable` | This provider/key cannot serve this model | Catalog row `is_available=false`; no key state |
| `bad_request` | Provider rejected the request itself (400/413/422, moderation) | None |
| `transient` | 5xx, overload, timeout, network, decrypt failure | row `kind='transient_cooldown'`, key-wide, for `ROUTER0_TRANSIENT_COOLDOWN_SECONDS` (10). The key is deprioritised, not skipped |

### Generic rules (`providers/classify.go`, OpenAI-style APIs), first match wins

1. Transport error, connection reset, or the per-attempt timeout → `transient`. (A cancelled client context is not classified; see routing-engine.md.)
2. Body `error.code` or `error.type` ∈ {`insufficient_quota`, `insufficient_credits`, `billing_hard_limit_reached`, `credit_limit_exceeded`, `payment_required`, `quota_exceeded`} → `insufficient_credits`, whatever the HTTP status.
3. HTTP 402 → `insufficient_credits`.
4. HTTP 429 → `rate_limit`.
5. HTTP 401, 403 → `auth`.
6. HTTP 404, or body code `model_not_found` → `model_unavailable`.
7. HTTP 408, 409, 425, 500–599 → `transient`.
8. HTTP 400, 413, 422 → `bad_request`.
9. Anything else → `transient`.

`Code` = the provider's structured `error.code`/`error.type` string (≤ 200 chars). `Message` = the provider's `error.message` (kept in memory for a possible `400 upstream_rejected_request` response, never persisted).

### Cooldown duration for `rate_limit`

First available, clamped to [1 s, 3600 s]:
1. `Retry-After` header: integer seconds or an HTTP-date.
2. Provider reset headers: Groq `x-ratelimit-reset-requests` / `x-ratelimit-reset-tokens` (Go-duration-like `"7.66s"`, `"2m59.56s"`; use the one for the exhausted dimension, i.e. whichever `x-ratelimit-remaining-*` is 0, else the max); OpenRouter `X-RateLimit-Reset` (unix ms); Anthropic `anthropic-ratelimit-*-reset` (RFC3339, P2); Gemini body `error.details[].retryDelay` (`"17s"`, P2).
3. `ROUTER0_DEFAULT_RATE_LIMIT_COOLDOWN_SECONDS` (30).

### Provider overrides (implement `providers.Classifier`; fall back to generic)

| Provider | Rule | Class |
|----------|------|-------|
| Groq | 429 (`rate_limit_exceeded`) | `rate_limit` |
| Groq | 498 (flex-tier capacity) | `transient` |
| Groq | 413 (request exceeds TPM / context) | `bad_request` |
| Groq | 400 body code `organization_restricted` | `auth` |
| OpenRouter | 402 | `insufficient_credits` |
| OpenRouter | 403 (moderation flag on input) | `bad_request` (overrides generic `auth`) |
| OpenRouter | 200 response or SSE event whose body is `{"error":{"code":<int>,…}}` | classify using the numeric `code` as the HTTP status |
| OpenRouter | 502, 503 (no upstream available) | `transient` |
| Anthropic (P2) | 400 `invalid_request_error` whose message contains `credit balance is too low` | `insufficient_credits` |
| Anthropic (P2) | 401 `authentication_error`, 403 `permission_error` | `auth` |
| Anthropic (P2) | 404 `not_found_error` | `model_unavailable` |
| Anthropic (P2) | 413 `request_too_large` | `bad_request` |
| Anthropic (P2) | 429 `rate_limit_error` | `rate_limit` |
| Anthropic (P2) | 500 `api_error`, 529 `overloaded_error` | `transient` |
| Gemini (P2) | 429 `RESOURCE_EXHAUSTED` | `rate_limit` (cooldown from `retryDelay`) |
| Gemini (P2) | 400 with reason `API_KEY_INVALID`, 403 `PERMISSION_DENIED` | `auth` |
| Gemini (P2) | 400 `FAILED_PRECONDITION` whose message mentions billing | `insufficient_credits` |
| Gemini (P2) | 404 `NOT_FOUND` | `model_unavailable` |
| Gemini (P2) | body is a JSON array `[{"error":…}]` (OpenAI-compat endpoint quirk) | unwrap the first element, then apply the rules |
| Mesh API (P2) | generic rules | — |

Assumed: the OpenRouter 403-moderation, Groq 498/413, Gemini billing, and Mesh API behaviours come from public docs as understood at spec time; each rule has a fixture-based unit test, so a wrong rule is a one-line fix. Mesh API rules stay generic until its docs (app.meshapi.ai) are checked during the Phase 2 slice.

### Key-state checks (`keystate.Check(key, model)`)

`CheckMany` reads all active rows for the plan's keys in one query (or from the 2 s cache). For each (key, model), the first active row wins, in order credits_exhausted → (P3) budget_exhausted → rate_limited (matching `scope_model`) → transient_cooldown. `RetryAfter` = `until_at − now`. `Mark` writes through to Postgres and updates the local cache immediately, so the same instance never re-tries a key it just marked. Assumed: there is no last-resort retry of keys in cooldown. If every candidate is in cooldown, the request returns 429/402 without calling any provider, which is the cheapest correct answer.

### Final error precedence (`routing.FinalError`)

Each considered candidate gets an effective class. Attempted candidates use their error class. Skipped candidates map `rate_limited` → `rate_limit`, and `credits_exhausted`/`budget_exhausted` → `insufficient_credits`. Then, first match wins:

| # | Condition | Response |
|---|-----------|----------|
| 1 | Plan empty (no org provider offers the model, after header filter) | **404** `invalid_request_error` / `model_not_found`, `param: "model"` |
| 2 | Any candidate `rate_limit` | **429** `rate_limit_error` / `rate_limited`; `Retry-After` = ceil(min RetryAfter over rate-limited candidates), ≥ 1 |
| 3 | Any candidate `insufficient_credits` | **402** `insufficient_credits` / `insufficient_credits` |
| 4 | All candidates `model_unavailable` | **404** `model_not_found` |
| 5 | All candidates `bad_request` or `model_unavailable` (≥ 1 `bad_request`) | **400** `invalid_request_error` / `upstream_rejected_request`; message = first `bad_request` provider message, prefixed `"<provider>: "` |
| 6 | Otherwise (transient, auth, or mixes of those) | **503** `upstream_error` / `all_providers_failed` |

Why 429 beats 402: when some keys are throttled and others are out of credits, waiting `Retry-After` seconds and retrying will succeed without human action, so it is the most actionable signal for an automated caller. 402 tells the caller that retrying will not help until someone tops up. 404 is only returned when nothing could serve the model, because a 404 caused by a stale catalog must not hide a real throttle.

Every error body includes `error.router0.attempts` (provider, key label, outcome, class, retry_after_s), so callers and humans can see why.

## Success Criteria

- [ ] Unit: table test of `Classify` per provider over fixture responses (status + headers + body) covering every row above → expected class, `Code`, `RetryAfter`.
- [ ] Unit: `Retry-After: 7` → 7 s; `Retry-After: <HTTP-date 20 s ahead>` → 20 s ±1; Groq `x-ratelimit-reset-requests: 2m3.5s` with remaining-requests 0 → 124 s; no hints → 30 s; `Retry-After: 99999` → 3600 s.
- [ ] Unit: `FinalError` table test covering all six precedence rows and mixed cases (429+402 → 429; 402+503 → 402; 404+503 → 503; 400+404 → 400).
- [ ] Integration: every serving key returns 402 → client gets 402 `insufficient_credits`; a second request within the hour makes no upstream calls and still returns 402.
- [ ] Integration: keys return 429 with `Retry-After: 5` and `Retry-After: 9` → client gets 429 with `Retry-After: 5`.
- [ ] Integration: one key 429 (`Retry-After: 20`), the other 402 → 429 with `Retry-After` 20 (±1).
- [ ] Integration: unknown model → 404 `model_not_found` with `param: "model"`, and no provider calls.
- [ ] Integration: the OpenAI Python/Node SDK error mapping holds: the status codes and `error.code` values in responses equal the table above (asserted on raw HTTP in Go).
- [ ] No `request_attempts.error_message` value is longer than 200 chars or contains the mock's sentinel body text `SENTINEL-BODY`.
