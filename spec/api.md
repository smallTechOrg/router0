# API: router0

Base URL: `http(s)://<host>:8080`. Three auth schemes:

| Routes | Auth | Notes |
|--------|------|-------|
| `/v1/*` | `Authorization: Bearer r0-sk-…` (service token; legacy `zer0:sk-…` accepted) | Called by backend services |
| `/auth/*` | none / refresh cookie | Login, register |
| `/api/*` | `Authorization: Bearer <JWT>` | Dashboard; scoped to the caller's org (`org_id` claim) |
| `/admin/*` | JWT with platform `role=admin` | Platform operator |
| `/healthz`, `/metrics` | none | Run behind a firewall |

Org roles on `/api/*`: `owner` and `admin` may write; `member` may only read (GET). Writes by a `member` return `403 forbidden/org_role_required`. Resources of another org return `404` (never `403`).

---

## Inference (OpenAI-compatible)

### `POST /v1/chat/completions` — Phase 1

Request headers:

| Header | Required | Meaning |
|--------|----------|---------|
| `Authorization: Bearer r0-sk-…` | yes | Service token |
| `X-Router0-Providers: groq,openrouter` | no | Restricts AND orders providers for this request, overriding org priority. Case-insensitive; whitespace ignored; Provider aliases are accepted: `gemini` → `google`, and (Assumed) `aifiesta` / `mesh` → `meshapi` (Mesh API by Fiesta Labs, "AI Fiesta Agent Mesh"). Canonical provider ids are `groq`, `openrouter`, `anthropic`, `google`, `meshapi`, `nvidia`, `together`, `baseten`. Unknown provider name → `400 invalid_provider_override`. Providers listed but without keys in the org are ignored. |

Body: standard OpenAI chat completion request. Fields forwarded: `model` (required, canonical or any provider-specific name), `messages`, `max_tokens`, `temperature`, `top_p`, `stop`, `stream`, `stream_options`, `tools`, `tool_choice`, `response_format`, `seed`, `user`. Unknown fields are ignored. Message `content` may be a string or an array of `{"type":"text","text":…}` parts (flattened to a string); any non-text part returns `400 unsupported_content`. Assumed: image/audio inputs are out of scope for Phases 1–3.

Phase 1 forwards `tools`/`tool_choice`/`tool_calls`/`tool` messages unchanged to OpenAI-compatible providers (Groq, OpenRouter). Phase 2 translates them for Anthropic (native Messages API) and verifies them for Gemini and Mesh API, non-stream and stream (an OpenAI-SDK-style agent loop is a Phase 2 gate test).

**Success (non-streaming) — 200**

```json
{
  "id": "chatcmpl-…",
  "object": "chat.completion",
  "created": 1763640000,
  "model": "llama-3.3-70b",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "1, 2, 3"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19}
}
```

`model` is the canonical model name. Assumed: callers want the name they asked for; the provider-specific id is in `X-Router0-Model`.

Assistant tool calls are returned as `message.tool_calls` with `finish_reason: "tool_calls"`.

**Success (streaming) — 200, `Content-Type: text/event-stream`**

`data: {chat.completion.chunk}` lines in OpenAI format, ending with `data: [DONE]`. Chunk `model` = canonical name. When the caller sets `stream_options.include_usage`, the final chunk carries `usage`. router0 always asks the provider for usage so it can log tokens.

Failover in streaming happens only before the first chunk is written. If the upstream breaks after that, router0 writes one final `data: {"error":{"type":"upstream_error","code":"stream_interrupted","message":"…"}}` line followed by `data: [DONE]`, and logs the request as `error` with `error_code = stream_interrupted`.

**Response headers (success and error)**

| Header | Example |
|--------|---------|
| `X-Router0-Request-Id` | `r0-3f9c…` |
| `X-Router0-Canonical-Model` | `llama-3.3-70b` |
| `X-Router0-Provider` | `groq` (success only) |
| `X-Router0-Model` | `llama-3.3-70b-versatile` (success only) |
| `X-Router0-Key` | `groq-a` (key label; success only) |
| `X-Router0-Attempts` | `2` (attempted + skipped candidates) |
| `X-Router0-Latency-Ms` | `812` (non-streaming: total; streaming: time to first byte) |
| `Retry-After` | `12` (429 only) |

