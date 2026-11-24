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
# terminal 1: mock providers (Groq :9901, OpenRouter :9902, Anthropic :9903, Gemini :9904, Mesh API :9905)
make mock

# terminal 2: the gateway, pointed at the mocks (migrations run on start)
source .env && GROQ_BASE_URL=http://127.0.0.1:9901/v1 OPENROUTER_BASE_URL=http://127.0.0.1:9902/v1 \
  ANTHROPIC_BASE_URL=http://127.0.0.1:9903/v1 GOOGLE_BASE_URL=http://127.0.0.1:9904 \
  MESHAPI_BASE_URL=http://127.0.0.1:9905/v1 make dev

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

## Anthropic, Gemini, Mesh API, tool calling, catalog and aliases

**Providers.** Add keys on the Provider Keys page (or `POST /api/keys`):

| Provider id | Key looks like | Base URL env (default) | Notes |
|-------------|----------------|------------------------|-------|
| `anthropic` | `sk-ant-...` | `ANTHROPIC_BASE_URL` (`https://api.anthropic.com/v1`) | native Messages API; OpenAI requests are translated, streaming included |
| `google` (alias `gemini`) | `AIza...` | `GOOGLE_BASE_URL` (`https://generativelanguage.googleapis.com`) | OpenAI-compatible chat endpoint plus native model listing |
| `meshapi` (aliases `aifiesta`, `mesh`) | `rsk_...` | `MESHAPI_BASE_URL` (`https://api.meshapi.ai/v1`) | OpenAI-compatible; vendor-prefixed ids such as `mistral/mistral-7b-instruct-v0` |

**Tool calling** is OpenAI-format and works the same on Groq, OpenRouter, Anthropic, Gemini and Mesh API, streaming included: send `tools` and `tool_choice`, get `finish_reason: "tool_calls"` with `tool_calls[].id`, send back the assistant turn and `role: "tool"` messages with `tool_call_id`. Gemini quirks (missing ids, repeated indexes, `stop` finish reason) and Anthropic's `tool_use`/`tool_result` blocks are normalised by the adapters.

**Using router0 from an OpenAI-compatible agent:** base URL `http://localhost:8080/v1`, API key = a service token (`r0-sk-...`), model = a canonical name from `GET /v1/models` (for example `claude-sonnet-5-5`, `gemini-2.5-flash`, `llama-3.3-70b`, `mistral-7b`). Pin a provider with `X-Router0-Providers: google` (or `anthropic`, `meshapi`/`aifiesta`/`mesh`).

```python
from openai import OpenAI
c = OpenAI(base_url="http://localhost:8080/v1", api_key="r0-sk-...")
tools = [{"type": "function", "function": {"name": "get_weather", "parameters": {
    "type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}}]
msgs = [{"role": "user", "content": "Weather in Paris?"}]
r = c.chat.completions.create(model="claude-sonnet-5-5", messages=msgs, tools=tools)
call = r.choices[0].message.tool_calls[0]
msgs += [r.choices[0].message, {"role": "tool", "tool_call_id": call.id, "content": '{"temp_c":18}'}]
print(c.chat.completions.create(model="claude-sonnet-5-5", messages=msgs, tools=tools).choices[0].message.content)
```

**Catalog.** `/v1/models` lists chat models only. Image, audio, embedding and other models are discovered and kept (dashboard **Catalog** page, kind filter; `GET /api/catalog?kind=image`), but a chat request for one returns `404 model_not_found`. "Refresh now" (`POST /api/catalog/refresh`) re-lists every provider. A model a provider added after the last refresh is found on demand at request time (60 s per-model debounce).

**Aliases.** `POST /api/aliases {"alias":"smart","canonical_model":"claude-sonnet-5-5"}` (owner/admin; `GET` for any member, `DELETE /api/aliases/smart`). `model="smart"` then works and the response `model` is the canonical name. An alias that equals an existing model is rejected with `422 alias_shadows_model`; a duplicate with `409 alias_conflict`.

**Mock keys** (Phase 2 mocks): Anthropic `mock-ok-anth`, `mock-credits-*` (400 "credit balance is too low" -> router0 402), `mock-529-*` (overloaded -> failover); Mesh keys may carry the prefix, e.g. `rsk_mock-429-15-a`. Select a scripted tool conversation by adding `[scenario:single]`, `[scenario:parallel]` or `[scenario:chain]` to the user message together with a `tools` list. `curl -X POST localhost:9902/_models -d '["acme/new-model-x"]'` changes a mock's model list (on-demand discovery demo).

## Budgets, balances and key state

