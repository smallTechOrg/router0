# Roadmap: Zer0 Router

## Goal and Scope

Build a self-hosted, OpenAI-compatible inference gateway that routes across multiple user-supplied provider keys, maximises availability, and shows pool health in a web dashboard.

**In scope (Phase 1):** Single-binary Go gateway + Next.js dashboard + PostgreSQL + Redis. All six capabilities below delivered end-to-end.

**Out of scope across all phases:**
- Storing prompt or response content
- Email delivery (invites are shown on-screen)
- Billing or markup
- OAuth / SSO login
- Fine-tuning or model hosting
- Conversation history storage
- Mobile application

---

## Phases of Development

---

### Scaffold

**Goal:** A runnable skeleton — the Go binary starts, connects to PostgreSQL and Redis, runs migrations, serves `GET /healthz`, and the Next.js dashboard builds. Everything wired; no business logic yet.

**Independent slices:**

| Slice | Owns | Dependencies |
|-------|------|-------------|
| A: Go skeleton | `cmd/router/main.go`, `internal/db/postgres.go`, `internal/db/redis.go`, `migrations/`, `docker-compose.yml`, `Makefile`, `.env.example` | None |
| B: Next.js shell | `dashboard/` (layout, login page stub, tailwind, next.config.ts) | None |

**Gate:**
```
docker compose up -d && \
  source .env && \
  go build ./... && \
  go test ./internal/db/... -count=1 -v && \
  cd dashboard && pnpm install && pnpm run build
```

**How the user tests it:**
1. Run the gate command.
2. `curl http://localhost:8080/healthz` returns `{"status":"ok"}`.
3. `open http://localhost:3000` shows the login page stub (no form submission works yet — clearly labelled "Coming soon in Phase 1").

---

### Phase 1: Complete Primary Journey

**Goal:** A user receives an invite, registers, adds provider keys, points their OpenAI client at the router, receives streamed responses, and views pool health and request feed in the dashboard. All six capabilities are live and real on the tested path.

**Slices (all are independent or dependencies marked):**

| Slice | Owns | Dependencies |
|-------|------|-------------|
| 1: Auth & Onboarding | `internal/auth/`, `internal/handlers/auth.go`, `migrations/000001`, `migrations/000002` | Scaffold |
| 2: Provider Key Management | `internal/keys/`, `internal/handlers/keys.go`, `migrations/000003` | Slice 1 (user FK) |
| 3: Provider Adapters | `internal/providers/` (all 6 adapters + interface + registry) | Scaffold |
| 4: Routing Engine | `internal/routing/`, `internal/health/` (basic probe), `migrations/000005` | Slice 3 (Provider interface); Slice 2 (key retrieval) |
| 5: Inference Handler + Metadata | `internal/handlers/inference.go`, `internal/metadata/recorder.go`, `migrations/000004` | Slices 1, 2, 3, 4 |
| 6: Dashboard UI | `dashboard/app/dashboard/`, `dashboard/app/auth/`, `dashboard/app/admin/`, `dashboard/components/`, `dashboard/lib/` | Slices 1–5 for live data |
| 7: Observability | `internal/metrics/prometheus.go`, structured slog wired into inference handler and routing engine, `config/alerts.yaml`, `internal/handlers/admin.go` (basic: pool inspect + invite create) | Slices 1–5 for data |

**Dependencies between slices:**
- Slice 2 depends on Slice 1 (user table must exist).
- Slice 4 depends on Slice 3 (Provider interface) and Slice 2 (key decryption path).
- Slice 5 depends on Slices 1–4 (all must be wired for end-to-end request flow).
- Slice 6 depends on Slices 1–5 for live data (can be built against stub API responses in parallel, then wired at the end).
- Slice 7 is largely independent (cross-cutting); wiring into the inference handler happens alongside or after Slice 5.

**Key files per slice:**

Slice 1: `internal/auth/middleware.go`, `internal/auth/jwt.go`, `internal/auth/keygen.go`, `internal/handlers/auth.go`, `migrations/000001_create_users.*.sql`, `migrations/000002_create_invites.*.sql`

Slice 2: `internal/keys/crypto.go`, `internal/keys/repository.go`, `internal/handlers/keys.go`, `migrations/000003_create_provider_keys.*.sql`

Slice 3: `internal/providers/provider.go`, `internal/providers/registry.go`, `internal/providers/google.go`, `internal/providers/groq.go`, `internal/providers/openrouter.go`, `internal/providers/nvidia.go`, `internal/providers/together.go`, `internal/providers/baseten.go`

Slice 4: `internal/routing/pool.go`, `internal/routing/scorer.go`, `internal/routing/selector.go`, `internal/routing/ratelimit.go`, `internal/health/probe.go`, `internal/health/scheduler.go`, `migrations/000005_create_pool_scores.*.sql`

Slice 5: `internal/handlers/inference.go`, `internal/metadata/recorder.go`, `migrations/000004_create_request_metadata.*.sql`

Slice 6: `dashboard/app/`, `dashboard/components/`, `dashboard/lib/api.ts`, `dashboard/lib/auth.ts`

Slice 7: `internal/metrics/prometheus.go`, `internal/handlers/admin.go`, `config/alerts.yaml`

**Gate:**
```
source .env && \
  go test ./internal/... -tags integration -run TestGatewayEndToEnd -count=1 -v && \
  cd dashboard && pnpm run build
```