**Errors** (shape in architecture.md › Conventions; precedence in capabilities/error-classification.md)

| HTTP | `type` | `code` | When |
|------|--------|--------|------|
| 400 | `invalid_request_error` | `invalid_json`, `missing_model`, `unsupported_content`, `invalid_provider_override` | Request rejected before routing |
| 401 | `authentication_error` | `invalid_api_key` | Missing, unknown, or revoked service token |
| 404 | `invalid_request_error` | `model_not_found` (`param: "model"`) | No org provider (after header filter) offers the model, or every attempted provider answered "model not found" |
| 402 | `insufficient_credits` | `insufficient_credits` | No candidate rate-limited, and at least one candidate out of credits / budget |
| 429 | `rate_limit_error` | `rate_limited` | At least one candidate rate-limited (attempted or in cooldown). `Retry-After` = soonest cooldown end, seconds, ≥ 1 |
| 400 | `invalid_request_error` | `upstream_rejected_request` | Every attempt was rejected as a bad request (provider 400/422); `message` carries the first provider's error message |
| 503 | `upstream_error` | `all_providers_failed` | Anything else (transient, auth, mixed) |

Example 429:

```http
HTTP/1.1 429 Too Many Requests
Retry-After: 12
X-Router0-Request-Id: r0-3f9c…

{"error":{"message":"All 3 keys serving 'llama-3.3-70b' are rate limited. Retry after 12s.","type":"rate_limit_error","param":null,"code":"rate_limited",
  "router0":{"request_id":"r0-3f9c…","attempts":[
    {"provider":"groq","key":"groq-a","outcome":"skipped","class":"rate_limit","retry_after_s":12},
    {"provider":"groq","key":"groq-b","outcome":"error","class":"rate_limit","retry_after_s":40},
    {"provider":"openrouter","key":"or-main","outcome":"error","class":"rate_limit","retry_after_s":60}]}}}
```

### `GET /v1/models` — Phase 1

Lists the caller org's catalog, one entry per canonical **chat** model that is available on at least one provider the org has an active key for. Models of other kinds (image, audio/TTS/music/transcription, embedding, computer-use/robotics/guard and similar) are never listed (Phase 2 classification: model-catalog.md › Model kinds; Phase 1 already skips Groq whisper/tts/guard and `embed` ids). Every entry has `"kind":"chat"`.

```json
{"object":"list","data":[
  {"id":"llama-3.3-70b","object":"model","created":1763640000,"owned_by":"router0",
   "router0":{"kind":"chat","providers":[
     {"provider":"groq","provider_model_id":"llama-3.3-70b-versatile"},
     {"provider":"openrouter","provider_model_id":"meta-llama/llama-3.3-70b-instruct"}]}}]}
```

Phase 2 adds org aliases as extra entries with `"router0":{"alias_of":"claude-sonnet-5-5"}`.

`/v1/embeddings` is not served in any phase (out of scope; roadmap.md). A request to it returns the router's standard `404`.

---

## Authentication

### `POST /auth/register` — Phase 1 (changed)

Body: `{"invite_token":"…","email":"a@b.co","password":"min 12 chars","org_name":"Acme"}` (`org_name` optional, default = email).
- Invite without `org_id` → creates an org; user becomes `owner`.
- Invite with `org_id` → user joins that org with `invites.org_role` (default `member`).

201:
```json
{"user":{"id":"…","email":"a@b.co","role":"user","status":"active","created_at":"…"},
 "org":{"id":"…","name":"Acme","role":"owner"},
 "access_token":"<jwt>"}
```
The response no longer contains `zer0_key`; the dashboard sends new users to `/dashboard/tokens` to create their first token. Errors unchanged: 400 invalid invite, 409 used invite / email exists, 410 expired, 422 short password.

### `POST /auth/login`, `POST /auth/refresh`, `POST /auth/logout` — unchanged

Access tokens gain an `org_id` claim. A JWT without `org_id` (issued before the upgrade) is accepted; middleware then loads the org from `org_members`.

### `GET /auth/me` — Phase 1 (changed)

`{"id","email","role","status","org":{"id","name","role"}}`

---

