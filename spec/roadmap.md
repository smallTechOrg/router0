# Roadmap: router0

## Goal and Scope

Turn the existing Zer0 Router (Go 1.22 + Next.js 14 + Postgres + Redis) into **router0**: an org-scoped, OpenAI-compatible gateway with BYOK keys, many revocable service tokens, canonical-model resolution, fixed provider priority with key order and header override, failover on any error, and three distinct actionable errors (404 / 402 / 429). The stack is extended, not rewritten, except that Redis is removed and everything runs bare-local against the developer's Postgres 14 (binding user directive; see architecture.md › Stack).

**Already exists (Scaffold done, commit db85a19):** the Go binary with chi routes, JWT + invite auth, AES-GCM provider keys, six OpenAI-compatible adapters, a pool/scorer router that ignores the requested model, `request_metadata` + `request_attempts`, and a dashboard with login/keys/requests/costs/admin pages. `internal/integration/gateway_test.go` does not compile (`NewAdminHandler` arity) and is replaced in Phase 1.

**Out of scope (all phases):** storing prompt/response content; smart/semantic/learned routing; billing or markup; email delivery (invites stay on-screen); OAuth/SSO; multi-org membership per user; image/audio inputs; fine-tuning/hosting; conversation storage; **embeddings API (`/v1/embeddings`; no provider consensus on a standard, and Claude has no embeddings API)**; image/TTS/music/transcription endpoints (such models are discovered only so they can be filtered out of `/v1/models`).

### Common environment for every gate

All gates run from the repo root, bare-local: the developer's Homebrew `postgresql@14` on `127.0.0.1:5432`, test database `router0_test`, `GO111MODULE=on`, **no real provider keys** (mock providers via `httptest`), and no other services. The block below is referred to as `GATE_ENV`:

```bash
pg_isready -h 127.0.0.1 -p 5432 && \
(createdb router0_test 2>/dev/null || true) && \
export GO111MODULE=on \
  DATABASE_URL="postgres://$(whoami)@127.0.0.1:5432/router0_test?sslmode=disable" \
  AES_KEY='000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f' \
  JWT_SECRET='router0-gate-jwt-secret-0123456789abcdef'
```

Assumed: `router0_test` (and the dev database `router0`, created once with `createdb router0`) are owned by the current OS user with no password. The integration tests run migrations themselves (`db.RunMigrations`) and create unique orgs/users per test, so `router0_test` is never wiped between runs. `(createdb … || true)` makes the step idempotent; the parentheses keep a failing `pg_isready` from being swallowed by `|| true`.

---

## Phase 1: Org-scoped gateway with canonical models and failover (quick win)

**Goal:** A user registers (org created), adds Groq and OpenRouter keys (mock or real), sets provider priority and key order, creates/revokes service tokens in the dashboard, and calls `/v1/chat/completions` (streaming and not) with a canonical model name. They see failover across keys and providers, get `404 model_not_found` / `402 insufficient_credits` / `429 rate_limited` + `Retry-After` when everything is exhausted, and see each request's provider, key, latency, tokens, status, and failover trail in the request log. Everything user-visible is named router0.

Capabilities: auth-onboarding, service-tokens, provider-key-management, model-catalog (discovery for Groq + OpenRouter), routing-engine, error-classification, provider-adapters (Groq + OpenRouter), dashboard (Phase 1 pages), observability.

### Slices (owned paths are disjoint)

| Slice | Owns (exclusive write access) | Depends on |
|-------|-------------------------------|------------|
| **1A Schema** | `migrations/000014_create_orgs.{up,down}.sql`, `migrations/000015_create_service_tokens.{up,down}.sql`, `migrations/000016_org_provider_routing.{up,down}.sql`, `migrations/000017_org_model_catalog.{up,down}.sql`, `migrations/000018_request_log_org.{up,down}.sql`, `migrations/000019_provider_key_state.{up,down}.sql` | — (runs first; small) |
| **1B Orgs, auth & tokens** | `internal/auth/`, `internal/orgs/`, `internal/tokens/`, `internal/handlers/auth.go`, `internal/handlers/tokens.go`, `internal/handlers/admin.go` | 1A |
| **1C Catalog** | `internal/catalog/`, `internal/handlers/catalog.go` | 1A; existing `providers.Provider.ListModels` |
| **1D Keys & priority** | `internal/keys/`, `internal/handlers/keys.go`, `internal/handlers/priority.go` | 1A; contracts `catalog.Discoverer`, `keystate.Store` (injected) |
| **1E Adapters, classification, key state & cache** | `internal/providers/`, `internal/keystate/` (Postgres `provider_key_state` + in-memory cache), `internal/cache/` | 1A (000019) for keystate tests |
| **1F Routing & inference** | `internal/routing/` (incl. deleting the legacy Redis-based files), `internal/handlers/inference.go`, `internal/handlers/helpers.go`, `internal/metadata/`, `internal/metrics/`, `config/alerts.yaml` | contracts from 1B, 1C, 1D, 1E (architecture.md › Phase 1 Internal Contracts) |
| **1G Request log API** | `internal/handlers/pool.go` | 1A |
| **1H Dashboard** | `dashboard/` | api.md (may build against stubbed responses) |
| **1I Wiring, mocks, Redis removal & gate** | `internal/server/`, `cmd/router/main.go`, `cmd/mockprovider/`, `internal/testutil/`, `internal/integration/`, `internal/db/redis.go` (delete), `internal/health/` (delete), `internal/models/`, `go.mod`, `go.sum`, `docker-compose.yml`, `Makefile`, `.env.example`, `README.md` | all others (integrates last) |

