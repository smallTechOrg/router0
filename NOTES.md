# Build Journal — Zer0 Router

## Run: 2026-08-15T16:30 — feature/zer0-router-20260815-1630-v0.1

### Spec
Spec-writer completed at ~16:41. All 13 spec files written. Self-review: all checks pass.
No placeholders. Assumed flags:
- JWT_SECRET env var (min 32 bytes); no ORM; pnpm for dashboard.
- Go service on PORT (default 8080); Next.js on 3000; dashboard proxies /api + /admin in dev.
- Cost lookup table accurate as of 2026-08; OpenRouter cost from x-openrouter-generation-cost header; Baseten costs $0.00 Phase 1.

### Scaffold
- Slice A: Go skeleton (cmd/router/main.go, internal/db/, migrations/, docker-compose.yml, Makefile, .env.example)
- Slice B: Next.js shell (dashboard/ layout, login stub, tailwind, next.config.ts)
- Both slices independent — launched concurrently.

### Friction log
(populated as encountered)

### Scaffold Gate Results (2026-08-15 ~17:00)
- go build: PASS (required GO111MODULE=on; asdf Go had it set to off globally)
- go test ./internal/db/...: PASS (TestPackageCompiles)
- docker-compose: port conflict — local postgres on 5432; changed to 5433
- DATABASE_URL: changed localhost → 127.0.0.1 (IPv6 ::1 refused; IPv4 works)
- GET /healthz: {"status":"ok","db":"ok","redis":"ok"} ✓
- GET /metrics: HTTP 200 ✓
- Migrations: 5 tables created (users, invites, provider_keys, request_metadata, pool_scores) ✓
- pnpm run build: 11 routes, zero errors ✓

### Friction log
1. GO111MODULE=off set globally by asdf — added GO111MODULE=on to .env.example, .env, and Makefile
2. Local postgres on port 5432 — changed docker-compose to port 5433
3. localhost resolves to ::1 (IPv6) — changed DATABASE_URL to 127.0.0.1

---

## Run: 2026-11-20T13:56 — feature/router0-gateway-20261120-1356-v0.1 (base: main)

Pivot: Zer0 Router → router0, a model-name-routed, multi-tenant (org) BYOK gateway.

### Friction log
1. **Harness `support/` tree missing** — symptom: the skill's `support/agents/*.md` role files and `support/patterns/*` aren't present beside the synced skill copy; there's no `project-builder` agent type either. What I did: ran the roles from the SKILL.md description; spec-writer delegated to a general-purpose agent. Lesson: the synced skill install must ship `support/`, or SKILL.md should inline minimal role checklists.
2. **Infra assumption wrong late** — symptom: the spec was written for docker-compose PG16 + Redis, but the machine has no Docker or Redis, and the user wants bare-local with no Redis. What I did: sent the spec back for a no-Redis revision; reuse Homebrew PG14 on 5432 (`router0`, `router0_test` DBs created). Lesson: the intake technical round should ask "how do you run infra locally (Docker / bare / remote)?" and probe for `docker`/`redis-server`/`pg_isready` BEFORE spec-writing.
3. **Real keys deferred** — the user will add provider keys later, so gates are mock-provider-only; the real-key smoke stays open until the keys exist.
4. **Thin baseline tests** — the existing code has tests only in internal/db; the integration test doesn't compile (`NewAdminHandler` arity). Phase 1 replaces it.