## Service Tokens — Phase 1

### `GET /api/tokens`

```json
{"tokens":[{"id":"…","name":"billing-worker","prefix":"r0-sk-7Hk2pQ9xLm3a","created_at":"…","created_by":"a@b.co","last_used_at":"…","revoked_at":null,"legacy":false}]}
```
Revoked tokens are included (sorted after live ones) so the log stays explainable.

### `POST /api/tokens` (owner/admin)

Body `{"name":"billing-worker"}` — 1–64 chars `[A-Za-z0-9._-]`. 201:
```json
{"id":"…","name":"billing-worker","token":"r0-sk-7Hk2pQ9xLm3aV8…","prefix":"r0-sk-7Hk2pQ9xLm3a","created_at":"…"}
```
`token` is returned only here. 409 `token_name_exists` if a live token has the name. 422 invalid name.

### `DELETE /api/tokens/{id}` (owner/admin)

Revokes (sets `revoked_at`, evicts the token from the in-process cache; other instances within 30 s). 204. Revoking an already-revoked token is 204 (idempotent). 404 for another org's token.

---

## Provider Keys — Phase 1 (org-scoped)

### `GET /api/providers`

Registered adapters, for the add-key form: `{"providers":[{"id":"groq","label":"Groq"},{"id":"openrouter","label":"OpenRouter"},{"id":"google","label":"Gemini"},{"id":"meshapi","label":"Mesh API"},…]}`. Labels come from a static map in `handlers/keys.go`. An unknown id falls back to the id itself.

### `GET /api/keys`

```json
{"keys":[{"id":"…","provider":"groq","label":"groq-a","key_hint":"gsk_ab12…","position":0,
  "status":"active","last_tested_at":"…","last_test_result":"ok",
  "state":{"blocked":true,"reason":"rate_limited","retry_after_s":9,"models":["llama-3.3-70b-versatile"]},
  "created_at":"…"}]}
```
`state` is read from `provider_key_state` via keystate. Sorted by provider priority, then `position`.

### `POST /api/keys` (owner/admin)

Body `{"provider":"groq","key":"gsk_…","label":"groq-a"}` (`model_id` still accepted for baseten). Valid providers: every registered adapter (`groq`, `openrouter`, `google`, `nvidia`, `together`, `baseten`; Phase 2 adds `anthropic`, `meshapi`; aliases `gemini`, `aifiesta`, `mesh` are accepted and normalised to `google` / `meshapi` (Assumed)). 201 returns the key record (no secret). Side effects: appended at the end of the provider's key order; provider appended to the org priority if new; catalog discovery for (org, provider) is triggered asynchronously. 409 duplicate label; 422 missing fields / invalid provider.

### `DELETE /api/keys/{id}` (owner/admin) — 204, sets `status='disabled'`.

### `POST /api/keys/{id}/test` (owner/admin)

Calls the adapter's `ListModels` with the key (no tokens spent). 200 `{"status":"ok","latency_ms":143,"model_count":21}` or `{"status":"error","class":"auth","message":"…"}`. On `ok`, sets `status='active'` (re-enables a key in `error`) and refreshes the catalog for that provider.

### `PATCH /api/keys/{id}` (owner/admin) — Phase 3

Body `{"label":"…","monthly_budget_usd":50}` (`null` clears budget).

### `POST /api/keys/{id}/reset-state` (owner/admin) — Phase 3

Deletes the key's `provider_key_state` rows (cooldown/credits/budget) and evicts the key-state cache. 204.

---

## Routing Priority — Phase 1

### `GET /api/routing/priority`

```json
{"providers":[
  {"provider":"groq","position":0,"keys":[{"id":"k1","label":"groq-a","position":0},{"id":"k2","label":"groq-b","position":1}]},
  {"provider":"openrouter","position":1,"keys":[{"id":"k3","label":"or-main","position":0}]}]}
```
Only providers with at least one non-disabled key are listed.

### `PUT /api/routing/priority` (owner/admin)

Body:
```json
{"providers":["openrouter","groq"],"key_order":{"groq":["k2","k1"]}}
```
- `providers` must be a permutation of the providers returned by GET (422 `invalid_priority` otherwise).
- `key_order` is optional per provider; when present it must be a permutation of that provider's non-disabled key ids (422 `invalid_key_order`).
- Applied in one transaction. 200 returns the GET body.