Order: 1A first → 1B, 1C, 1D, 1E, 1G, 1H in parallel → 1F (can start in parallel against the contracts and integrates once 1B–1E land) → 1I.

Not owned by any Phase 1 slice and not to be edited: `internal/db/postgres.go`, `internal/db/db_test.go`, `migrations/000001`–`000013`, `spec/`, `NOTES.md`.

Redis removal in Phase 1 is spread across owners: 1B removes the redis client from `handlers/admin.go`; 1F removes it from `handlers/inference.go` and deletes the legacy `internal/routing/{pool,scorer,selector,promoter,tier,meta_syncer,ratelimit,ratelimit_observer,remediate}.go`; 1I deletes `internal/db/redis.go` and `internal/health/`, strips `routing.Pool`/redis from `internal/models/discovery.go`, drops `REDIS_URL` from `main.go`/`.env.example`, removes the Redis service from `docker-compose.yml` (now documented in `README.md` as a deployment example only), and runs `go mod tidy` so `github.com/redis/go-redis/v9` leaves `go.mod`.

### Key files per slice

- **1A:** six migration pairs exactly as in data.md, including backfills (one org per existing user, legacy tokens, `provider_keys.org_id/position`, priority rows) and `provider_key_state`. PG 14-compatible SQL only.
- **1B:** `internal/auth/middleware.go` (`ServiceTokenMiddleware`, `JWTMiddleware` + `org_id`, `RequireOrgRole`, OpenAI-shaped 401 for `/v1`), `internal/auth/jwt.go` (`OrgID` claim), `internal/auth/keygen.go` (`GenerateServiceToken`, unbiased base58), `internal/tokens/repository.go`, `internal/tokens/verifier.go` (sha256 + legacy bcrypt + 30 s in-process cache via `internal/cache`, evicted on revoke), `internal/orgs/repository.go`, `internal/handlers/tokens.go`, `internal/handlers/auth.go` (transactional register creating org, no key), `internal/handlers/admin.go` (invites with `org_id`, `/admin/orgs`, remove pool endpoints and the `routing.Pool` constructor dependency). Unit tests: keygen format/distribution, verifier cache-hit path, legacy bcrypt path.
- **1C:** `internal/catalog/canonical.go` + `canonical_test.go` (fixture table from model-catalog.md), `internal/catalog/overrides.go`, `internal/catalog/repository.go` (upsert/mark-unavailable/list), `internal/catalog/discovery.go` (`RefreshProvider`, in-process per-(org,provider) mutex, key fallback on auth), `internal/catalog/resolver.go`, `internal/catalog/scheduler.go` (6-hourly refresh loop, started by 1I), `internal/handlers/catalog.go` (`GET /v1/models`, `GET /api/catalog`, `POST /api/catalog/refresh`).
- **1D:** `internal/keys/repository.go` (org-scoped CRUD, `position`, `ActiveByOrgProvider`, `MarkAuthError`), `internal/keys/priority.go` (`PriorityStore`, transactional replace), `internal/handlers/keys.go` (`/api/keys*`, `/api/providers`, `state` from keystate, discovery trigger; drop `AddComboToAll`), `internal/handlers/priority.go` (`/api/routing/priority`).
- **1E:** `internal/providers/provider.go` (extended types: `ErrorClass` set, `ProviderError{Code,RetryAfter}`, `ChatChunk{Usage,Err}`, `*float64` temperature, tool-call `Index`), `internal/providers/classify.go` + `classify_test.go` (generic rules, Retry-After parsing), `internal/providers/openai_compat.go` (classifier use, shared transport, no 15 s client timeout, `include_usage`, 1 MiB scanner, SSE error events), `internal/providers/groq.go` / `openrouter.go` (Classifier overrides, base-URL `ListModels`, drop `:free` filter, router0 attribution headers), `internal/providers/registry.go` (per-file `init`, derived `ValidProviders`), `internal/keystate/store.go` + `store_test.go` (Postgres `provider_key_state` `Check`/`CheckMany`/`Mark`/`Sweep` with a 2 s write-through in-memory cache; tests use `DATABASE_URL` when set, else skip), `internal/cache/ttl.go` + `ttl_test.go` (generic TTL+LRU, injectable clock).
- **1F:** `internal/routing/plan.go`, `internal/routing/executor.go`, `internal/routing/outcome.go` + table tests (fake AttemptFuncs, fake keystate), `internal/handlers/inference.go` (rewritten: parse → resolve → plan → execute → respond; stream commit-on-first-chunk; `X-Router0-*` headers; removes the `ListModels` func), `internal/handlers/helpers.go` (`writeOpenAIError`), `internal/metadata/recorder.go` (org/token/label/trail fields, buffered async writer, no raw bodies), `internal/metrics/prometheus.go` (`router0_*`), `config/alerts.yaml`.
- **1G:** `internal/handlers/pool.go` → `GET /api/requests` (filters, cursor, `trail`, `token_name`), `GET /api/requests/{id}`, `GET /api/costs` org-scoped; delete `GetPool`/`GetAttempts` and the `routing.Pool` constructor dependency. (1D likewise drops `routing.Pool` from `NewKeysHandler`.)
- **1H:** `dashboard/app/dashboard/layout.tsx` (nav, brand), `page.tsx` (overview), `tokens/page.tsx`, `keys/page.tsx`, `routing/page.tsx`, `requests/page.tsx`, `admin/page.tsx` (invite org select), `app/auth/register/page.tsx` (org name; redirect to tokens), `components/TokenCreatedModal.tsx`, `components/FailoverTrail.tsx`, `components/KeyStateBadge.tsx`, `lib/api.ts` (typed wrappers for every Phase 1 endpoint), `package.json` name `router0-dashboard`; remove "Zer0" strings.
- **1I:** `internal/server/router.go` (`server.New(Deps) http.Handler`: the single route table), `cmd/router/main.go` (uses `server.New`; no `REDIS_URL`; no legacy scheduler/promoter/meta-syncer; starts catalog scheduler and hourly `keystate.Sweep`; `/healthz` reports db only; `service: router0` log field; admin bootstrap creates org), `internal/testutil/mockprovider/mockprovider.go` (scripted OpenAI-compatible server; behaviour selected by bearer key: `mock-ok*`, `mock-429-<secs>*`, `mock-402*`, `mock-401*`, `mock-500*`, `mock-midstream*`; configurable `/models` list; per-key call counters; error bodies contain `SENTINEL-BODY`), `cmd/mockprovider/main.go` (serves Groq-flavoured mock on `:9901` and OpenRouter-flavoured on `:9902`), `internal/integration/phase1_test.go`, `internal/integration/smoke_test.go` (`//go:build integration && smoke`), delete `internal/integration/gateway_test.go`, `Makefile` targets `gate-phase1`, `smoke`, `mock`, `db` (`createdb router0; createdb router0_test`, idempotent), and `dev`/`test` without `source .env` requirements for Redis, `.env.example` (`DATABASE_URL=postgres://<you>@127.0.0.1:5432/router0?sslmode=disable`, router0 vars, `TEST_OPENROUTER_KEY`, no Redis vars), `README.md` (bare-local quick start; docker-compose described as a deployment example only).

