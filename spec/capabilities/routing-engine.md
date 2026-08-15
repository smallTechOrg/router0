# Capability: Routing Engine

## What It Does

Selects the best model-provider-key combination for each inference request by scoring active and common pools using a weighted formula over latency, error rate, cost, and rate-limit quota. Handles retry on failure (sequential then parallel hedge) and updates pool scores in Redis after each request.

## Inputs

| Input | Type | Source | Required |
|-------|------|---------|----------|
| User ID | UUID | Auth middleware | Yes |
| Internal `ChatRequest` | Go struct | Request handler | Yes |
| Pool state | Redis sorted sets | Redis (`pool:{user_id}:active`, `pool:{user_id}:common`) | Yes |
| Rate-limit counters | Redis counters | Redis (`rl:{user_id}:{provider}:{model}:{window}`) | Yes |
| Rolling metrics | Redis hashes | Redis (`metrics:{user_id}:{provider}:{model}`) | Yes |

## Outputs

| Output | Type | Destination |
|--------|------|-------------|
| Selected `Combo` (provider + model + key ID) | Go struct | Request handler → Provider Adapter |
| Updated pool scores | Redis sorted set | Redis |
| Updated rate-limit counters | Redis counter | Redis |
| Request outcome metadata | Go struct | Metadata Recorder → PostgreSQL |

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| Redis | ZRANGEBYSCORE pool sets; GET/INCR rl counters; HGET metrics | Return 503 to client; log "pool unavailable" |
| Provider Adapter | `Stream()` or `Complete()` call | Retry / hedge logic (see below) |

## Business Rules

### Pool Structure

Each user has two pools stored in Redis:

- **Active pool** (`pool:{user_id}:active`): top-scoring model-provider combos. Serves 80% of requests (determined by `rand.Float64() < 0.80`).
- **Common pool** (`pool:{user_id}:common`): all registered combos. Serves 20% of requests (telemetry gathering).

A combo is the tuple `(provider, model, provider_key_id)`. When a user has multiple keys for the same provider, each key-model pair is a distinct combo.

### Scoring Formula

Each combo is scored 0.0–1.0 (higher is better). Combos with exhausted daily quota are scored `-1` and excluded from selection entirely.

```
score(combo) =
    0.35 * (1 - error_rate_5m)           // reliability weight
  + 0.30 * latency_score(combo)          // speed weight (inverted)
  + 0.20 * (1 - cost_score(combo))       // cost weight (inverted)
  + 0.15 * quota_remaining_fraction(combo)  // quota pressure weight
```

Where:
- `error_rate_5m` = (errors in last 5 min) / (requests in last 5 min), range [0, 1]. Defaults to 0.0 for a new combo with no traffic.
- `latency_score(combo)` = `p95_latency_ms(combo)` / `max_p95_ms_in_pool`, range [0, 1]. If only one combo, score = 0.5.
- `cost_score(combo)` = `cost_per_token(combo)` / `max_cost_in_pool`, range [0, 1].
- `quota_remaining_fraction(combo)` = min(minute_remaining/minute_limit, hour_remaining/hour_limit, day_remaining/day_limit), range [0, 1]. If provider does not publish limits, defaults to 1.0.

Weights are static in Phase 1. Phase 2 replaces them with a contextual-bandit learner (see `spec/agent.md`).

### Rate-Limit Tracking

Redis keys:

| Key Pattern | Type | Value | TTL |
|-------------|------|-------|-----|
| `rl:{user_id}:{provider}:{model}:minute` | String (int counter) | Requests used | Expires at the top of next minute |
| `rl:{user_id}:{provider}:{model}:hour` | String (int counter) | Requests used | Expires at the top of next hour |
| `rl:{user_id}:{provider}:{model}:day` | String (int counter) | Requests used | Expires at UTC midnight |

Counters are incremented with `INCR` after each request is dispatched. Provider-returned rate-limit headers update `quota_remaining_fraction` directly via `SET rl:{user_id}:{provider}:{model}:remaining:{window}` (separate key per window, overwritten on each response).

**Critical rule:** any combo where `day_remaining == 0` (daily quota exhausted) must NEVER be selected. The scorer assigns `-1` and the selector skips it entirely.

### Error Classification and Pool Impact

