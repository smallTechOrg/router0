# Architecture: router0

## System Overview

router0 is two processes plus one store (PostgreSQL):

```
Backend service (OpenAI SDK)            Browser (org member)
        │  Authorization: Bearer r0-sk-…        │
        ▼                                       ▼
┌──────────────────────────┐          ┌──────────────────────────┐
│ Go gateway (:8080)       │◄─────────│ Next.js dashboard (:3000)│
│  /v1/*   service tokens  │  /api/*  │  NextAuth session → JWT  │
│  /api/*  JWT (dashboard) │  /auth/* └──────────────────────────┘
│  /admin/* JWT + admin    │
└──────┬───────────┬───────┘
       │   in-process caches (token TTL cache, key-state cache, catalog-refresh mutexes)
       │
  PostgreSQL 14+ (only shared store)
  orgs, members, tokens, keys, priority, catalog, provider_key_state (cooldowns),
  request logs, (Phase 3) spend + balances
       │
       ▼
  Provider HTTP APIs: Groq, OpenRouter (Phase 1); Anthropic, Gemini, Mesh API (Phase 2);
  NVIDIA, Together, Baseten (existing adapters, not priorities)
```

The Go service and the dashboard share no in-process state. The dashboard calls the Go service's `/auth/*`, `/api/*`, and `/admin/*` routes through Next.js rewrites.

## Data Flow: Inference Request

```
1. POST /v1/chat/completions   Authorization: Bearer r0-sk-<32 base58>
   Optional header: X-Router0-Providers: groq,openrouter

2. Service-token middleware (internal/auth)
   - sha256(token) → process-local TTL cache (30 s) → else SELECT service_tokens
   - reject revoked → 401; attach org_id + token_id to ctx; async UPDATE last_used_at

3. Model resolution (internal/catalog)
   - canonical := Canonicalize(request.model)          (Phase 2: alias lookup first)
   - rows := org_model_catalog WHERE org_id AND canonical_model AND is_available
   - (Phase 2) no rows → on-demand refresh of the org's providers, retry once
   - still none → 404 model_not_found

4. Candidate plan (internal/routing)
   - provider order := X-Router0-Providers (filters + orders) OR org_provider_priority
   - for each provider that serves the model (one provider_model_id per provider):
       for each active key of that provider ordered by provider_keys.position:
           candidate{provider, provider_model_id, key_id, key_label}

5. Execute with failover (internal/routing + internal/keystate)
   - keystate.CheckMany(plan keys) — one SELECT on provider_key_state behind a 2 s in-memory cache
   - for each candidate in order (max 8 attempts):
       state → skip if rate_limited / credits_exhausted (transient_cooldown keys were moved to the end)
       decrypt key (AES-256-GCM) in-memory; call adapter Complete/Stream
       success → stop; error → providers' Classify → keystate.Mark → next candidate
   - streaming: failover allowed only until the first chunk has been written to the client

6. Response
   - success: OpenAI-shaped body/SSE; headers X-Router0-Request-Id, -Provider, -Model,
     -Canonical-Model, -Key, -Attempts, -Latency-Ms
   - exhausted: error precedence → 429 rate_limited | 402 insufficient_credits | 404 model_not_found
     | 400 upstream_rejected_request | 503 all_providers_failed (see capabilities/error-classification.md)

7. Metadata (async, internal/metadata) — never prompt/response content
   - INSERT request_metadata (org, token, canonical model, provider, key label, tokens, latency, status)
   - INSERT request_attempts per candidate (success / error / skipped + class)
   - Prometheus counters; one slog line
```

## Stack

The stack is kept (binding user decision: extend, do not rewrite), with one removal: Redis (binding user directive, see below).

