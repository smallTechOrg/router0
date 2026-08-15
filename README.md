# Zer0 Router

A multi-tenant, OpenAI-compatible inference gateway. Bring your own LLM provider API keys and get a single `zer0:sk-xxx` token to use as a drop-in replacement for the OpenAI API key. Requests are intelligently routed across providers to maximise availability, minimise cost, and ensure no request goes unfulfilled.

## Architecture

- **Router service** — Go 1.22 (low latency, high concurrency, streaming SSE proxy)
- **Dashboard** — Next.js 14 App Router (full-stack SSR + API routes)
- **Database** — PostgreSQL 16 (users, keys, request metadata, pool scores)
- **Cache** — Redis 7 (rate-limit counters, pool state, health probe results)

## Prerequisites

- Go 1.22+
- Node.js 18+, pnpm 8+
- Docker + Docker Compose (for PostgreSQL and Redis)
- `GO111MODULE=on` (set in `.env` or globally)

## Quick Start

### 1. Clone and configure

```bash
git clone <repo>
cd zer0-router
cp .env.example .env
# Edit .env and fill in AES_KEY, JWT_SECRET, and ADMIN_PASSWORD
# For testing, set TEST_GROQ_KEY to a real Groq API key
```

Generate required secrets:
```bash
openssl rand -hex 32    # → AES_KEY (64 hex chars = 32 bytes)
openssl rand -base64 32 # → JWT_SECRET
```

### 2. Start dependencies

```bash
docker-compose up -d
```

PostgreSQL is available on port 5433 (to avoid conflicts with local postgres on 5432).
Redis is available on port 6379.

### 3. Run the router service

```bash
# Load .env first
source .env && GO111MODULE=on go run ./cmd/router
```

Or build and run:
```bash
GO111MODULE=on go build -o bin/router ./cmd/router
source .env && ./bin/router
```

The router starts on `PORT` (default `8080`). Migrations run automatically on startup.

**Health check:**
```bash
curl http://localhost:8080/healthz
# {"status":"ok","db":"ok","redis":"ok"}
```

### 4. Run the dashboard

```bash
cd dashboard
pnpm install
pnpm run dev
```

Open `http://localhost:3000`.

## Development

### Run tests

```bash
# Unit tests (no external services needed)
GO111MODULE=on go test ./internal/... -count=1 -v

# Integration tests (requires running PostgreSQL, Redis, and a real Groq API key in .env)
source .env && GO111MODULE=on go test ./internal/... -tags integration -count=1 -v

# Dashboard tests
cd dashboard && pnpm test
```

### Database migrations

Migrations run automatically when the router starts. Migration files live in `migrations/`.

To inspect migration state:
```bash
PGPASSWORD=zer0dev psql -h 127.0.0.1 -p 5433 -U zer0 -d zer0router -c 'SELECT * FROM schema_migrations'
```

### Scaffold gate (verify the skeleton)

```bash
docker-compose up -d && \
  source .env && \
  GO111MODULE=on go build ./... && \
  GO111MODULE=on go test ./internal/db/... -count=1 -v && \
  cd dashboard && pnpm install && pnpm run build
```

Expected results:
- `go build` exits 0
- `TestPackageCompiles` passes
- Next.js builds 11 routes, zero TypeScript errors

## Environment Variables

See `.env.example` for full documentation. Required vars:

| Variable | Description |
|----------|-------------|
| `DATABASE_URL` | PostgreSQL DSN (use `127.0.0.1:5433` with docker-compose) |
| `REDIS_URL` | Redis URL |
| `AES_KEY` | 64 hex chars (32 bytes) — for provider key encryption |
| `JWT_SECRET` | Min 32 bytes — for dashboard JWT auth |
| `ADMIN_EMAIL` | First admin user email |
| `ADMIN_PASSWORD` | First admin user password |
| `GO111MODULE` | Must be `on` (override if globally set to `off`) |

## Providers (Phase 1)

All adapters are in `internal/providers/`:
- Google AI Studio (`google.go`) — Gemini Flash 1.5, Gemini Pro
- Groq Cloud (`groq.go`) — Llama 3.1 70B, Mixtral 8x7B
- OpenRouter (`openrouter.go`) — aggregator, many models
- NVIDIA NIM (`nvidia.go`) — Llama 3.1 70B, 8B
- Together AI (`together.go`)
- Baseten (`baseten.go`)

## API

The router is OpenAI-compatible:

```
POST http://localhost:8080/v1/chat/completions
Authorization: Bearer zer0:sk-<your-key>
Content-Type: application/json
```

Response headers added:
- `X-Zer0-Model` — model used
- `X-Zer0-Provider` — provider used
- `X-Zer0-Latency-Ms` — total latency

## Project Layout

```
cmd/router/         Go entry point
internal/
  auth/             Zer0 key auth, JWT, key generation
  db/               PostgreSQL + Redis clients
  handlers/         HTTP handlers
  health/           Health probe scheduler
  keys/             AES-256 key crypto + repository
  metadata/         Async request metadata recording
  metrics/          Prometheus collectors
  providers/        Provider adapters (plugin pattern)
  routing/          Pool scoring, selector, rate limits
migrations/         SQL migrations (000001–000005)
dashboard/          Next.js 14 App Router dashboard
config/             config.yaml, alerts.yaml
spec/               Full product spec
```

## Observability

- **Structured JSON logs** — `slog` to stdout; ship to Loki/Datadog/CloudWatch
- **Prometheus metrics** — `GET /metrics` endpoint
- **Sample alerts** — `config/alerts.yaml`

## Status

- Scaffold: complete ✓
- Phase 1 (full primary journey): pending
- Phase 2 (advanced operations + pool intelligence): pending
