# router0

An org-scoped, OpenAI-compatible LLM gateway. Each org brings its own provider keys (BYOK), issues many revocable service tokens (`r0-sk-...`), and calls one endpoint with a model name. router0 resolves the model across the org's providers, tries them in a fixed priority order (key order inside a provider), fails over on any error, and returns one of three distinct, actionable errors: 404 (model not found), 402 (out of credits), 429 (rate limited, with `Retry-After`).

- **Gateway:** Go 1.22 (`cmd/router`), chi, pgx, AES-256-GCM key storage, Prometheus metrics.
- **Dashboard:** Next.js 14 (`dashboard/`): tokens, provider keys + order, routing priority, request log with failover trail.
- **Storage:** PostgreSQL 14+ is the only external service. There is no Redis: cooldowns and credit state live in Postgres, short-lived caches live in process memory.

Specs live in `spec/` (start with `spec/roadmap.md` and `spec/api.md`).

## Quick start (bare-local, no Docker, no Redis)

Prerequisites: Go 1.22+, Node 18+ with pnpm, Homebrew `postgresql@14`.

```bash
brew services start postgresql@14     # if not running
make db                               # creates databases router0 and router0_test (idempotent)

cp .env.example .env
# edit .env: DATABASE_URL=postgres://<your-os-user>@127.0.0.1:5432/router0?sslmode=disable
#            AES_KEY=$(openssl rand -hex 32)   JWT_SECRET=$(openssl rand -base64 32)
#            ADMIN_EMAIL / ADMIN_PASSWORD
```

Use `127.0.0.1`, not `localhost` (IPv6 `::1` is refused). Set `GO111MODULE=on` for every Go command (the Makefile does).

### Try it without real provider keys

```bash
# terminal 1: mock providers (Groq flavour on :9901, OpenRouter flavour on :9902)
make mock

# terminal 2: the gateway, pointed at the mocks (migrations run on start)
source .env && GROQ_BASE_URL=http://127.0.0.1:9901 OPENROUTER_BASE_URL=http://127.0.0.1:9902 make dev

# terminal 3: the dashboard
cd dashboard && pnpm install && pnpm dev     # http://localhost:3000
```

Log in as the admin (`ADMIN_EMAIL` / `ADMIN_PASSWORD`; the admin gets its own org), create an invite on the Admin page, open the invite link in a private window and register an owner. Then:

1. **Provider Keys:** the mock picks its behaviour from the key you add. Add Groq `mock-429-15-a` (label `groq-a`), Groq `mock-ok-b` (`groq-b`) and OpenRouter `mock-ok-or` (`or-main`).
2. **Tokens:** create `demo-svc` and copy the `r0-sk-...` token into `T` (it is shown once).
3. Call the gateway:

```bash
T=r0-sk-...
curl -s localhost:8080/v1/models -H "Authorization: Bearer $T"

curl -s -D - localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $T" -H 'Content-Type: application/json' \
  -d '{"model":"llama-3.3-70b","messages":[{"role":"user","content":"hi"}]}'
# 200, X-Router0-Provider: groq, X-Router0-Key: groq-b, X-Router0-Attempts: 2

curl -sN localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $T" -H 'Content-Type: application/json' \
  -d '{"model":"llama-3.3-70b","stream":true,"messages":[{"role":"user","content":"hi"}]}'

# force a provider (restricts AND orders)
curl -s -D - localhost:8080/v1/chat/completions -H "Authorization: Bearer $T" \
  -H 'X-Router0-Providers: openrouter' -H 'Content-Type: application/json' \
  -d '{"model":"llama-3.3-70b","messages":[{"role":"user","content":"hi"}]}'
```

4. **Requests page:** the request shows its failover trail (`groq-a` rate limited, then `groq-b` ok). **Routing page:** move OpenRouter above Groq and save; the next call is served by OpenRouter.

Mock key behaviours: `mock-ok*` (200, JSON and SSE), `mock-429-<secs>*` (429 + `Retry-After`), `mock-402*` (402), `mock-401*` (401), `mock-500*`, `mock-503*`, `mock-404*`, `mock-400*`, `mock-midstream*` (stream drops after two chunks).

### With real keys

Same flow without the `*_BASE_URL` overrides: add real Groq / OpenRouter keys and call with `"model":"llama-3.3-70b"` (any provider-specific id such as `meta-llama/llama-3.3-70b-instruct` also resolves).

## How the gateway behaves

