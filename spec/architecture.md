# Architecture: Zer0 Router

## System Overview

Zer0 Router is a two-process system: a Go HTTP gateway and a Next.js dashboard.

```
Browser
  │
  ├─► Next.js Dashboard (Next.js on Node.js)
  │       │  SSR + API routes proxy to Go service
  │       └──► Go Router Service (HTTP)
  │               │
  └─► Go Router Service  ◄── OpenAI-compatible client (user's app)
          │
          ├── PostgreSQL  (users, keys, request metadata, pool scores)
          └── Redis        (rate-limit counters, pool state, health cache)
```

Every inference request arrives at the Go service. The Next.js dashboard communicates with the Go service's internal API routes (prefixed `/api/` and `/admin/`) for all data.

The Go service and Next.js dashboard can run in separate containers or as two processes on the same host. They share no in-process state — all shared state lives in PostgreSQL and Redis.

## Data Flow: Inference Request

```
1. Client sends POST /v1/chat/completions
   Authorization: Bearer zer0:sk-<key>

2. Auth middleware
   - Extract display prefix from key
   - SELECT user by prefix → bcrypt compare
   - Attach user_id to request context

3. Routing Engine
   - Roll dice (80/20 active/common pool)
   - Load pool from Redis (ZRANGEBYSCORE)
   - Filter exhausted combos
   - Score and sort
   - Select top combo

4. Key decryption
   - SELECT ciphertext + nonce for key_id
   - AES-256-GCM decrypt in-memory

5. Provider Adapter
   - Translate ChatRequest → provider HTTP format
   - Open streaming SSE connection
   - Return chunk channel

6. Response streaming
   - Pipe chunks to client (chunked transfer encoding)
   - Add response headers:
       X-Zer0-Model: <model>
       X-Zer0-Provider: <provider>
       X-Zer0-Latency-Ms: <ms>

7. Metadata recording (async, post-stream)
   - INSERT request_metadata row (no prompt content)
   - INCR Redis rate-limit counters
   - Update Redis metrics hash
   - Emit structured log line

8. On failure → retry/hedge logic (see Routing Engine capability)
```

## Stack

| Component | Technology | Rationale |
|-----------|-----------|-----------|
| Router service | **Go 1.22+** | Low latency, high concurrency for streaming proxying; small container footprint; native goroutines for hedge parallelism and probe scheduler. User preference. |
| Dashboard | **Next.js 14 (App Router)** | Full-stack SSR + API routes in one repo; TypeScript type safety across client and server; user preference. |
| Database | **PostgreSQL 16** | Relational integrity for users/keys/metadata; JSONB if needed for provider-specific metadata; user preference. |
| Cache / shared state | **Redis 7** | Fast atomic counters for rate-limit tracking; sorted sets for pool scoring; TTL-aligned expiry for window resets; user preference. |
| Provider adapters | **Go interface + one file per provider** | Plugin pattern: new providers added without touching core routing. User preference. |
| Authentication (router) | **JWT (HS256)** via `golang-jwt/jwt` | Stateless auth for dashboard API; bcrypt for Zer0 key + password hashing. |
| Authentication (dashboard) | **NextAuth.js** | Handles session, CSRF, cookie management with the Credentials provider. |
| Encryption | **AES-256-GCM** via Go `crypto/aes` + `crypto/cipher` | Standard library, no extra dependencies; authenticated encryption. |
| HTTP router (Go) | **chi** | Lightweight, idiomatic Go router; middleware composable. |
| PostgreSQL driver (Go) | **pgx v5** | High-performance, non-ORM; named migrations with `golang-migrate`. |
| Redis client (Go) | **go-redis v9** | Official Redis client; supports pipelining for atomic counter updates. |
| Metrics | **prometheus/client_golang** | Standard Prometheus instrumentation; `/metrics` endpoint. |
| Logging | **log/slog** (Go standard library, 1.21+) | Structured JSON handler built-in; no external dependency. |
| CSS | **Tailwind CSS 3** | Utility-first, minimal bundle; specified for dashboard. |
| Migrations | **golang-migrate** | SQL-based, ordered migrations; runs at startup. |
| Container | **Docker + docker-compose** | Single-command local development: router + PostgreSQL + Redis. |

