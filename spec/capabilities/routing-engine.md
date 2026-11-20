# Capability: Routing Engine

Phase 1. Replaces the per-user "best combo" pool/scorer path. Today `Selector.Select(ctx, userID)` and `Pool.SelectComboForSession` ignore the requested model; that is removed.

## What It Does

For each request: turn the requested model into an ordered list of candidates (provider + provider model id + key) using the org's catalog, the fixed provider priority (or the per-request header override), and key order. Then try candidates one after another, skipping keys in cooldown, failing over on any error, until one succeeds or the list is exhausted. On exhaustion, return the single most actionable error.

## Inputs

| Input | Source |
|-------|--------|
| `auth.Principal` (org, token) | Service-token middleware |
| Parsed request (model, stream, body) | `handlers/inference.go` |
| `X-Router0-Providers` header | Request |
| Catalog entries | `catalog.Resolver` |
| Provider order | `orgs.PriorityStore` |
| Active keys per provider, by position | `keys.Repository.ActiveByOrgProvider` |
| Key states | `keystate.Store.Check` |

## Outputs

| Output | Destination |
|--------|-------------|
| Response (JSON or SSE) | Client |
| `routing.Result{Winner, Attempts}` | Metadata recorder (request + attempt rows) |
| `keystate.Mark` calls; key `status='error'` on auth; catalog row unavailable on `model_unavailable` | PostgreSQL (`provider_key_state`, `provider_keys`, `org_model_catalog`) |

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| PostgreSQL | catalog, priority, keys (3 indexed queries) | 503 `upstream_error/router_unavailable` |
| PostgreSQL via keystate | `CheckMany` once per request (2 s in-memory cache), `Mark` on failure | Check error: fail open (treat keys as not blocked), log `warn`. Mark error: log `warn`, continue |
| Provider adapters | `Complete` / `Stream` | Classify → mark → next candidate |

## Business Rules

### Plan construction (`routing.BuildPlan`)

1. `entries := catalog.Resolve(org, model)`. Empty → plan is empty → `404 model_not_found`.
2. Provider order:
   - Header present: parse the comma list, lowercase, trim, map `gemini` → `google`, de-duplicate keeping the first occurrence. An unknown provider id → `400 invalid_provider_override` before any upstream call. The order is exactly the header list; providers not in it are excluded.
   - No header: `org_provider_priority` order, then any remaining providers alphabetically.
3. For each provider in order that has an entry: for each active key (`status='active'`) in `position` order → `Candidate{provider, entry.ProviderModelID, key.ID, key.Label}`.
4. Keys whose keystate is `transient_cooldown` are moved to the end of the plan, keeping their relative order. They are not skipped, so a single-key org is not locked out by one blip.
5. Empty plan after the header filter → `404 model_not_found` with message `"… not offered by providers in X-Router0-Providers (groq)"`.

### Execution (`routing.Executor.Run`)