---

## Catalog

### `GET /api/catalog` — Phase 1 API, Phase 2 page

```json
{"models":[{"canonical":"llama-3.3-70b","kind":"chat","providers":[
   {"provider":"groq","provider_model_id":"llama-3.3-70b-versatile","source":"discovered","available":true,"last_seen_at":"…"},
   {"provider":"openrouter","provider_model_id":"meta-llama/llama-3.3-70b-instruct","source":"discovered","available":true,"last_seen_at":"…"}]}],
 "refreshes":[{"provider":"groq","last_refreshed_at":"…","last_status":"ok","model_count":21}],
 "aliases":[]}
```
Query `?q=llama` filters canonical names by substring; (P2) `?kind=chat|image|audio|embedding|other` filters by model kind (default: all kinds, so the dashboard can show and filter them; `/v1/models` is always chat-only). `kind` is one of those five values; `embedding` exists only as a filtered-out kind.

### `POST /api/catalog/refresh` (owner/admin) — Phase 1

Body optional `{"provider":"groq"}`; default all providers with keys. Runs discovery synchronously (30 s cap) with the provider's first active key. 200 returns `refreshes`. If a refresh is already running (in-process mutex) that provider is reported with `"last_status":"in_progress"`.

### Aliases — Phase 2

- `GET /api/aliases` → `{"aliases":[{"alias":"fast","canonical_model":"llama-3.3-70b","created_at":"…"}]}`
- `POST /api/aliases` (owner/admin) `{"alias":"fast","canonical_model":"llama-3.3-70b"}` → 201. Alias is lowercased; 409 if it exists; 422 if alias equals an existing canonical model name in the org catalog (aliases must not shadow real models). The target need not exist yet (resolved at request time).
- `DELETE /api/aliases/{alias}` (owner/admin) → 204.

---

## Request Log — Phase 1

### `GET /api/requests`

Query: `limit` (default 50, max 200), `before` (RFC3339 cursor on `created_at`), `status` (`success|error`), `model` (canonical), `token_id`.

```json
{"requests":[{"request_id":"r0-3f9c…","created_at":"…","token_name":"billing-worker",
  "canonical_model":"llama-3.3-70b","provider":"groq","provider_model_id":"llama-3.3-70b-versatile","key_label":"groq-b",
  "status":"success","http_status":200,"error_code":null,"stream":true,
  "latency_ms":812,"input_tokens":12,"output_tokens":7,"estimated_cost_usd":0.0000126,"attempt_count":2,
  "trail":[
    {"order":0,"provider":"groq","key_label":"groq-a","outcome":"error","class":"rate_limit","http_status":429,"latency_ms":95},
    {"order":1,"provider":"groq","key_label":"groq-b","outcome":"success","latency_ms":717}]}],
 "next_before":"…"}
```

### `GET /api/requests/{request_id}` — same object for one request; 404 for another org's request.

### `GET /api/costs` — existing endpoint, now grouped by org (provider, canonical model, key label) over the last 30 days.

`GET /api/pool` and `GET /api/attempts` are removed in Phase 1 (the trail is embedded in `/api/requests`).

---

## Admin (platform) — existing, adjusted in Phase 1

| Method | Path | Change |
|--------|------|--------|
| `POST` | `/admin/invites` | Body gains optional `org_id`, `org_role` (`admin`/`member`); without `org_id` the invitee gets a new org |
| `GET` | `/admin/users` | Each user gains `org: {id, name, role}` |
| `GET` | `/admin/orgs` | New: list orgs with member, token, key counts |
| `GET`/`POST` | `/admin/provider-keys`, `/admin/models`, `/admin/discovery`, `/admin/perf-test` | Unchanged (legacy global discovery) |
| `POST` | `/admin/pool/reset`, `GET /admin/pool/{user_id}` | Removed in Phase 1 (pools no longer exist) |

## Operational

- `GET /healthz` → `{"status":"ok","db":"ok"}` (database only; 503 with `"db":"error"` when Postgres is unreachable).
- `GET /metrics` → Prometheus; metric names in capabilities/observability.md.