> **Assumed:** JWT secret is set via `JWT_SECRET` env var (min 32 bytes). The Go service does not use an ORM — raw pgx queries only. Dashboard uses `pnpm` as the package manager.

> **Assumed:** The Go service listens on `PORT` env var (default `8080`). The Next.js dashboard listens on port `3000`. In development, the dashboard proxies `/api` and `/admin` to `http://localhost:8080` via `next.config.ts` rewrites.

## Layout

```
zer0-router/
│
├── cmd/
│   └── router/
│       └── main.go                  # Entry point: wires server, DB, Redis, scheduler, metrics
│
├── internal/
│   ├── auth/
│   │   ├── middleware.go            # Zer0 key auth middleware (bcrypt lookup)
│   │   ├── jwt.go                   # JWT issue + validate (HS256)
│   │   └── keygen.go                # zer0:sk- key generation (base58, 32 chars)
│   │
│   ├── providers/
│   │   ├── provider.go              # Provider interface + ProviderError + ChatRequest/Response types
│   │   ├── registry.go              # map[string]Provider; registered at init
│   │   ├── google.go                # Google AI Studio adapter
│   │   ├── groq.go                  # Groq Cloud adapter
│   │   ├── openrouter.go            # OpenRouter adapter
│   │   ├── nvidia.go                # NVIDIA NIM adapter
│   │   ├── together.go              # Together AI adapter
│   │   └── baseten.go               # Baseten adapter
│   │
│   ├── routing/
│   │   ├── pool.go                  # Pool load/save (Redis sorted sets); promote/demote logic
│   │   ├── scorer.go                # Weighted scoring formula; combo eligibility filter
│   │   ├── selector.go              # Request routing: pool selection, retry, hedge
│   │   └── ratelimit.go             # Redis rl: counter INCR/GET; window expiry; exhaustion check
│   │
│   ├── health/
│   │   ├── probe.go                 # Minimal-token probe per provider; result cache in Redis
│   │   └── scheduler.go             # Goroutine pool; triggers probes on idle-key interval
│   │
│   ├── keys/
│   │   ├── crypto.go                # AES-256-GCM Encrypt / Decrypt
│   │   └── repository.go            # PostgreSQL CRUD for provider_keys
│   │
│   ├── handlers/
│   │   ├── inference.go             # POST /v1/chat/completions
│   │   ├── keys.go                  # GET/POST/DELETE /api/keys; POST /api/keys/:id/test
│   │   ├── pool.go                  # GET /api/pool; GET /api/requests; GET /api/costs
│   │   ├── auth.go                  # POST /auth/register; POST /auth/login; POST /auth/logout; GET /auth/me
│   │   └── admin.go                 # /admin/* endpoints
│   │
│   ├── metadata/
│   │   └── recorder.go              # Async INSERT to request_metadata; cost estimation
│   │
│   ├── metrics/
│   │   └── prometheus.go            # Register collectors; /metrics handler
│   │
│   └── db/
│       ├── postgres.go              # pgx pool setup; migration runner
│       └── redis.go                 # go-redis client setup
│
├── migrations/
│   ├── 000001_create_users.up.sql
│   ├── 000001_create_users.down.sql
│   ├── 000002_create_invites.up.sql
│   ├── 000002_create_invites.down.sql
│   ├── 000003_create_provider_keys.up.sql
│   ├── 000003_create_provider_keys.down.sql
│   ├── 000004_create_request_metadata.up.sql
│   ├── 000004_create_request_metadata.down.sql
│   └── 000005_create_pool_scores.up.sql
│   └── 000005_create_pool_scores.down.sql
│
├── dashboard/                        # Next.js app (App Router)
│   ├── app/
│   │   ├── layout.tsx
│   │   ├── page.tsx                  # Redirect: authenticated → /dashboard, else /auth/login
│   │   ├── auth/
│   │   │   ├── login/page.tsx
│   │   │   └── register/page.tsx
│   │   ├── dashboard/
│   │   │   ├── layout.tsx            # Sidebar nav; auth guard
│   │   │   ├── page.tsx              # Pool status (active + common pool tables)
│   │   │   ├── keys/page.tsx         # Provider key management
│   │   │   ├── requests/page.tsx     # Request feed (last 50)
│   │   │   └── costs/page.tsx        # Cost tracker table + stub chart
│   │   ├── admin/
│   │   │   └── invites/page.tsx      # Admin: invite management
│   │   └── api/
│   │       └── auth/
│   │           └── [...nextauth]/route.ts   # NextAuth Credentials provider
│   ├── components/
│   │   ├── PoolTable.tsx
│   │   ├── KeyList.tsx
│   │   ├── RequestFeed.tsx
│   │   ├── CostTable.tsx
│   │   ├── QuotaBar.tsx
│   │   ├── StatusBadge.tsx
│   │   └── StubChart.tsx             # "Coming soon" placeholder
│   ├── lib/
│   │   ├── api.ts                    # Typed fetch wrappers to Go service
│   │   └── auth.ts                   # NextAuth config
│   ├── next.config.ts                # Rewrites /api → :8080/api, /admin → :8080/admin
│   ├── tailwind.config.ts
│   ├── tsconfig.json
│   └── package.json
│
├── config/
│   ├── config.yaml                   # Default config: ports, probe intervals, pool thresholds
│   └── alerts.yaml                   # Sample Prometheus alerting rules
│
├── .env.example                      # All required env vars with placeholder values
├── docker-compose.yml                # router + postgres + redis
├── Makefile                          # make dev, make test, make test-integration, make migrate
└── spec/
    └── ...
```

