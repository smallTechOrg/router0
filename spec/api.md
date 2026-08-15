# API: Zer0 Router

All Go service endpoints use JSON request/response bodies. Dates are RFC3339. UUIDs are lowercase hyphenated strings.

Base URL (development): `http://localhost:8080`

---

## Authentication

### `POST /auth/register`

Register a new user using an invite token.

**Request body:**
```json
{
  "invite_token": "550e8400-e29b-41d4-a716-446655440000",
  "email": "user@example.com",
  "password": "hunter2-but-longer"
}
```

**Response 201:**
```json
{
  "user": {
    "id": "c47d9b2e-...",
    "email": "user@example.com",
    "role": "user",
    "status": "active",
    "created_at": "2026-08-15T10:00:00Z"
  },
  "zer0_key": "zer0:sk-3fGhJ8kLmNpQrStUvWxYzAbCdEfGhJk",
  "access_token": "<JWT>"
}
```

The `zer0_key` field is returned exactly once and never again. Store it immediately.

**Error responses:**
- 400: missing/invalid fields
- 409: invite already used
- 410: invite expired
- 422: password too short (< 12 chars)

---

### `POST /auth/login`

Authenticate with email and password.

**Request body:**
```json
{
  "email": "user@example.com",
  "password": "hunter2-but-longer"
}
```

**Response 200:**
```json
{
  "access_token": "<JWT, 15 min TTL>",
  "user": {
    "id": "c47d9b2e-...",
    "email": "user@example.com",
    "role": "user",
    "status": "active"
  }
}
```

Refresh token is set as an HTTP-only `Set-Cookie: refresh_token=<JWT>; HttpOnly; SameSite=Strict; Path=/auth/refresh`.

**Error responses:**
- 401: incorrect credentials (same message regardless of which field is wrong)

---

### `POST /auth/refresh`

Exchange refresh token cookie for a new access token.

**Request:** No body. Refresh token read from `refresh_token` cookie.

**Response 200:**
```json
{ "access_token": "<JWT, 15 min TTL>" }
```

**Error responses:**
- 401: missing or invalid refresh token

---

### `POST /auth/logout`

Invalidate the refresh token cookie.

**Response 204:** No body. Clears `refresh_token` cookie.

---

### `GET /auth/me`

Returns the authenticated user's profile.

**Headers:** `Authorization: Bearer <access_token>`

**Response 200:**
```json
{
  "id": "c47d9b2e-...",
  "email": "user@example.com",
  "role": "user",
  "status": "active",
  "zer0_key_prefix": "3fGhJ8kLmN",
  "created_at": "2026-08-15T10:00:00Z"
}
```

---

## Provider Key Management

All endpoints require `Authorization: Bearer <access_token>`.

### `GET /api/keys`

List the authenticated user's provider keys.

**Response 200:**
```json
{
  "keys": [
    {
      "id": "a1b2c3d4-...",
      "provider": "groq",
      "label": "My Groq key",
      "key_hint": "gsk_abcd…",
      "status": "active",
      "last_tested_at": "2026-08-15T09:55:00Z",
      "last_test_result": "ok",
      "created_at": "2026-08-10T08:00:00Z"
    }
  ]
}
```

---

### `POST /api/keys`

Add a new provider key.

**Request body:**
```json
{
  "provider": "groq",
  "key": "gsk_abcdefghijklmnop...",
  "label": "My Groq key"
}
```

`provider` must be one of: `google`, `groq`, `openrouter`, `nvidia`, `together`, `baseten`.

For `baseten`, an additional field `model_id` (string) is required.

**Response 201:**
```json
{
  "id": "a1b2c3d4-...",
  "provider": "groq",
  "label": "My Groq key",
  "key_hint": "gsk_abcd…",
  "status": "active",
  "created_at": "2026-08-15T10:05:00Z"
}
```

**Error responses:**
- 400: invalid provider
- 422: missing required fields

---

### `DELETE /api/keys/:id`

Soft-delete a provider key (set status to `disabled`, remove from all pools).

**Response 204:** No body.

**Error responses:**
- 404: key not found (or belongs to another user)

---

### `POST /api/keys/:id/test`

Fire a 1-token probe against the provider key.

**Response 200:**
```json
{
  "status": "ok",
  "latency_ms": 312
}
```

or on failure:

```json
{
  "status": "error",
  "message": "invalid_api_key"
}
```

The HTTP status of this endpoint is always 200 if the test itself could be attempted. The `status` field in the body indicates the provider result.

**Error responses:**
- 404: key not found

---

## Pool and Request Data

All endpoints require `Authorization: Bearer <access_token>`.

### `GET /api/pool`

Returns the authenticated user's current pool state.

**Response 200:**
```json
{
  "active": [
    {
      "provider": "groq",
      "model": "llama-3.1-70b-versatile",
      "provider_key_id": "a1b2c3d4-...",
      "score": 0.83,
      "p95_latency_ms": 421,
      "error_rate_1h": 0.02,
      "cost_per_token": 0.00000079,
      "quota": {
        "minute": { "remaining": 28, "limit": 30, "reset_in_s": 14 },
        "hour": { "remaining": 1800, "limit": 2000, "reset_in_s": 3612 },
        "day": { "remaining": 14000, "limit": 14400, "reset_in_s": 49200 }
      },
      "last_health_check_at": "2026-08-15T09:50:00Z",
      "pool": "active"
    }
  ],
  "common": [...]
}
```

---

### `GET /api/requests`

Returns the last 50 request metadata records for the authenticated user.

**Query params:**
- `limit` (int, default 50, max 50)