**Monthly budgets.** `PATCH /api/keys/{id} {"monthly_budget_usd": 50}` (owner/admin; `null` removes it, `0` pauses the key; the smallest non-zero budget is `0.0001`). Each successful request adds its estimated cost to the key's spend for the current UTC month. Costs come from the provider's reported cost, router0's price table, or the catalog's per-model prices. Once spend reaches the budget, the key is skipped (`skip_reason: budget_exhausted`). If no other key can serve the request, the client gets `402 insufficient_credits`. Budgets reset at 00:00 UTC on the 1st. The counter is rebuilt from the request log on startup and every hour. The keys page shows spend against budget (estimated) and has a budget editor. The Costs page shows a bar per key.

**Balances (OpenRouter).** router0 reads each active OpenRouter key's remaining balance (`GET /key`, plus `/credits` for keys without their own limit) on startup and every `ROUTER0_BALANCE_POLL_SECONDS` (600). The keys page "Poll balances" button, or `POST /api/keys/poll-balances`, does it on demand. A key at $0 is marked out of credits and skipped before it fails. The mark lasts one poll interval plus a minute, and a later poll with a positive balance clears it. A 402 from a live request is never cleared by a poll. Other providers have no per-key balance API and show "not available". On Cloud Run with request-based CPU (`--min-instances 0`), the periodic loop only runs while the service is handling traffic. The startup poll and the button still work.

**Service-token budgets.** `PATCH /api/tokens/{id} {"monthly_budget_usd": 25}` (Tokens page "Set monthly budget") caps what one calling app can spend per UTC month, whatever keys serve it. Over the cap, its requests get `402 token_budget_exceeded` before any provider is called; other tokens are unaffected. The counter is reconciled from the request log like key spend.

**Reset state.** `POST /api/keys/{id}/reset-state` (the keys page "Reset state" button) deletes a key's cooldown and out-of-credits rows, so the next request tries it again. It does not lift a budget; raise or remove the budget instead.

With the mocks: set a key's budget to `0.0001` and send requests until it shows "budget reached". To run the balance poll against the mock, run `curl -X POST localhost:9902/_balance -d 0`, then click "Poll balances": `or-main` shows "out of credits" and is skipped. Then run `-d 5` and poll again to clear it.

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
make gate-phase2  # the full Phase 2 gate: Phase 1 + Anthropic/Gemini/Mesh/tool-calling/catalog tests
make smoke        # optional real-key smoke tests (TEST_GROQ_KEY, TEST_OPENROUTER_KEY, TEST_ANTHROPIC_KEY, TEST_GEMINI_KEY, TEST_MESHAPI_KEY)
```

The **Phase 1 gate** (`make gate-phase1`) builds, vets, runs unit tests, runs every `TestPhase1_*` integration test against the real Postgres database `router0_test` with mock providers, checks that go-redis is gone from `go.mod` and no legacy branding is left in the dashboard, and builds the dashboard. It needs only local Postgres.

Integration tests (`internal/integration`) drive `server.New`, the same route table `cmd/router` uses, over HTTP; providers are scripted mock servers from `internal/testutil/mockprovider`, selected through `GROQ_BASE_URL` / `OPENROUTER_BASE_URL` / `ANTHROPIC_BASE_URL` / `GOOGLE_BASE_URL` / `MESHAPI_BASE_URL`. The OpenAI-SDK-style agent loop used by the tool-calling tests is `internal/testutil/agentloop`. Each test creates its own org and never truncates tables.

### Troubleshooting

- **`pnpm` fails on Node 22 on this machine** (broken asdf shim): prefix dashboard commands with `ASDF_NODEJS_VERSION=23.11.0`, e.g. `ASDF_NODEJS_VERSION=23.11.0 pnpm run build`. `make gate-phase1` / `gate-phase2` already do.
- **Go builds ignore modules:** asdf may set `GO111MODULE=off` globally; use `GO111MODULE=on` (the Makefile exports it).
- **Connection refused on `localhost:5432`:** use `127.0.0.1` in `DATABASE_URL`.

### Metrics and logs

`GET /metrics` (Prometheus, `router0_*` names; see `config/alerts.yaml`). Budgets and balances: `router0_key_skips_total{provider,reason}` (`rate_limited`, `credits_exhausted`, `budget_exhausted`), `router0_spend_usd_total{provider}` (estimated), `router0_balance_polls_total{provider,result}`, `router0_spend_reconcile_total{status}`, `GET /healthz` (`{"status":"ok","db":"ok"}`). Logs are JSON on stdout with `service: router0`; one `request_completed` line per inference request.

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