## Conventions

### Error Shape (Go API responses)

All error responses use:

```json
{
  "error": {
    "message": "Human-readable description.",
    "type": "auth_error | validation_error | provider_error | gateway_error | internal_error",
    "code": "snake_case_machine_code",
    "details": [...]  // optional, per-field validation errors or per-provider failures
  }
}
```

HTTP status codes map predictably: 400 → validation_error, 401 → auth_error, 403 → forbidden, 404 → not_found, 409 → conflict, 422 → validation_error (form), 429 → rate_limit, 503 → gateway_error.

### Naming

- Go packages: lowercase single word or two words, no underscores (`handlers`, `routing`, `metadata`).
- Go types: PascalCase. Go functions and methods: camelCase for unexported, PascalCase for exported.
- PostgreSQL tables: `snake_case` plural (`users`, `provider_keys`, `request_metadata`, `pool_scores`).
- PostgreSQL columns: `snake_case`.
- Redis keys: `snake_case` with `:` separators (`rl:{user_id}:{provider}:{model}:{window}`).
- Environment variables: `SCREAMING_SNAKE_CASE`.
- Next.js components: PascalCase filenames (`PoolTable.tsx`).
- Next.js API routes: kebab-case path segments (Next.js convention).

### Logging Fields (slog)

All slog calls use structured key-value pairs, never format strings with embedded data. Example:

```go
slog.Info("request_completed",
    "event", "request_completed",
    "request_id", reqID,
    "user_id", userID,
    "provider", combo.Provider,
    "model", combo.Model,
    "latency_ms", latencyMs,
    "status", status,
)
```

Never pass sensitive values (keys, passwords, prompt content) to any slog call.

### Test Conventions

- Unit tests: `*_test.go` co-located with the package, no build tag, no network calls.
- Integration tests: `*_integration_test.go`, build tag `//go:build integration`, require real env vars from `.env`.
- Table-driven tests using `t.Run` sub-tests.
- Test helpers in `internal/<pkg>/testhelpers_test.go` (unexported, test-only).
- No mocking framework — use interface injection and inline test doubles.

### Provider Adapter Conventions

Each adapter file:
- Defines exactly one struct (e.g. `GroqProvider`).
- Implements the `Provider` interface.
- Contains no global state — all configuration passed via method parameters.
- Reads no environment variables directly — the decrypted key is passed in.
- Has a corresponding `*_test.go` with unit tests (mocked HTTP) and `*_integration_test.go` (real key).

### Sensitive Value Handling

- Provider API keys: decrypted to `string` in-memory inside `handlers/inference.go`; passed as a parameter; never stored in any struct that outlives the request handler goroutine.
- Zer0 API keys: only the bcrypt hash and the display prefix are ever in memory outside the single registration handler.
- No `fmt.Sprintf`/`fmt.Println` with key values anywhere in the codebase. Use structured `slog` only.