| ProviderError Class | Rate-limit counter action | Pool score impact |
|---------------------|--------------------------|-------------------|
| `ErrClassRateLimit` | Mark window exhausted in Redis | Score set to -1 for exhausted window; removed from active pool |
| `ErrClassProviderThrottle` | Mark provider throttled (5 min cooldown Redis key) | All combos for provider excluded for 5 min |
| `ErrClassAuth` | None | Key status set to `error` via DB write; removed from pool |
| `ErrClassTimeout` | Increment request counter | p95 latency increased |
| `ErrClassUpstream` | Increment error counter | error_rate increased |

### Request Routing Flow

1. Authenticate the Zer0 API key → resolve `user_id`.
2. Roll dice: `active = rand.Float64() < 0.80`.
3. Load the chosen pool from Redis (sorted set, descending score order).
4. Filter out any combos with exhausted daily quota or provider-throttle cooldown.
5. If the filtered pool is empty, fall back to the other pool. If both empty, return 503.
6. Select the top-scored combo from the filtered pool.
7. Decrypt the provider key in-memory; call `Provider.Stream()` (or `Complete()`) with the request.
8. On success: stream response to client; record metadata; update metrics.
9. On failure: execute retry/hedge logic (see below).

### Retry and Hedge Logic

**Sequential retry (attempt 2):**
- On any failure except `ErrClassAuth` (which disqualifies the key), pick the next-ranked eligible combo from the same pool.
- Execute the same request.

**Parallel hedge (attempt 3+):**
- If the sequential retry also fails, enter hedge mode.
- Fire 2 remaining eligible combos simultaneously using `context.WithCancel`.
- Return the first successful response to the client; cancel the other in-flight request immediately.
- If both fail, return structured 503.

**Total failure response:**

```json
{
  "error": {
    "message": "All available providers failed to fulfill this request.",
    "type": "zer0_gateway_error",
    "code": "all_providers_failed",
    "details": [
      {"provider": "groq", "model": "llama-3.1-70b-versatile", "error": "rate_limit"},
      {"provider": "google", "model": "gemini-1.5-flash", "error": "upstream_error"}
    ]
  },
  "retry_after": 30
}
```

HTTP status: 503. `retry_after` is the minimum TTL-to-expiry across exhausted rate-limit windows (seconds), or 30 if unknown.

### Pool Promotion and Demotion

Re-scored every 5 minutes via a background goroutine for each user that has had traffic in the past 24 hours.

**Promotion to active pool** (common → active): score ≥ 0.70 AND error_rate_1h < 0.05 AND p95_latency_ms < 800 AND day_quota_remaining_fraction > 0.20.

**Demotion from active pool** (active → common): score < 0.40 OR error_rate_1h > 0.15 OR p95_latency_ms > 2000 OR day_quota_remaining_fraction < 0.05.

Active pool size is soft-capped at 5 combos. If more than 5 combos meet promotion criteria, the top 5 by score are in active; others remain in common.

### Metrics Stored in Redis

`metrics:{user_id}:{provider}:{model}` hash fields:

| Field | Type | Description |
|-------|------|-------------|
| `requests_5m` | int | Request count in rolling 5-minute window |
| `errors_5m` | int | Error count in rolling 5-minute window |
| `requests_1h` | int | Request count in rolling 1-hour window |
| `errors_1h` | int | Error count in rolling 1-hour window |
| `p95_latency_ms` | int | Running p95 latency estimate (reservoir sampling, 100-sample reservoir) |
| `last_request_at` | unix timestamp | For pool re-scoring scheduling |

Rolling windows are approximated with two counter buckets per window (current + previous) rotated on TTL. This avoids expensive sorted set operations at request time.

## Success Criteria

- [ ] With two active pool combos (e.g. Groq + Google), 100 sequential requests produce roughly 80 ± 10 routing to the active pool and 20 ± 10 to the common pool.
- [ ] A combo with a manually set daily quota counter at the limit is never selected; verified by sending 10 requests and asserting none use the exhausted combo in `X-Zer0-Provider` / `X-Zer0-Model` headers.
- [ ] Mocking the top-ranked combo to return `ErrClassUpstream`: the router uses the second-ranked combo (sequential retry); `X-Zer0-Provider` in the response matches the second combo.
- [ ] Mocking the top two combos to fail: the router enters hedge mode and returns a successful response from one of the remaining combos within 2× single-request timeout.
- [ ] Mocking all combos to fail: the router returns HTTP 503 with `{"error":{"code":"all_providers_failed"}}` and a `retry_after` field > 0.
- [ ] After 10 simulated errors on a combo, the re-scoring goroutine demotes it from the active pool within one 5-minute cycle.
- [ ] Redis `rl:{user_id}:{provider}:{model}:minute` counter increments by 1 for each completed request.