| Component | Technology | Rationale |
|-----------|-----------|-----------|
| Gateway | **Go 1.22** (`github.com/zer0-router/zer0` module) | Already built; goroutines and `net/http` streaming suit an SSE proxy; one static binary. |
| HTTP router | **chi v5** | Already used; composable per-route middleware (service token vs JWT vs admin). |
| Dashboard | **Next.js 14 App Router + TypeScript + Tailwind 3**, `pnpm` | Already built; SSR pages and NextAuth in one app. |
| Dashboard auth | **NextAuth v4 Credentials** → Go `/auth/login` JWT | Already built; session holds the Go-issued JWT. |
| Gateway auth (humans) | **JWT HS256** (`golang-jwt/jwt/v5`) | Already built; gains an `org_id` claim. |
| Gateway auth (services) | **Service tokens, SHA-256 hashed**, process-local TTL cache | Tokens are 32 random base58 chars (~187 bits), so a fast hash is safe and avoids bcrypt's ~250 ms per request. Legacy bcrypt `zer0:sk-` keys are still verified (see data.md). |
| Database | **PostgreSQL 14+** via **pgx v5**, raw SQL, no ORM. Dev/test: the developer's local Homebrew `postgresql@14` on `127.0.0.1:5432` | Already used; the **only** shared store. Relational integrity for org → keys → catalog → logs; also holds key cooldown state (`provider_key_state`). All migrations use only PG ≤ 14 features (checked: no `MERGE`, no `NULLS NOT DISTINCT`, no SQL/JSON constructors; `gen_random_uuid()` is core since PG 13). |
| Migrations | **golang-migrate**, numbered SQL files run at startup | Already used; Phase 1 adds `000014`–`000019`. |
| In-process state | Go `sync.Map` / small TTL+LRU cache in `internal/cache` (no external dependency) | Token cache (30 s TTL, explicit invalidation on revoke), key-state read cache (2 s TTL, write-through), catalog-refresh mutexes and on-demand miss debounce (in memory). |
| Encryption | **AES-256-GCM** (stdlib) with `AES_KEY` env | Already built; provider keys decrypted only in memory per attempt. |
| Metrics | **prometheus/client_golang** | Already used; metric names renamed `router0_*`. |
| Logging | **log/slog** JSON | Already used. |
| Tests | stdlib `testing` + `net/http/httptest` mock providers, against local Postgres database `router0_test` | Phase 1 gate must run with no real provider keys and nothing but local Postgres. |
| Local infra | **Bare local**: `go run` + `pnpm dev` + local PG14 databases `router0` (dev) and `router0_test` (tests) | User directive: simpler local ops. `docker-compose.yml` is kept only as a deployment artifact (see below) and is not used for dev or test. |

**Why Redis was removed (binding user directive):** the user runs everything bare-local, without Docker. Every Redis use had a simple replacement. Rate-limit and credits cooldowns persist in Postgres (`provider_key_state`). Short-lived caches and locks live in process memory. The legacy pool/scorer counters are deleted along with the pool router. One fewer service to install, run, and back up. go-redis is removed from `go.mod` in Phase 1. Single instance is the target. With several instances behind a load balancer, correctness still holds because Postgres is the source of truth for key state and tokens. Assumed: up to a few seconds of key-state staleness across instances (the 2 s read cache) and up to 30 s for token revocation on *other* instances (the revoking instance invalidates immediately) are acceptable.

**Deployment artifact:** `docker-compose.yml` stays in the repo, documented in `README.md` as an optional deployment example only. Phase 1 (slice 1I) removes its Redis service. Assumed: it is not exercised by any gate.

Assumed: the dev databases are owned by the current OS user with no password: `DATABASE_URL=postgres://$(whoami)@127.0.0.1:5432/router0?sslmode=disable` for dev and `…/router0_test…` for tests, created with `createdb router0` / `createdb router0_test`. The user is spelled out explicitly in URLs so both pgx and golang-migrate's lib/pq driver resolve it the same way.

Assumed: the Go module path `github.com/zer0-router/zer0` and the legacy column name `request_metadata.zer0_request_id` are internal identifiers and are NOT renamed in Phase 1, to avoid a churn-only diff that would touch every file. Everything user-visible (token prefix, headers, metric names, error types, dashboard branding, logs `service` field, OpenRouter attribution headers) is renamed to router0 in Phase 1. A module-path rename may be done as a separate mechanical change after Phase 3.

Assumed: `GO111MODULE=on` must be set for every Go command (asdf sets it off globally on the dev machine — NOTES.md).

Assumed: `DATABASE_URL` uses `127.0.0.1`, not `localhost` (IPv6 `::1` is refused — NOTES.md).

## Layout

Existing files are kept; **new** marks Phase 1 additions, **(P2)/(P3)** later phases, **legacy** marks code taken off the request path in Phase 1 and deleted in Phase 3.