### Integration tests required in `internal/integration/phase1_test.go` (all mock-only)

`TestPhase1_RegisterCreatesOrg`, `TestPhase1_TokenLifecycle` (create → use → revoke → 401), `TestPhase1_LegacyTokenAccepted`, `TestPhase1_CatalogDiscovery`, `TestPhase1_PriorityOrder`, `TestPhase1_KeyOrderFailover`, `TestPhase1_ProviderFailover`, `TestPhase1_HeaderOverride`, `TestPhase1_Streaming`, `TestPhase1_StreamFailoverBeforeFirstByte`, `TestPhase1_StreamInterrupted`, `TestPhase1_ModelNotFound`, `TestPhase1_InsufficientCredits`, `TestPhase1_RateLimitedRetryAfter`, `TestPhase1_MixedPrecedence`, `TestPhase1_CooldownSkips`, `TestPhase1_CooldownPersistsAcrossRestart` (a second `server.New` instance on the same DB skips the key marked by the first, proving Postgres is the source of truth), `TestPhase1_RevokeInvalidatesCacheImmediately`, `TestPhase1_AuthErrorDisablesKey`, `TestPhase1_RequestLogTrail`, `TestPhase1_NoContentStored`, `TestPhase1_OrgIsolation`, `TestPhase1_MetricsRenamed`. Each one implements the matching success criteria in the capability files.

### Gate (exact command)

```bash
pg_isready -h 127.0.0.1 -p 5432 && \
(createdb router0_test 2>/dev/null || true) && \
export GO111MODULE=on \
  DATABASE_URL="postgres://$(whoami)@127.0.0.1:5432/router0_test?sslmode=disable" \
  AES_KEY='000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f' \
  JWT_SECRET='router0-gate-jwt-secret-0123456789abcdef' && \
go build ./... && \
go vet ./... && \
go vet -tags 'integration smoke' ./internal/integration/... && \
go test ./internal/... -count=1 && \
go test -tags integration ./internal/integration/... -run 'TestPhase1' -count=1 -v && \
! grep -q 'go-redis' go.mod && \
! grep -rIqi 'zer0' dashboard/app dashboard/components && \
(cd dashboard && pnpm install --frozen-lockfile && pnpm run build)
```

`make gate-phase1` runs the same command. It needs no provider keys, no Docker, and no Redis; only the local Postgres 14 must be running (`brew services start postgresql@14`). The gate passes only if every `TestPhase1_*` runs (none skipped) and passes.

### Optional real-key smoke (not part of the gate)

