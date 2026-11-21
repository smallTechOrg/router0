# Capability: Observability (Request Log, Metrics, Logs)

Phase 1.

## What It Does

Records metadata (never content) for every inference request and every candidate considered, and serves it as the org's request log with a failover trail. Also exposes Prometheus metrics and structured logs for operators.

## Inputs

| Input | Source |
|-------|--------|
| `routing.Result` + request facts (org, token, canonical model, stream, tokens, latency, final status/code) | Inference handler |
| Dashboard queries | `GET /api/requests`, `GET /api/requests/{id}`, `GET /api/costs` |

## Outputs

| Output | Destination |
|--------|-------------|
| `request_metadata` row + `request_attempts` rows | PostgreSQL (async) |
| Request log JSON with `trail` | Dashboard |
| `/metrics` | Prometheus |
| JSON log lines | stdout |

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| PostgreSQL | async INSERTs (buffered channel, 1 worker pool of 4) | `error` log; the request is unaffected; drop when buffer (10 000) full and increment `router0_metadata_dropped_total` |

## Business Rules

### Request log

- One `request_metadata` row per inference request: `org_id`, `service_token_id`, `zer0_request_id` (= `r0-…`), `canonical_model`, `provider`/`model` (winner or `'none'`), `provider_key_id`, `provider_key_label`, `input_tokens`, `output_tokens`, `latency_ms`, `estimated_cost_usd`, `status`, `http_status`, `error_code`, `stream`, `attempt_count`, `retry_count` (= attempts − 1, legacy).
- One `request_attempts` row per considered candidate (attempted or skipped), with `attempt_order`, `outcome`, `error_class`, `skip_reason`, `http_status`, `retry_after_s`, `provider_error_code`, `latency_ms`, `provider_key_label`.
- `key_label` is copied at write time, so renaming or disabling a key does not rewrite history.
- **Never stored:** prompt messages, response text, tool arguments, provider error bodies, provider keys, router0 tokens.
- `GET /api/requests` joins attempts into `trail` ordered by `attempt_order`, and joins `service_tokens.name` → `token_name`. It is org-scoped. Pagination uses `before` on `created_at` (index `request_metadata_org_created`).

### Metrics (renamed `zer0_*` → `router0_*`)

| Metric | Type | Labels |
|--------|------|--------|
| `router0_requests_total` | counter | `provider` (winner or `none`), `status`, `code` |
| `router0_request_duration_seconds` | histogram (0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60) | `provider`, `stream` |
| `router0_attempts_total` | counter | `provider`, `outcome`, `class` |
| `router0_key_state_marks_total` | counter | `provider`, `reason` |
| `router0_catalog_refresh_total` | counter | `provider`, `status` |
| `router0_token_auth_failures_total` | counter | `reason` (`unknown`, `revoked`, `malformed`) |
| `router0_metadata_dropped_total` | counter | — |

No `org_id`, key id, or model labels, to keep cardinality bounded. Assumed: per-org breakdowns come from the request log, not from Prometheus. `config/alerts.yaml` is rewritten for these names: error ratio > 15 % for 5 m; rising `router0_key_state_marks_total{reason="credits_exhausted"}`; `router0_metadata_dropped_total` increasing.

### Logs

One `info` line `request_completed` per request (fields in architecture.md › Logging). `warn` on each failover attempt (`attempt_failed`, with provider, key_label, class). `error` on metadata write failure. Base field `service: "router0"`. No secrets or content (existing rule).

## Success Criteria

- [ ] After a failover request (Groq key A 429 → key B ok), `GET /api/requests` returns it within 3 s with `attempt_count: 2`, `key_label: "groq-b"`, and `trail[0].class: "rate_limit"`.
- [ ] A request with message content `SENTINEL-PROMPT-7f3a` → `SELECT` over every text column of `request_metadata` and `request_attempts` for that request finds no match.
- [ ] `GET /metrics` contains `router0_requests_total` and no `zer0_` metric names.
- [ ] Org A cannot see org B's requests (`GET /api/requests/{B's id}` → 404).
- [ ] A 404 `model_not_found` request is logged with `provider='none'`, `error_code='model_not_found'`, `attempt_count: 0`.