```
router0/
├── cmd/
│   ├── router/main.go                 # env, DB, migrations; builds server.New(deps); background jobs
│   ├── mockprovider/main.go           # new: runs testutil/mockprovider on :9901 (groq) / :9902 (openrouter) for keyless manual testing
│   └── loadtest/main.go               # existing
├── internal/
│   ├── server/                        # new: router.go — single chi route table used by main.go AND tests
│   ├── auth/
│   │   ├── middleware.go              # ServiceTokenMiddleware (new), JWTMiddleware (+org_id), AdminOnly, RequireOrgRole (new)
│   │   ├── jwt.go                     # Claims gain OrgID
│   │   └── keygen.go                  # GenerateServiceToken() → r0-sk-…
│   ├── orgs/                          # new: repository.go (orgs, org_members)
│   ├── tokens/                        # new: repository.go, hash.go (sha256 + legacy bcrypt verify)
│   ├── catalog/                       # new: canonical.go, repository.go, discovery.go, resolver.go
│   │                                  #      (P2) aliases.go, ondemand.go  (P3) embedding_discovery.go
│   ├── keystate/                      # new: provider_key_state (Postgres) + 2 s in-memory cache  (P3) budget.go
│   ├── cache/                         # new: tiny generic TTL+LRU cache (sync.Map-based), no deps
│   ├── routing/
│   │   ├── plan.go                    # new: candidate plan from catalog rows + priority + key order + header
│   │   ├── executor.go                # new: ordered failover loop, generic over an attempt func
│   │   ├── outcome.go                 # new: final error precedence (429 > 402 > 404 > 400 > 503)
│   │   └── pool.go scorer.go selector.go promoter.go tier.go meta_syncer.go
│   │       ratelimit.go ratelimit_observer.go remediate.go   # legacy pool router: DELETED in Phase 1
│   ├── providers/
│   │   ├── provider.go                # Provider interface; ProviderError gains Class set, RetryAfter, Code
│   │   ├── classify.go                # new: generic OpenAI-style HTTP→class rules shared by adapters
│   │   ├── registry.go                # ValidProviders() derived from registered adapters
│   │   ├── openai_compat.go           # shared OpenAI wire client; uses Classify; stream_options.include_usage
│   │   ├── groq.go openrouter.go      # Phase 1 priority adapters (+ per-provider classify overrides)
│   │   ├── google.go                  # Gemini (P2: tool-calling conformance, classify)
│   │   ├── anthropic.go anthropic_translate.go anthropic_stream.go   # (P2) native Messages API
│   │   ├── meshapi.go                 # (P2) Mesh API by Fiesta Labs, OpenAI-compatible
│   │   ├── embed_*.go                 # (P3) Embedder implementations
│   │   ├── openrouter_balance.go      # (P3) /key + /credits polling client
│   │   └── nvidia.go together.go baseten.go discovery.go model_metadata.go   # existing, not priorities
│   ├── handlers/
│   │   ├── inference.go               # POST /v1/chat/completions (rewritten on routing.Executor)
│   │   ├── catalog.go                 # new: GET /v1/models, GET /api/catalog, POST /api/catalog/refresh
│   │   ├── tokens.go                  # new: /api/tokens
│   │   ├── priority.go                # new: /api/routing/priority
│   │   ├── keys.go                    # /api/keys (org-scoped, state, position)
│   │   ├── pool.go                    # /api/requests, /api/requests/{id}, /api/costs (org-scoped); /api/pool removed
│   │   ├── auth.go admin.go helpers.go
│   │   ├── aliases.go                 # (P2)
│   │   └── embeddings.go              # (P3)
│   ├── balance/                       # (P3) poller goroutine
│   ├── metadata/recorder.go           # request + attempt records (org, token, key label, trail)
│   ├── metrics/prometheus.go          # router0_* metrics
│   ├── keys/ crypto.go repository.go priority.go  # org-scoped provider key CRUD + position; org_provider_priority
│   ├── models/                        # legacy global admin discovery (pool/cache deps stripped in Phase 1)
│   ├── health/                        # legacy probes: DELETED in Phase 1
│   ├── db/                            # pgx pool, migration runner (cache client file deleted in Phase 1)
│   ├── testutil/mockprovider/         # new: scripted OpenAI-compatible mock provider (P2: anthropic, gemini wire)
│   └── integration/                   # phase1_test.go, smoke_test.go (new); gateway_test.go legacy (deleted Phase 1)
├── migrations/                        # 000001–000013 existing; 000014–000019 Phase 1; 000020 P2; 000021–000022 P3
├── dashboard/
│   ├── app/dashboard/
│   │   ├── page.tsx                   # Overview (replaces pool status)
│   │   ├── tokens/page.tsx            # new: service tokens
│   │   ├── keys/page.tsx              # provider keys + key order + state badges
│   │   ├── routing/page.tsx           # new: global provider priority
│   │   ├── requests/page.tsx          # request log with failover trail
│   │   ├── catalog/page.tsx           # (P2) canonical model → providers
│   │   ├── costs/page.tsx  admin/     # existing
│   ├── components/  lib/api.ts  lib/auth.ts
├── config/ Makefile .env.example README.md NOTES.md
├── docker-compose.yml                 # deployment example only (Postgres + router); not used for dev/test
└── spec/
```