```bash
(createdb router0_test 2>/dev/null || true) && \
GO111MODULE=on \
DATABASE_URL="postgres://$(whoami)@127.0.0.1:5432/router0_test?sslmode=disable" \
AES_KEY='000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f' \
JWT_SECRET='router0-gate-jwt-secret-0123456789abcdef' \
TEST_GROQ_KEY="$TEST_GROQ_KEY" TEST_OPENROUTER_KEY="$TEST_OPENROUTER_KEY" \
go test -tags 'integration smoke' ./internal/integration/... -run 'TestSmoke' -count=1 -v
```

`TestSmokeGroq` (skips unless `TEST_GROQ_KEY`): discovery finds `llama-3.3-70b`, and a non-stream + stream completion succeed. `TestSmokeOpenRouter` (skips unless `TEST_OPENROUTER_KEY`): the same with `llama-3.3-70b:free` or `llama-3.3-70b`. `TestSmokeRealFailover` (skips unless both are set): an invalid Groq key `gsk_invalid_router0_smoke` at position 0, the real Groq key next, then OpenRouter. The request succeeds and the trail shows `auth` then `success`. `make smoke` runs the same.

### How the user tests it

Without real keys (browser + curl):
1. `brew services start postgresql@14` (if not running); `make db` (creates `router0` and `router0_test`); `cp .env.example .env` and fill `DATABASE_URL=postgres://$(whoami)@127.0.0.1:5432/router0?sslmode=disable`, `AES_KEY`, `JWT_SECRET`, `ADMIN_EMAIL`, `ADMIN_PASSWORD`.
2. Terminal 1: `make mock` (Groq mock on `:9901`, OpenRouter mock on `:9902`).
3. Terminal 2: `source .env && GROQ_BASE_URL=http://127.0.0.1:9901 OPENROUTER_BASE_URL=http://127.0.0.1:9902 make dev`.
4. Terminal 3: `cd dashboard && pnpm dev`; open `http://localhost:3000`, log in as admin, create an invite (Admin), open the invite link in a private window, and register as `owner@example.com` (org created).
5. Provider Keys: add Groq key `mock-429-15-a` (label `groq-a`), Groq key `mock-ok-b` (label `groq-b`), OpenRouter key `mock-ok-or` (label `or-main`).
6. Tokens: create `demo-svc` and copy the `r0-sk-…` token from the modal into `T`. `curl -s localhost:8080/v1/models -H "Authorization: Bearer $T"` lists `llama-3.3-70b` with providers groq and openrouter.
7. `curl -s localhost:8080/v1/chat/completions -H "Authorization: Bearer $T" -H 'Content-Type: application/json' -d '{"model":"llama-3.3-70b","messages":[{"role":"user","content":"hi"}]}' -D -` → 200, `X-Router0-Provider: groq`, `X-Router0-Key: groq-b`, `X-Router0-Attempts: 2`.
8. Same with `"stream":true` → SSE chunks then `data: [DONE]`.
9. Requests page: the request shows trail `groq/groq-a ✕ rate_limit 429 → groq/groq-b ✓`; Provider Keys shows `groq-a` "rate limited · Ns".
10. Routing page: move OpenRouter above Groq and Save; repeat step 7 → `X-Router0-Provider: openrouter`. Add `-H 'X-Router0-Providers: groq'` → served by Groq again.
11. Errors: `"model":"no-such-model"` → 404 `model_not_found`. Remove `groq-b` and `or-main`, add Groq `mock-402-x` → 429 while `groq-a` cools down (precedence); after 15 s → 402 `insufficient_credits`. Keys `mock-429-40-*` only → 429 with `Retry-After` ≈ 40.
12. Tokens: revoke `demo-svc`; step 7 → 401 `invalid_api_key`.

With real keys: same flow without steps 2–3's base-URL overrides, using real Groq/OpenRouter keys, and `model: "llama-3.3-70b"`.

---

## Phase 2: Anthropic, Gemini, Mesh API, tool calling, chat-only catalog, catalog page, aliases, on-demand discovery

**Goal:** Org adds Anthropic, Gemini, and Mesh API keys. `claude-sonnet-5-5` (or any discovered Claude model) is served via Anthropic's native Messages API, and via OpenRouter/Mesh API on failover. **OpenAI-format tool calling (the user's priority) works identically across Groq, OpenRouter, Anthropic, Gemini, and Mesh API, streaming included**, verified by mock round-trip tests driven by an OpenAI-SDK-style agent loop and by an optional real-Gemini smoke. `/v1/models` returns only chat models (image, TTS, music, transcription, computer-use, and similar models are discovered but filtered out). The Catalog page shows canonical model → providers with a kind filter and a refresh button. Org aliases work. A model that a provider started offering after the last refresh is found on demand at request time.

Capabilities: provider-adapters (Anthropic, Gemini, Mesh API), error-classification (their rules), model-catalog (kind classification, aliases, on-demand), dashboard (catalog page), provider-key-management (`rsk_` hint).

### Slices

