# Capability: Observability

## What It Does

Emits structured JSON logs for every request event, exposes a Prometheus `/metrics` endpoint for operational monitoring, and provides an Admin API for inspecting and manipulating pool state across all users.

## Inputs

| Input | Type | Source | Required |
|-------|------|---------|----------|
| Request lifecycle events | Go function calls | Request handler, routing engine, adapters | Yes |
| Admin API credentials | JWT with `role=admin` | HTTP `Authorization` header | Yes (Admin API) |
| Prometheus scrape | HTTP GET | Prometheus server or any scraper | Yes (`/metrics`) |

## Outputs

| Output | Type | Destination |
|--------|------|-------------|
| Structured JSON log lines | Newline-delimited JSON | stdout (captured by container runtime → Loki/Datadog/CloudWatch) |
| Prometheus metrics | text/plain (Prometheus exposition format) | `GET /metrics` |
| Admin API responses | JSON | HTTP response |

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| None | Logging and metrics are in-process | N/A |
| PostgreSQL | Admin API: read user pool state | Return 500 to admin caller |
| Redis | Admin API: read/write pool state; force promote/demote | Return 500 to admin caller |

## Business Rules

### Structured JSON Logs

Every log line is a JSON object on a single newline. All lines include these base fields:

| Field | Type | Description |
|-------|------|-------------|
| `ts` | RFC3339 nanosecond | Timestamp |
| `level` | string | `debug` / `info` / `warn` / `error` |
| `msg` | string | Short human-readable description |
| `service` | string | Always `"zer0-router"` |
| `request_id` | string | UUID per HTTP request |
| `user_id` | string | UUID of authenticated user (omitted for unauthenticated) |

**Per-request log line (emitted at request completion):**

| Field | Type | Description |
|-------|------|-------------|
| `event` | string | `"request_completed"` |
| `provider` | string | Provider that served the request |
| `model` | string | Model used |
| `provider_key_id` | string | UUID of the key used |
| `input_tokens` | int | Token count of the input |
| `output_tokens` | int | Token count of the response |
| `latency_ms` | int | Wall-clock time from first byte in to last byte out |
| `status` | string | `"success"` / `"error"` / `"hedged"` |
| `error_class` | string | `ProviderError.Class` value (omitted on success) |
| `retry_count` | int | Number of retries (0 = no retry, 1 = one sequential retry, 2 = hedge) |
| `pool` | string | `"active"` or `"common"` |
| `estimated_cost_usd` | float | Cost estimate |

**Never logged:** raw API keys (provider or Zer0), prompt content, response content, personally identifying information beyond `user_id`.

**Log level rules:**
- `debug`: pool scoring details (scores per combo), rate-limit counter reads.
- `info`: every completed request, startup events, pool promotion/demotion events.
- `warn`: retry triggered, pool falling below 2 combos for a user.
- `error`: all-providers-failed event, database write failure, Redis unavailability.

Log level is configurable via `LOG_LEVEL` environment variable (default: `info`).

### Prometheus Metrics

Endpoint: `GET /metrics` — no authentication in Phase 1 (run behind a firewall / Kubernetes NetworkPolicy).

| Metric Name | Type | Labels | Description |
|-------------|------|--------|-------------|
| `zer0_requests_total` | Counter | `provider`, `model`, `status` | Total inference requests |
| `zer0_request_duration_ms` | Histogram | `provider`, `model` | Request latency in ms. Buckets: 100, 250, 500, 1000, 2000, 5000 |
| `zer0_retry_total` | Counter | `provider`, `model`, `retry_type` (`sequential`/`hedge`) | Retries triggered |
| `zer0_pool_size` | Gauge | `user_id`, `pool` (`active`/`common`) | Current pool member count |
| `zer0_pool_score` | Gauge | `user_id`, `provider`, `model` | Current score (0–1) |
| `zer0_ratelimit_exhausted_total` | Counter | `user_id`, `provider`, `model`, `window` | Rate-limit exhaustion events |
| `zer0_provider_errors_total` | Counter | `provider`, `error_class` | Provider errors by class |
| `zer0_active_users` | Gauge | (none) | Users with at least one request in the last 24 h |

### Alerting Rules (Configuration — not enforced by the router)

A sample Prometheus alerting rules file is provided at `config/alerts.yaml`:

```yaml
groups:
  - name: zer0_router
    rules:
      - alert: HighErrorRate
        expr: rate(zer0_requests_total{status="error"}[5m]) / rate(zer0_requests_total[5m]) > 0.15
        for: 2m
        annotations:
          summary: "Error rate exceeded 15% for 2 minutes"

      - alert: AllProvidersDown
        expr: zer0_pool_size{pool="active"} == 0
        for: 1m
        annotations:
          summary: "Active pool is empty for a user"

      - alert: SmallActivePool
        expr: zer0_pool_size{pool="active"} < 2
        for: 5m
        annotations:
          summary: "Active pool has fewer than 2 combos for 5 minutes"
```

### Admin API

All Admin API endpoints require a valid JWT with `role = 'admin'`. Return HTTP 403 for non-admin callers.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/admin/users` | List all users (id, email, status, created_at) |
| `POST` | `/admin/invites` | Create an invite (body: `{"email":"..."}`) |
| `GET` | `/admin/pool/:user_id` | Inspect a user's full pool state (active + common combos, scores, rate-limit counters) |
| `POST` | `/admin/pool/:user_id/promote` | Force-promote a combo to the active pool. Body: `{"provider":"groq","model":"llama-3.1-70b-versatile","key_id":"<uuid>"}` |
| `POST` | `/admin/pool/:user_id/demote` | Force-demote a combo from active to common |
| `PUT` | `/admin/keys/:key_id/disable` | Set a provider key's status to `disabled` and remove from all pools |

Admin pool operations write directly to Redis pool sorted sets and emit an `info`-level log with `event: "admin_pool_override"` and the admin `user_id`.

## Success Criteria

- [ ] After one successful chat completion request, `stdout` contains exactly one JSON line with `event: "request_completed"` and correct `provider`, `model`, `input_tokens`, `output_tokens`, `latency_ms`, `status: "success"`.
- [ ] The log line contains no field with a value that looks like an API key (no string matching `sk-`, `gsk_`, `AIza`, or `Bearer`).
- [ ] `GET /metrics` returns HTTP 200 with `Content-Type: text/plain; version=0.0.4` and a line `zer0_requests_total` with at least one labelled sample after a request.
- [ ] `zer0_request_duration_ms_bucket` appears in `/metrics` output with the specified bucket boundaries.
- [ ] `GET /admin/pool/:user_id` by an admin user returns the correct pool combos for that user.
- [ ] `POST /admin/pool/:user_id/demote` removes the specified combo from the active pool in Redis; subsequent routing for that user does not select that combo from the active pool.
- [ ] `GET /admin/pool/:user_id` by a non-admin user returns HTTP 403.
- [ ] Starting the service with `LOG_LEVEL=debug` causes pool scoring detail lines to appear; starting with `LOG_LEVEL=info` suppresses them.