## Phase 1 Internal Contracts

These signatures let Phase 1 slices be built in parallel. A slice may add methods but must not change these.

```go
// internal/auth
type Principal struct{ OrgID, TokenID, TokenName string }
func ServiceTokenMiddleware(v TokenVerifier) func(http.Handler) http.Handler
func PrincipalFromCtx(ctx context.Context) (Principal, bool)
func OrgIDFromCtx(ctx context.Context) string            // set by JWTMiddleware for dashboard routes
func RequireOrgRole(roles ...string) func(http.Handler) http.Handler
type TokenVerifier interface{ Verify(ctx context.Context, rawToken string) (Principal, error) } // tokens.ErrInvalid / tokens.ErrRevoked

// internal/catalog
func Canonicalize(provider, modelID string) string        // provider "" = caller-supplied name
type Entry struct{ Canonical, Provider, ProviderModelID, Kind string }
type Resolver interface{ Resolve(ctx context.Context, orgID, requested string) ([]Entry, error) } // nil, nil = not found
type Discoverer interface{ RefreshProvider(ctx context.Context, orgID, provider string) (int, error) }
func (r *Repository) MarkUnavailable(ctx context.Context, orgID, provider, providerModelID string) error

// internal/orgs
func (r *Repository) OrgForUser(ctx context.Context, userID string) (orgID, role string, err error)

// internal/keys  (owns provider_keys AND org_provider_priority)
func (r *Repository) ActiveByOrgProvider(ctx context.Context, orgID, provider string) ([]*ProviderKey, error) // ordered by position
func (r *Repository) MarkAuthError(ctx context.Context, keyID string) error
type PriorityStore interface{ ProviderOrder(ctx context.Context, orgID string) ([]string, error) } // ranked, then alphabetical remainder

// internal/providers
type ErrorClass string // rate_limit | insufficient_credits | auth | model_unavailable | bad_request | transient
type ProviderError struct{ Class ErrorClass; HTTP int; Code string; RetryAfter time.Duration; Message string }
type Classifier interface{ Classify(status int, h http.Header, body []byte) *ProviderError } // optional per adapter

// internal/keystate
type State struct{ Blocked bool; Reason string /* rate_limited|credits_exhausted|transient_cooldown|budget_exhausted */; RetryAfter time.Duration }
type KeyModel struct{ KeyID, ProviderModelID string }
type Store interface { // Postgres provider_key_state + 2 s in-memory read cache, write-through
    Check(ctx context.Context, keyID, providerModelID string) (State, error)
    CheckMany(ctx context.Context, pairs []KeyModel) (map[KeyModel]State, error) // one SELECT per request
    Mark(ctx context.Context, keyID, providerModelID string, e *providers.ProviderError) error
}

// internal/cache
func New[K comparable, V any](ttl time.Duration, maxEntries int) *TTL[K, V] // Get, Set, Delete

// internal/routing
type Candidate struct{ Provider, ProviderModelID, KeyID, KeyLabel string }
func BuildPlan(entries []catalog.Entry, providerOrder []string, override []string, keysByProvider map[string][]*keys.ProviderKey) []Candidate
type AttemptFunc func(ctx context.Context, c Candidate, apiKey string) error // nil = success, *providers.ProviderError = classified failure
type AttemptRecord struct{ Candidate; Order int; Outcome string /* success|error|skipped */; Class string; HTTP int; RetryAfter time.Duration; LatencyMs int64 }
type Result struct{ Winner *Candidate; Attempts []AttemptRecord }
func (e *Executor) Run(ctx context.Context, plan []Candidate, fn AttemptFunc) Result
func FinalError(r Result) *GatewayError // {HTTP, Type, Code, Message, RetryAfterSeconds}
```

## Conventions

### Error shape

Inference routes (`/v1/*`) use the OpenAI error shape so SDKs parse it, plus a `router0` extension object:

```json
{
  "error": {
    "message": "Human-readable description.",
    "type": "invalid_request_error | authentication_error | insufficient_credits | rate_limit_error | upstream_error",
    "param": "model",
    "code": "model_not_found | invalid_api_key | insufficient_credits | rate_limited | upstream_rejected_request | all_providers_failed | invalid_provider_override",
    "router0": { "request_id": "r0-…", "attempts": [ { "provider": "groq", "key": "groq-a", "outcome": "error", "class": "rate_limit" } ] }
  }
}
```

Dashboard/admin routes (`/api/*`, `/auth/*`, `/admin/*`) keep the existing shape `{"error":{"message","type","code"}}` with types `auth_error | validation_error | forbidden | not_found | conflict | internal_error`.