| Slice | Owns | Depends on |
|-------|------|------------|
| **2A Schema** | `migrations/000020_org_model_aliases.{up,down}.sql` (aliases table and widening of the `org_model_catalog.kind` CHECK; data.md) | Phase 1 |
| **2B Anthropic adapter** | `internal/providers/anthropic.go`, `internal/providers/anthropic_translate.go`, `internal/providers/anthropic_stream.go`, `internal/providers/anthropic_test.go`, `internal/providers/anthropic_translate_test.go`, `internal/providers/anthropic_stream_test.go`, `internal/providers/testdata/anthropic/` | Phase 1 provider types |
| **2C Gemini conformance** | `internal/providers/google.go`, `internal/providers/google_test.go`, `internal/providers/testdata/gemini/` | Phase 1 |
| **2D Mesh API adapter** | `internal/providers/meshapi.go`, `internal/providers/meshapi_test.go`, `internal/providers/testdata/meshapi/`, `internal/providers/registry.go` (alias map entries only: `aifiesta`, `mesh` → `meshapi`) | Phase 1 |
| **2E Kinds, aliases & on-demand discovery** | `internal/catalog/` (adds `kind.go`, `kind_test.go`, `aliases.go`, `ondemand.go`; edits `discovery.go`, `canonical.go`, `canonical_test.go`, `resolver.go`, `overrides.go`), `internal/handlers/catalog.go`, `internal/handlers/aliases.go` | 2A |
| **2F Dashboard catalog** | `dashboard/` | 2E API (api.md) |
| **2G Wiring, mocks & gate** | `internal/server/`, `cmd/router/main.go`, `cmd/mockprovider/`, `internal/testutil/`, `internal/integration/phase2_test.go`, `internal/integration/smoke_phase2_test.go`, `Makefile`, `.env.example`, `README.md` | all |

Order: 2A → 2B, 2C, 2D, 2E, 2F in parallel → 2G. Paths are disjoint: `providers/` files are split per adapter (`registry.go` only in 2D), `catalog/` only in 2E.

### Key files