The `TestGatewayEndToEnd` integration test:
1. Starts the Go service against a test PostgreSQL database (from `.env`).
2. Creates an admin user directly in the DB.
3. Calls `POST /admin/invites` to create an invite.
4. Calls `POST /auth/register` with the invite token, email, and password — asserts `zer0_key` is returned.
5. Calls `POST /api/keys` with a real Groq API key (from `.env` `TEST_GROQ_KEY`) — asserts HTTP 201.
6. Calls `POST /v1/chat/completions` with the Zer0 key and prompt "count to 3" with `stream: true` — asserts HTTP 200, at least 3 SSE chunks received, `X-Zer0-Provider: groq` header present, `X-Zer0-Latency-Ms` > 0.
7. Calls `GET /api/requests` — asserts the request appears in the feed with `input_tokens > 0` and `status: success`.
8. Calls `GET /metrics` — asserts `zer0_requests_total` counter is > 0.

**How the user tests it:**
1. Copy `.env.example` to `.env`; fill in `DATABASE_URL`, `REDIS_URL`, `AES_KEY` (64 hex chars), `JWT_SECRET`, `TEST_GROQ_KEY` (a real Groq API key).
2. Run `docker compose up -d` to start PostgreSQL and Redis.
3. Run the gate command. All assertions pass.
4. Manual verification:
   - Open `http://localhost:3000`. Click "Register" (requires invite token from the test output).
   - Log in as the registered user.
   - Navigate to `/dashboard/keys` — see the Groq key listed with a green status badge.
   - Navigate to `/dashboard` — see the Groq combo in the active pool table with a score and latency.
   - Navigate to `/dashboard/requests` — see the test request in the feed with model, latency, tokens, and `success` badge.
   - Navigate to `/dashboard/costs` — see a non-zero estimated cost for Groq.
   - "Spend chart coming soon" stub is visible on the costs page.
   - "Analytics" section on the pool page shows a stub card labelled "Coming soon."
5. Using the `openai` Python client: `client = OpenAI(api_key="zer0:sk-...", base_url="http://localhost:8080/v1"); response = client.chat.completions.create(model="llama-3.1-70b-versatile", messages=[...])` — assert a non-empty response is returned.

---

### Phase 2: Advanced Operations and Pool Intelligence

**Goal:** Fully automated pool health management, time-series cost analytics, a complete admin API, and a contextual-bandit routing hook that outperforms static weights given sufficient telemetry.

**Capabilities delivered (≥3):**

| Capability | Description |
|-----------|-------------|
| 7: Advanced Health Probe Scheduler | Hybrid probing: idle-key probes on configurable interval (5–15 min), live traffic rate-limit header parsing already wired in Phase 1, billing API polling where available (OpenRouter, Google). Goroutine pool with backpressure. |
| 8: Pool Auto-Management | Automated promotion/demotion with configurable thresholds (env var overrides for `PROMOTE_MIN_SCORE`, `DEMOTE_MAX_ERROR_RATE`, etc.). Re-scoring every 5 minutes wired to pool_scores table. |
| 9: Cost Analytics | Time-series spend chart (daily granularity, 30-day window) on `/dashboard/costs`. PostgreSQL aggregation query by day. Budget alert threshold configurable per user. |
| 10: Admin API (full) | Complete admin surface: disable any key, force pool overrides, view any user's pool state, list all users. Plus: operator runbook for AES_KEY rotation. |
| 11: Bandit Routing (optional, feature-flagged) | Thompson Sampling `BanditScoreFunc` implementing the `ScoreFunc` interface from Phase 1. Active when `ROUTING_STRATEGY=bandit`. Cold-start fallback to weighted formula. Alpha/beta stored in Redis. |

**Independent slices:**

| Slice | Owns | Dependencies |
|-------|------|-------------|
| A: Health Probe Scheduler | `internal/health/` (full implementation) | Phase 1 complete |
| B: Pool Auto-Management | `internal/routing/pool.go` (promotion/demotion goroutine) | Phase 1 complete |
| C: Cost Analytics | `dashboard/app/dashboard/costs/page.tsx` (chart), PostgreSQL daily aggregation view | Phase 1 complete |
| D: Admin API complete | `internal/handlers/admin.go` (all admin endpoints) | Phase 1 complete |
| E: Bandit Routing | `internal/routing/bandit.go`, `BanditScoreFunc` | Phase 1 `ScoreFunc` interface |

**Gate:**
```
source .env && \
  go test ./internal/... -tags integration -count=1 -v && \
  go test ./internal/routing/... -run TestBanditScoreFunc -count=1 -v && \
  cd dashboard && pnpm run build && pnpm run test
```

The integration suite must include:
- A test that runs 200 requests through the bandit scorer with simulated telemetry and asserts the top-scored combo at round 200 matches the combo with the highest simulated success rate (the bandit must converge).
- A test that idle-key probes fire within the configured interval and update the Redis health cache.
- A test that pool promotion fires after injecting 20 low-error-rate, low-latency metadata rows for a common-pool combo.

**How the user tests it:**
- Set `ROUTING_STRATEGY=bandit` in `.env` and restart the service.
- Send 50 requests. Observe via `/metrics` that `zer0_pool_score` values drift over time (bandit is learning).
- On `/dashboard/costs`, the spend chart shows bars for each of the last 30 days (or fewer if the service is newer).
- Admin: `curl -H "Authorization: Bearer <admin_jwt>" http://localhost:8080/admin/pool/<user_id>` returns full pool state.