- **Model resolution:** the requested name is canonicalised (`llama-3.3-70b-versatile`, `meta-llama/llama-3.3-70b-instruct` and `Llama-3.3-70B` are the same model) and matched against the org's catalog, which is discovered from each provider's model list.
- **Routing:** providers in the org's priority order, keys in position order. `X-Router0-Providers: a,b` overrides the order for one request. Keys in cooldown (rate limited, out of credits) are skipped without a call; a transiently failing key is only moved to the end of the plan.
- **Failover:** any error moves on to the next candidate (max 8 attempts). Streaming fails over only before the first chunk; after that the client gets a `stream_interrupted` error event and `[DONE]`.
- **Response headers:** `X-Router0-Request-Id`, `-Canonical-Model`, `-Attempts` always; `-Provider`, `-Model`, `-Key`, `-Latency-Ms` on success; `Retry-After` on 429.
- **Privacy:** only metadata is stored (never prompts, responses, provider error bodies or keys).

| HTTP | `error.code` | Meaning |
|------|--------------|---------|
| 400 | `invalid_json`, `missing_model`, `unsupported_content`, `invalid_provider_override` | rejected before routing |
| 400 | `upstream_rejected_request` | every provider rejected the request itself |
| 401 | `invalid_api_key` | missing, unknown or revoked service token |
| 404 | `model_not_found` | no provider the org uses offers the model |
| 402 | `insufficient_credits` | nothing rate limited, at least one provider out of credits |
| 429 | `rate_limited` | at least one provider rate limited; `Retry-After` = soonest cooldown end |
| 503 | `all_providers_failed` | anything else (transient errors, auth failures) |

Precedence when several apply: 429 > 402 > 404 > 400 > 503. Every error body carries `error.router0.attempts`. Full contract: `spec/api.md`.

## Development

```bash
make test         # unit tests (creates router0_test if needed)
make build        # bin/router
make gate-phase1  # the full Phase 1 gate (see below)
make smoke        # optional real-key smoke tests (TEST_GROQ_KEY / TEST_OPENROUTER_KEY)
```

The **Phase 1 gate** (`make gate-phase1`) builds, vets, runs unit tests, runs every `TestPhase1_*` integration test against the real Postgres database `router0_test` with mock providers, checks that go-redis is gone from `go.mod` and no legacy branding is left in the dashboard, and builds the dashboard. It needs only local Postgres.

Integration tests (`internal/integration`) drive `server.New`, the same route table `cmd/router` uses, over HTTP; providers are scripted mock servers from `internal/testutil/mockprovider`, selected through `GROQ_BASE_URL` / `OPENROUTER_BASE_URL`. Each test creates its own org and never truncates tables.

### Troubleshooting

- **`pnpm` fails on Node 22 on this machine** (broken asdf shim): prefix dashboard commands with `ASDF_NODEJS_VERSION=23.11.0`, e.g. `ASDF_NODEJS_VERSION=23.11.0 pnpm run build`. `make gate-phase1` already does.
- **Go builds ignore modules:** asdf may set `GO111MODULE=off` globally; use `GO111MODULE=on` (the Makefile exports it).
- **Connection refused on `localhost:5432`:** use `127.0.0.1` in `DATABASE_URL`.

### Metrics and logs

`GET /metrics` (Prometheus, `router0_*` names; see `config/alerts.yaml`), `GET /healthz` (`{"status":"ok","db":"ok"}`). Logs are JSON on stdout with `service: router0`; one `request_completed` line per inference request.

## Layout

```
cmd/router/          gateway entry point (env, DB, migrations, background jobs)
cmd/mockprovider/    keyless mock providers for manual testing (:9901 / :9902)
cmd/loadtest/        simple load generator (ROUTER0_TOKEN=r0-sk-...)
internal/server/     the single route table (used by main and the tests)
internal/auth/ orgs/ tokens/       JWT, orgs, service tokens
internal/keys/ keystate/ catalog/  provider keys + priority, cooldown state, model catalog
internal/routing/ providers/       candidate plans + failover, provider adapters
internal/handlers/ metadata/ metrics/
internal/testutil/mockprovider/    scripted OpenAI-compatible mock provider
internal/integration/              Phase 1 integration tests (build tag: integration)
migrations/          SQL migrations (run automatically on start)
dashboard/           Next.js dashboard
spec/                product and engineering specs
```

## Deployment example

`docker-compose.yml` is a **deployment example only** (a PostgreSQL service; there is no Redis). It is not used for local development or tests. Run the gateway binary (`make build`) next to any PostgreSQL 14+ and set `DATABASE_URL`, `AES_KEY`, `JWT_SECRET`; running several instances behind a load balancer is safe because Postgres is the source of truth for tokens and key state (expect up to a few seconds of key-state cache staleness, and up to 30 s for token revocation to reach other instances).