- Strictly sequential, in plan order. No parallel hedging. Assumed: hedging is removed because it double-bills keys and contradicts fixed priority.
- For each candidate:
  1. State from the request's single `keystate.CheckMany` result (computed before the loop). `rate_limited` or `credits_exhausted` (or P3 `budget_exhausted`) → record `outcome=skipped`, `skip_reason`, `retry_after` → next. Skipped candidates do not count toward `ROUTER0_MAX_ATTEMPTS`.
  2. Decrypt the key (failure → `outcome=error, class=transient, provider_error_code=key_decrypt_failed` → next).
  3. Call the attempt func with a per-attempt context: non-stream timeout `ROUTER0_ATTEMPT_TIMEOUT_SECONDS` (120); stream timeout `ROUTER0_STREAM_FIRST_BYTE_TIMEOUT_SECONDS` (20) until the first chunk, then no router-side cap. Assumed: the current 15 s non-stream timeout is too short for long completions, so it becomes 120 s.
  4. Success → stop. Error → `*providers.ProviderError` (via the adapter's classifier) → record → `keystate.Mark` → side effects (auth → key `status='error'`; `model_unavailable` → catalog row unavailable) → next.
- Stops after `ROUTER0_MAX_ATTEMPTS` (8) attempted candidates. Unattempted candidates are not part of the error decision.
- The client disconnecting (request ctx cancelled) stops the loop immediately. The cancelled attempt is recorded as `error`/`client_cancelled` and the key is **not** marked.
- Fail over on **any** error class, including `bad_request` and `auth`, per the brief.

### Streaming

- The stream attempt func calls `Stream`, then waits for the first item from the channel: a content/role/tool chunk means success (commit). An error event or a channel closed before any chunk means failure, classified, and failover continues.
- On commit, the handler writes `200`, the `X-Router0-*` headers, and the first chunk, then pipes the rest. Every chunk is re-serialised as an OpenAI `chat.completion.chunk` with `model` = canonical name. Raw provider SSE lines are no longer passed through, because Anthropic and Gemini chunks must be translated and the model name rewritten.
- Mid-stream upstream failure → write `data: {"error":{"type":"upstream_error","code":"stream_interrupted",…}}`, then `data: [DONE]`. Record `status=error, error_code=stream_interrupted`. Mark the key per the classified error.
- router0 always sends `stream_options: {"include_usage": true}` upstream for providers that support it (OpenAI-compatible). It forwards the usage chunk to the client only if the client asked for it.

### Exhaustion → final error

`routing.FinalError(result)`. Precedence is defined in error-classification.md: **429 > 402 > 404 > 400 > 503**.

### Response headers

`X-Router0-Request-Id`, `X-Router0-Canonical-Model`, `X-Router0-Attempts` always. `X-Router0-Provider`, `X-Router0-Model`, `X-Router0-Key`, `X-Router0-Latency-Ms` on success. `Retry-After` on 429.

### Legacy code

`routing/pool.go`, `scorer.go`, `selector.go`, `promoter.go`, `tier.go`, `meta_syncer.go`, `ratelimit.go`, `ratelimit_observer.go` are no longer called from the request path, and their goroutines are not started from `main.go` in Phase 1. `remediate.go`'s `HandleAuthError` behaviour (key → `error`) moves into the executor's side effects. Deleted in Phase 3.

## Success Criteria (all runnable against mock providers)

- [ ] Priority `groq, openrouter`; mock Groq returns 200 → served by Groq, trail length 1.
- [ ] Mock Groq key A returns 500 → served by Groq key B; trail `[A:error/transient, B:success]`.
- [ ] Both Groq keys return 429 → served by OpenRouter; trail has 3 entries.
- [ ] `X-Router0-Providers: openrouter` → mock Groq receives zero requests (call counter) and the response comes from OpenRouter.
- [ ] `X-Router0-Providers: nosuch` → 400 `invalid_provider_override`, and no provider is called.
- [ ] Requesting `llama-3.3-70b` never calls a provider whose catalog lacks it (the request goes only to providers that list it).
- [ ] Streaming: Groq key A fails before the first byte (503), key B streams 3 chunks → the client receives 3 content chunks + `[DONE]`, and the trail shows the failover.
- [ ] Streaming: a mock that sends 2 chunks then closes with an error → the client receives the 2 chunks, a `stream_interrupted` error event, and `[DONE]`; the log row has `error_code=stream_interrupted`.
- [ ] After a 429 with `Retry-After: 30` on Groq key A, a second request within 30 s skips key A without calling it (mock call counter unchanged), and the trail shows `skipped/rate_limited`.
- [ ] A single-key org whose key just got a transient 502 still has the next request attempted on that key (moved to the end, not skipped).
- [ ] Client disconnect during an attempt does not mark the key (no keystate entry).
- [ ] With 10 keys all failing transiently, only 8 upstream calls are made.