### Naming

- Go packages: lowercase single word (`catalog`, `keystate`, `tokens`, `orgs`).
- Postgres tables: `snake_case` plural; columns `snake_case`; every org-owned table has `org_id UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE`.
- HTTP headers added by router0: `X-Router0-*`. Request id format `r0-<32 hex>`.
- Metrics: `router0_*`.
- Env vars: `SCREAMING_SNAKE_CASE`; new router0 tunables prefixed `ROUTER0_`.
- Next.js components PascalCase; route segments kebab-case.

### Configuration (new env vars, all optional)

| Var | Default | Meaning |
|-----|---------|---------|
| `ROUTER0_MAX_ATTEMPTS` | `8` | Max candidates attempted per request |
| `ROUTER0_ATTEMPT_TIMEOUT_SECONDS` | `120` | Non-streaming per-attempt timeout |
| `ROUTER0_STREAM_FIRST_BYTE_TIMEOUT_SECONDS` | `20` | Streaming: time allowed until first upstream chunk |
| `ROUTER0_DEFAULT_RATE_LIMIT_COOLDOWN_SECONDS` | `30` | Cooldown when a 429 carries no reset hint |
| `ROUTER0_CREDITS_COOLDOWN_SECONDS` | `3600` | How long a credits-exhausted key is skipped |
| `ROUTER0_TRANSIENT_COOLDOWN_SECONDS` | `10` | Cooldown after a transient (5xx/timeout/network) error |
| `ROUTER0_CATALOG_REFRESH_HOURS` | `6` | Periodic catalog re-discovery per org+provider |
| `ROUTER0_TOKEN_CACHE_TTL_SECONDS` | `30` | In-process service-token cache TTL (max revocation lag on other instances) |
| `ROUTER0_KEYSTATE_CACHE_MS` | `2000` | In-process key-state read cache TTL |
| `ANTHROPIC_BASE_URL` (P2) | `https://api.anthropic.com/v1` | |
| `MESHAPI_BASE_URL` (P2) | `https://api.meshapi.ai/v1` | |

Existing `GROQ_BASE_URL`, `OPENROUTER_BASE_URL`, `GOOGLE_BASE_URL`, etc. remain and are read at call time, which is how tests point adapters at `httptest` servers. Every adapter call (including `ListModels`) must use its base URL; the hard-coded `https://openrouter.ai/api/v1/models` in `openrouter.go` is a Phase 1 fix.

### Logging

Structured `slog` key-values only. Base field `service: "router0"`. One `request_completed` line per inference request with `request_id, org_id, token_id, canonical_model, provider, provider_model_id, key_label, attempts, status, error_code, latency_ms, input_tokens, output_tokens`. Never log: provider keys, router0 tokens (not even prefixes beyond 12 chars), prompts, responses, raw provider error bodies.

### Sensitive values

- Provider keys: decrypted per attempt inside the attempt func, passed as a parameter, never stored in a struct that outlives the attempt.
- Service tokens: full value returned once at creation; only `sha256` hex and a 12-char display prefix are stored.
- Provider error bodies: may be returned to the calling service inside `error.message` for `400 upstream_rejected_request`, but are never persisted. `request_attempts.error_message` stores only the provider's structured error `code`/`type`, truncated to 200 chars.
- No prompt or response content is persisted anywhere, including logs and in-memory caches.

### Tests

- Unit tests `*_test.go` beside the code, no network except `httptest`, table-driven, no mocking framework.
- Integration tests in `internal/integration/`, build tag `integration`, need only the local Postgres database `router0_test` (`DATABASE_URL`); providers are `internal/testutil/mockprovider` servers selected by `*_BASE_URL` env vars set with `t.Setenv`.
- Unit tests that need Postgres (e.g. `keystate`) use `DATABASE_URL` when set and `t.Skip` otherwise; the gate always sets it.
- Real-provider smoke tests use build tags `integration smoke` and `t.Skip` per provider when its `TEST_*_KEY` env var is empty.
- Integration tests create a fresh org per test (unique email) and never truncate shared tables. Each test builds its own `server.New` instance, so in-memory caches are per test.
- The metadata recorder is async: tests poll `GET /api/requests` for up to 3 s.

### Provider adapters

- One adapter type per provider; it registers itself in its own file's `init()` so adding a provider touches no shared file.
- Adapters read only their base URL from env; the decrypted key is a parameter.
- Adapters that need provider-specific error rules implement `providers.Classifier`; otherwise the shared OpenAI-style rules in `classify.go` apply.