- **2A:** `000020_org_model_aliases`: `org_model_aliases` plus `ALTER TABLE org_model_catalog DROP CONSTRAINT org_model_catalog_kind_check, ADD CONSTRAINT … CHECK (kind IN ('chat','embedding','image','audio','other'))`. Assumed: the auto-generated constraint name is `org_model_catalog_kind_check` (the migration looks it up in `pg_constraint` instead of hard-coding it, so it is safe either way).
- **2B:** translation tables and streaming event mapping exactly as in provider-adapters.md; `Classify` per error-classification.md; `ListModels` with `after_id` pagination; golden fixtures in `testdata/anthropic/*.json|*.sse`.
- **2C:** tool-calling conformance tests (mock fixtures of Google's OpenAI-compat responses incl. array-wrapped errors, missing tool-call ids, multiple tool calls, tool-result round trip, streaming tool-call deltas), native `GET /v1beta/models` listing (returns every model with `supportedGenerationMethods`; the kind is decided by `catalog.ClassifyKind`), Gemini `Classify`.
- **2D:** `MeshAPIProvider` on `openai_compat.go`, base URL `MESHAPI_BASE_URL` (default `https://api.meshapi.ai/v1`), Bearer auth, self-registration under id `meshapi`, alias entries `aifiesta`/`mesh` in `registry.go`, `Classify` = generic (no override). Unit tests with fixtures in `testdata/meshapi/`: chat response, SSE stream with a tool-call delta and final usage, `/models` listing with vendor-prefixed ids (`mistral/mistral-7b-instruct-v0`), a 401, a 429 with `Retry-After`, a 402-shaped body. Test that the request sent upstream is `POST {base}/chat/completions` with `Authorization: Bearer rsk_…` and `model` equal to the vendor-prefixed provider id unchanged.
- **2E:**
  - `kind.go`: `ClassifyKind(provider, id string, methods []string) string` per model-catalog.md (rules table); `discovery.go` stores every listed model with its kind (no more per-provider skipping) and the resolver and `/v1/models` only use `kind='chat'`; `kind_test.go` fixture table.
  - `canonical.go`: rule 7 gains the `-v<N>` suffix; fixture row for `mistral/mistral-7b-instruct-v0` (model-catalog.md).
  - `aliases.go` (CRUD + shadowing check), `ondemand.go` (miss debounce key, parallel refresh with 10 s cap), resolver alias step, overrides for Claude/Gemini ids; `GET/POST/DELETE /api/aliases`; `/v1/models` includes aliases; `GET /api/catalog?kind=&q=` (api.md).
- **2F:** `dashboard/app/dashboard/catalog/page.tsx` (canonical model → provider chips, search, kind filter `all | chat | image | audio | embedding | other`, "Refresh now" button, per-provider refresh status, aliases), `components/ProviderChip.tsx`, `lib/api.ts` (catalog with `kind`, refresh, aliases), keys page provider labels for Anthropic / Mesh API with the `rsk_…` placeholder hint.
- **2G:** `mockprovider` gains: Anthropic wire format (`/v1/messages`, SSE events, `x-api-key`); a Gemini-flavoured OpenAI-compat mock whose `/v1beta/models` listing deliberately includes non-chat models (`models/gemini-2.5-flash-image`, `models/gemini-2.5-flash-preview-tts`, `models/lyria-realtime-exp`, `models/gemini-2.5-computer-use-preview`, `models/gemini-robotics-er-1.5-preview`, `models/text-embedding-004`) as observed live against real Gemini; a Mesh API flavour (Bearer auth, vendor-prefixed ids in `/models`: `mistral/mistral-7b-instruct-v0`, `meta-llama/llama-3.3-70b-instruct`, `anthropic/claude-sonnet-5.5`, plus a non-chat `openai/dall-e-3`; same scripted behaviours as other mocks, keyed by substrings of the bearer such as `rsk_mock-429-15-a`); scripted tool-calling conversations (single call, parallel calls, multi-round) in non-stream and SSE form for every flavour; a mutable `/models` list (for on-demand tests). `cmd/mockprovider` adds `:9903` (anthropic), `:9904` (gemini), `:9905` (meshapi). `.env.example` and `README.md` document `ANTHROPIC_BASE_URL`, `GOOGLE_BASE_URL`, `MESHAPI_BASE_URL`; `Makefile` `mock` target starts all five mocks.

### Integration tests required in `internal/integration/phase2_test.go`

Tool calling (priority; each runs the loop below against the named flavour):
`TestPhase2_AgentLoopToolCalling` (table over groq, openrouter, anthropic, gemini, meshapi flavours; non-stream), `TestPhase2_AgentLoopToolCallingStream` (same table; stream), `TestPhase2_AnthropicToolCalling`, `TestPhase2_AnthropicStreamToolCalls`, `TestPhase2_GeminiToolCalling`, `TestPhase2_GeminiStreamToolCalls`, `TestPhase2_MeshAPIToolCalling`, `TestPhase2_CrossProviderToolParity`.

The **OpenAI-SDK-style agent loop** helper (in `internal/testutil/`, same file set as the mocks) mimics what the OpenAI SDK does: send `tools=[get_weather]` and `tool_choice:"auto"`; assert `finish_reason == "tool_calls"` with a non-empty `tool_calls[0].id`, `type:"function"`, and `function.arguments` that parses as JSON; append the assistant message (with `content: null`, `tool_calls` intact) and a `{"role":"tool","tool_call_id":…,"content":…}` message; resend; assert a final `finish_reason == "stop"` text answer. Streaming variant: accumulate `tool_calls[index]` deltas (id and name on the first delta, `arguments` concatenated) and assert the accumulated arguments parse to the same object as the non-stream run, then run the same second round. The loop also covers two parallel tool calls in one assistant turn and a two-round chain (second tool call after the first result).

Other:
`TestPhase2_AnthropicChat`, `TestPhase2_AnthropicStream`, `TestPhase2_AnthropicCreditsIs402`, `TestPhase2_MeshAPIChat`, `TestPhase2_MeshAPIStream`, `TestPhase2_MeshAPIFailover` (Groq key 429 → Mesh mock serves `llama-3.3-70b` via `meta-llama/llama-3.3-70b-instruct`; headers `X-Router0-Provider: meshapi`, attempts 2; and a Mesh 429 → OpenRouter leg), `TestPhase2_MeshAPIProviderAlias` (`X-Router0-Providers: aifiesta` and `mesh` both route to `meshapi`), `TestPhase2_ClaudeFailoverToOpenRouter` (Anthropic 529 → OpenRouter `anthropic/claude-sonnet-5.5` serves canonical `claude-sonnet-5-5`), `TestPhase2_ModelsChatOnly` (after discovery against the Gemini and Mesh mocks, `GET /v1/models` contains `gemini-2.5-flash` and `mistral-7b` and contains no id with `image`, `tts`, `lyria`, `computer-use`, `robotics`, `embed`, or `dall-e`; `GET /api/catalog?kind=image` and `?kind=audio` do list them), `TestPhase2_NonChatModelNotServed` (a chat request for `gemini-2.5-flash-image` → 404 `model_not_found`), `TestPhase2_Aliases`, `TestPhase2_AliasCannotShadow`, `TestPhase2_OnDemandDiscovery`, `TestPhase2_OnDemandDebounce`.

### Gate (exact command)

```bash
pg_isready -h 127.0.0.1 -p 5432 && \
(createdb router0_test 2>/dev/null || true) && \
export GO111MODULE=on \
  DATABASE_URL="postgres://$(whoami)@127.0.0.1:5432/router0_test?sslmode=disable" \
  AES_KEY='000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f' \
  JWT_SECRET='router0-gate-jwt-secret-0123456789abcdef' && \
go build ./... && \
go vet ./... && \
go vet -tags 'integration smoke' ./internal/integration/... && \
go test ./internal/... -count=1 && \
go test -tags integration ./internal/integration/... -run 'TestPhase1|TestPhase2' -count=1 -v && \
! grep -q 'go-redis' go.mod && \
! grep -rIqi 'zer0' dashboard/app dashboard/components && \
(cd dashboard && pnpm install --frozen-lockfile && pnpm run build)
```

The gate needs no real keys, no Docker, and no Redis. It passes only if every `TestPhase2_*` above runs (none skipped), including both agent-loop tests for all five flavours.

Optional smoke (real keys, not part of the gate): the Phase 1 smoke command with `-run 'TestSmoke'` plus `TEST_ANTHROPIC_KEY`, `TEST_GEMINI_KEY`, `TEST_MESHAPI_KEY`. `TestSmokeGeminiTools` (the user's priority; skips when `TEST_GEMINI_KEY` is unset) runs the agent loop above, non-stream and stream, against real Gemini and also asserts `GET /v1/models` contains no image/TTS/music models after discovery. `TestSmokeAnthropicTools` and `TestSmokeMeshAPI` (key `rsk_…`, model `mistral/mistral-7b-instruct-v0` or the first chat model its `/models` lists) each skip when their key is unset.

### How the user tests it

1. `make mock` (now also Anthropic `:9903`, Gemini `:9904`, Mesh API `:9905`), then `make dev` with `ANTHROPIC_BASE_URL=http://127.0.0.1:9903/v1 GOOGLE_BASE_URL=http://127.0.0.1:9904 MESHAPI_BASE_URL=http://127.0.0.1:9905/v1` (or real keys and no overrides).
2. Add Anthropic `mock-ok-anth`, Gemini `mock-ok-gem`, Mesh API `rsk_mock-ok-mesh` keys. Open **Catalog**: `claude-sonnet-5-5` lists anthropic, openrouter, and meshapi chips. Set the kind filter to `image` and `audio`: the Gemini image/TTS/music models appear there only. `curl -s localhost:8080/v1/models -H "Authorization: Bearer $T"` lists none of them. Click "Refresh now" and watch the per-provider status update.
3. Using the OpenAI Python SDK against `http://localhost:8080/v1` with the service token, run a weather-tool conversation (`tools=[get_weather]`, then send the tool result back) with `model="claude-sonnet-5-5"`. You get `tool_calls`, then a final answer. Repeat with `stream=True`; `tool_calls[0].function.arguments` deltas concatenate to valid JSON. Repeat with `X-Router0-Providers: google` and a Gemini model, and with `X-Router0-Providers: meshapi` (alias `aifiesta` works too).
4. Real Gemini (optional): add a real Gemini key, run `TEST_GEMINI_KEY=… make smoke` or the same SDK script with `model="gemini-2.5-flash"`; confirm tool calls round-trip in both modes.
5. Catalog page: add alias `smart → claude-sonnet-5-5`; `model="smart"` works and the response `model` is `claude-sonnet-5-5`.
6. On-demand: `curl -X POST localhost:9902/_models -d '["acme/new-model-x"]'` (mock admin endpoint), then call `model="new-model-x"` → 200 on first try; Catalog shows it with source `on_demand`.

---

## Phase 3: Balance polling, monthly budgets, priority UI polish, legacy cleanup

**Goal:** OpenRouter keys with zero balance are skipped before they fail. Per-key monthly USD budgets stop a key at its limit (402 when nothing else can serve). The priority and key UI gets drag-and-drop, budgets, balances, and "reset state". The legacy pool tables are dropped (their code was already deleted in Phase 1).

Capabilities: credit-tracking, provider-key-management (P3 parts), dashboard (P3 parts).

### Slices

| Slice | Owns | Depends on |
|-------|------|------------|
| **3A Schema** | `migrations/000021_key_budgets_balances.{up,down}.sql`, `migrations/000022_drop_legacy_pool.{up,down}.sql` | Phase 2 |
| **3B Balance polling** | `internal/balance/`, `internal/providers/openrouter_balance.go`, `internal/providers/openrouter_balance_test.go` | 3A; `keystate` credits set/clear from 3C |
| **3C Budgets, spend & key state** | `internal/keystate/`, `internal/metadata/` | 3A |
| **3D Keys & priority polish** | `internal/keys/`, `internal/handlers/keys.go`, `internal/handlers/priority.go`, `dashboard/` | 3A, 3C |
| **3E Wiring & gate** | `internal/server/`, `cmd/router/main.go`, `cmd/mockprovider/`, `internal/testutil/`, `internal/integration/phase3_test.go`, `Makefile`, `.env.example`, `README.md` | all |

Order: 3A → 3B, 3C, 3D in parallel (3B and 3D code against the 3C contracts) → 3E.

Contracts added by 3C for 3B/3D (signatures fixed at slice start): `keystate.Store.SetCredits(ctx, keyID, reason string, ttl time.Duration) error`; `keystate.Store.ClearCredits(ctx, keyID, onlyReason string) error`; `keystate.Store.Reset(ctx, keyID) error`; `keystate.Store.AddSpend(ctx, keyID string, microUSD int64) error` (upsert into `provider_key_spend`); `keystate.Store.MonthSpend(ctx, keyID string) (microUSD int64, err error)` (5 s in-memory cache).

### Key files

- **3B:** `balance/poller.go` (10-min loop over active OpenRouter keys, test hook `PollNow`), `openrouter_balance.go` (`/key`, `/credits` client).
- **3C:** `keystate/budget.go`, `keystate/spend.go` (Postgres `provider_key_spend`), `keystate/store.go` (budget in `Check`/`CheckMany`), `metadata/recorder.go` (spend upsert), `metadata/reconcile.go` (hourly reconcile from `request_metadata`).
- **3D:** `PATCH /api/keys/{id}`, `POST /api/keys/{id}/reset-state`, balances/spend in `GET /api/keys`; `dashboard/app/dashboard/routing/page.tsx` drag-and-drop (native HTML5 DnD, arrows kept), keys page budget editor, spend bar, balance, and reset button.
- **3E:** `server.New` gains the balance poller; mock OpenRouter `/key` and `/credits` endpoints (with a `/_balance` admin endpoint) in `mockprovider`.

### Integration tests required in `internal/integration/phase3_test.go`

`TestPhase3_BalancePollMarksExhausted`, `TestPhase3_BalancePollClears`, `TestPhase3_MonthlyBudget`, `TestPhase3_BudgetCleared`, `TestPhase3_SpendReconcile`, `TestPhase3_ResetKeyState`, `TestPhase3_LegacyTablesDropped` (after migrations the legacy pool tables do not exist and chat still works).

### Gate (exact command)

```bash
pg_isready -h 127.0.0.1 -p 5432 && \
(createdb router0_test 2>/dev/null || true) && \
export GO111MODULE=on \
  DATABASE_URL="postgres://$(whoami)@127.0.0.1:5432/router0_test?sslmode=disable" \
  AES_KEY='000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f' \
  JWT_SECRET='router0-gate-jwt-secret-0123456789abcdef' && \
go build ./... && \
go vet ./... && \
go vet -tags 'integration smoke' ./internal/integration/... && \
go test ./internal/... -count=1 && \
go test -tags integration ./internal/integration/... -run 'TestPhase1|TestPhase2|TestPhase3' -count=1 -v && \
! grep -q 'go-redis' go.mod && \
! grep -rIqi 'zer0' dashboard/app dashboard/components && \
(cd dashboard && pnpm install --frozen-lockfile && pnpm run build)
```

Optional smoke: `TestSmokeOpenRouterBalance` (needs `TEST_OPENROUTER_KEY`): the poll returns a numeric balance.

### How the user tests it

1. Provider Keys: set `groq-b` budget to `$0.001`. Send requests until it shows "budget reached". The next request skips it (trail `skipped: budget_exhausted`). With no other key → 402.
2. Set mock OpenRouter balance to 0 (`curl -X POST localhost:9902/_balance -d 0`). Within 10 min, or after clicking "Poll balances", `or-main` shows "out of credits" and is skipped.
3. Routing page: drag OpenRouter above Groq and Save; the next request's trail starts with OpenRouter. "Reset state" on a rate-limited key clears its badge immediately (its `provider_key_state` rows are deleted).

---

## Self-Review

- [x] Every phase has an exact, runnable gate command that needs no real provider keys (mock providers via `httptest`), no Docker, and no Redis: only the local Postgres 14 at `127.0.0.1:5432` with `router0_test` created idempotently, plus `GO111MODULE=on`. Real-key smoke runs are separate and optional.
- [x] No Redis anywhere: key state is in `provider_key_state` (Postgres) + in-memory cache, tokens use an in-process TTL cache, catalog locks and debounce are in-process; go-redis removal is checked by the gates. Docker appears only as the deployment-example note for `docker-compose.yml`.
- [x] Migrations 000001–000022 use only PG ≤ 14 features (data.md › PostgreSQL Compatibility); none flagged.
- [x] Phase 1 slices own disjoint paths: 1A migrations 14–19 · 1B `auth/ orgs/ tokens/ handlers/{auth,tokens,admin}.go` · 1C `catalog/ handlers/catalog.go` · 1D `keys/ handlers/{keys,priority}.go` · 1E `providers/ keystate/ cache/` · 1F `routing/ handlers/{inference,helpers}.go metadata/ metrics/ config/alerts.yaml` · 1G `handlers/pool.go` · 1H `dashboard/` · 1I `server/ cmd/router cmd/mockprovider testutil/ integration/ db/redis.go health/ models/ go.mod go.sum docker-compose.yml Makefile .env.example README.md`. No path appears twice.
- [x] Phase 2 slices are disjoint: 2A migration 20 · 2B `providers/anthropic*` · 2C `providers/google*` · 2D `providers/meshapi*` + `providers/registry.go` · 2E `catalog/ handlers/{catalog,aliases}.go` · 2F `dashboard/` · 2G `server/ cmd/ testutil/ integration/phase2_test.go smoke_phase2_test.go Makefile .env.example README.md`.
- [x] Phase 3 slices are disjoint: 3A migrations 21–22 · 3B `balance/`, `providers/openrouter_balance*.go` · 3C `keystate/ metadata/` · 3D `keys/ handlers/{keys,priority}.go dashboard/` · 3E wiring only (legacy code already gone in Phase 1). Slices are lettered A–E consecutively and no removed piece (embeddings) is referenced.
- [x] Embeddings appear only as an out-of-scope item and as the `embedding` catalog kind that is filtered out of `/v1/models`.
- [x] agent.md is complete: each AI pattern is evaluated with a decision, and the conclusion is deterministic routing with no LLM inside router0.
- [x] No placeholders remain; every assumption is marked `Assumed:` inline.