**Response 200:**
```json
{
  "requests": [
    {
      "id": "req-uuid-...",
      "zer0_request_id": "...",
      "provider": "groq",
      "model": "llama-3.1-70b-versatile",
      "pool": "active",
      "input_tokens": 42,
      "output_tokens": 118,
      "latency_ms": 437,
      "estimated_cost_usd": "0.00012538",
      "status": "success",
      "retry_count": 0,
      "created_at": "2026-08-15T10:02:31Z"
    }
  ]
}
```

---

### `GET /api/costs`

Returns aggregated cost data for the current calendar month (UTC).

**Response 200:**
```json
{
  "month": "2026-08",
  "by_provider": [
    {
      "provider": "groq",
      "request_count": 142,
      "input_tokens": 58420,
      "output_tokens": 163200,
      "estimated_cost_usd": "0.17534816"
    }
  ],
  "total_estimated_cost_usd": "0.17534816"
}
```

---

## Inference (OpenAI-Compatible)

### `POST /v1/chat/completions`

OpenAI-compatible chat completion endpoint. Accepts standard OpenAI request format. Supports streaming via `stream: true`.

**Headers:**
- `Authorization: Bearer zer0:sk-<key>` (required)
- `Content-Type: application/json`

**Request body:** Standard OpenAI `ChatCompletionRequest`. The router respects `model`, `messages`, `max_tokens`, `temperature`, `stream`, `stop`. Other fields are passed through to the provider where supported.

Example:
```json
{
  "model": "llama-3.1-70b-versatile",
  "messages": [
    {"role": "user", "content": "What is 2 + 2?"}
  ],
  "stream": true
}
```

**Note on `model` field:** If the requested model is available in the user's active pool, it is preferred. If not available, the router selects the best available combo regardless of model (transparency: the actual model is reported in response headers). If the user does not specify a `model` field, the top-scored combo is used.

**Response (non-streaming) 200:**
```json
{
  "id": "chatcmpl-zer0-...",
  "object": "chat.completion",
  "created": 1755216000,
  "model": "llama-3.1-70b-versatile",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "4"
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 24,
    "completion_tokens": 2,
    "total_tokens": 26
  }
}
```

**Response headers added by Zer0 Router:**
```
X-Zer0-Model: llama-3.1-70b-versatile
X-Zer0-Provider: groq
X-Zer0-Latency-Ms: 437
X-Zer0-Request-Id: zer0-req-550e8400-...
```

**Response (streaming) 200:**
`Content-Type: text/event-stream`
Standard OpenAI SSE format:
```
data: {"id":"chatcmpl-...","object":"chat.completion.chunk","choices":[{"delta":{"content":"4"},"index":0}]}

data: [DONE]
```

**Error responses:**
- 401: invalid or missing Zer0 API key
- 403: user account disabled
- 503: all providers failed (see below)

**Total failure (503):**
```json
{
  "error": {
    "message": "All available providers failed to fulfill this request.",
    "type": "gateway_error",
    "code": "all_providers_failed",
    "details": [
      {
        "provider": "groq",
        "model": "llama-3.1-70b-versatile",
        "error": "rate_limit_exhausted"
      },
      {
        "provider": "google",
        "model": "gemini-1.5-flash",
        "error": "upstream_error"
      }
    ]
  },
  "retry_after": 30
}
```

`Retry-After: 30` header is also set.

---

## Metrics

### `GET /metrics`

Prometheus metrics in text exposition format. No authentication in Phase 1.

**Response 200:**
```
Content-Type: text/plain; version=0.0.4; charset=utf-8

# HELP zer0_requests_total Total inference requests processed
# TYPE zer0_requests_total counter
zer0_requests_total{provider="groq",model="llama-3.1-70b-versatile",status="success"} 142
...
```

---

## Admin API

All Admin API endpoints require `Authorization: Bearer <access_token>` with `role = 'admin'`. Non-admin callers receive 403.

### `GET /admin/users`

List all users.

**Response 200:**
```json
{
  "users": [
    {
      "id": "...",
      "email": "user@example.com",
      "role": "user",
      "status": "active",
      "created_at": "2026-08-10T08:00:00Z"
    }
  ]
}
```

---

### `POST /admin/invites`

Create an invite.

**Request body:**
```json
{ "email": "newuser@example.com" }
```

**Response 201:**
```json
{
  "token": "550e8400-e29b-41d4-a716-446655440000",
  "email": "newuser@example.com",
  "expires_at": "2026-08-22T10:00:00Z"
}
```

---

### `GET /admin/pool/:user_id`

Inspect a user's full pool state (reads live Redis state).

**Response 200:** Same schema as `GET /api/pool` but for any user.

---

### `POST /admin/pool/:user_id/promote`

Force-promote a combo to the active pool.

**Request body:**
```json
{
  "provider": "groq",
  "model": "llama-3.1-70b-versatile",
  "key_id": "a1b2c3d4-..."
}
```

**Response 200:**
```json
{ "status": "promoted" }
```

---

### `POST /admin/pool/:user_id/demote`

Force-demote a combo from the active pool to common.

**Request body:** Same as promote.

**Response 200:**
```json
{ "status": "demoted" }
```

---

### `PUT /admin/keys/:key_id/disable`

Disable any provider key (across all users).

**Response 200:**
```json
{ "status": "disabled" }
```

---

## Common Error Format

All error responses use this envelope:

```json
{
  "error": {
    "message": "Human-readable explanation.",
    "type": "auth_error | validation_error | not_found | forbidden | gateway_error | internal_error",
    "code": "snake_case_code",
    "details": []
  }
}
```

| HTTP Status | `type` |
|-------------|--------|
| 400 | `validation_error` |
| 401 | `auth_error` |
| 403 | `forbidden` |
| 404 | `not_found` |
| 409 | `conflict` |
| 410 | `gone` |
| 422 | `validation_error` |
| 500 | `internal_error` |
| 503 | `gateway_error` |
