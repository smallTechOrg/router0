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

### Phase 1 build log (2026-11-20)
5. **My own gate re-check was a no-op** — symptom: re-running the 1E gate via a shell variable holding package paths printed "matched no packages" and I almost read it as green. Cause: zsh doesn't word-split `$VAR`. What I did: re-ran with explicit paths. Lesson: handback verification must use literal package paths and must assert a nonzero PASS count, never just "no error".
6. **Parallel slices can't compile the shared `handlers` package mid-flight** — each slice verified its own package plus a scratch copy for handlers; real compile only happened at integration (1I). Worked because contracts in spec/architecture.md were precise. Lesson: keep per-slice compile boundaries package-sized; put cross-slice HTTP handlers behind small interfaces.
7. **Dashboard/backend contract drift caught by review, not tests** — KeyStateBadge didn't know `credits_exhausted`/`budget_exhausted` (contract names); fixed inline. Lesson: enumerate enum values in api.md, not just shapes.
8. **pnpm shim broken under default asdf Node 22** — builds run with `ASDF_NODEJS_VERSION=23.11.0`.
9. **Known gaps carried forward:** JWT lifetimes left at 24h/7d (spec says 15m/30d; changing risks dashboard session handling); `/api/requests` cursor is a plain timestamp (same-microsecond rows across a page boundary could be skipped); pre-routing 400s (bad JSON, missing model) are not logged; real-key smoke tests (`make smoke`) have NOT been run — no provider keys yet.

### Phase 2 build log (2026-11-21)
10. **`isolation: worktree` agents were created from the stale scaffold commit (db85a19), not the feature branch HEAD** — symptom: the agent's worktree lacked all Phase 1 code and specs; its `git merge --ff-only` fix attempt was (rightly) blocked by the permission classifier. What I did: created worktrees myself with `git worktree add -b phase2-2x .claude/worktrees/p2-2x HEAD`. Lesson: verify `git rev-parse HEAD` inside an isolated worktree before trusting it; prefer explicit worktrees off the right commit.
11. **Isolated-agent sandboxes can only write to their own worktree** — the redirect to my hand-made worktrees failed for agents spawned with `isolation: worktree`; relaunching as normal agents writing into the explicit worktree paths worked. Lesson: isolation and explicit-path worktrees are different mechanisms; don't mix them.
12. **Duplicate agents wrote into the same worktree** — redirecting the old agents (instead of stopping them) let both old and new agents write google.go / anthropic files concurrently; one overwrote the other with an unverified copy. Caught by re-reading the diff and re-running tests before merging. Lesson: STOP superseded agents first, then relaunch; never "redirect" a running agent into a path another agent is using.
13. **Network drop killed the last slice agent mid-run** — resumed the same agent via SendMessage with its context intact after `git status` showed what was written; no work lost.
14. **Upgrade wart found only in the live demo:** the catalog rows stored under Phase 1 stayed `kind='chat'` after upgrading (the startup refresh only covered never-refreshed pairs), so `/v1/models` still listed image/TTS/music models until a manual refresh. Fixed: the scheduler now refreshes every (org, provider) pair at startup. Lesson: when a phase changes derived/classified data, the live upgrade path (not just fresh DB tests) needs a check.
15. **Gemini 3 thought signatures** are re-attached from an in-process cache keyed by tool-call id (the shared `ToolCall` type can't carry them). Works for a single gateway process; would break across instances or when a conversation moves providers mid-way.
16. **Real-Gemini tool-calling verified live** through the gateway (non-stream round trip + stream with argument accumulation + usage) using the user's key. Anthropic and Mesh remain mock-verified only (no real keys yet); Claude/Mesh smoke tests (`make smoke`) have not been run.
17. **Mesh API auth "failure" had two causes, both mine.** (a) I started the demo gateway with MESHAPI_BASE_URL pointed at the local mock, so the user's real `rsk_` key was rejected by the mock (symptom: key stored as `error:auth` while their direct curl worked). Lesson: never leave provider base-URL overrides pointed at mocks in an environment where the user will test real keys; say so loudly or default to real endpoints. (b) After pointing at the real API, key tests failed as generic `transient` because real Mesh's `GET /v1/models` returns a BARE JSON ARRAY (not OpenAI's `{"data":[...]}`) and our mock returned the friendlier wrapper, so all tests passed while production failed. Fixed in meshapi.go (accepts both shapes), mock now mirrors the real shape. Lesson: derive mocks from the provider's published OpenAPI/response schema, not from the "OpenAI-compatible" label; "OpenAI-compatible" covers chat completions, not necessarily listing.
18. **Provider-declared model types beat name-pattern rules.** Mesh's `model_type` + `supports_completions_api` are now surfaced as `Extra["kind"]` and honored by catalog discovery (so Responses-API-only text models don't appear as chat). Mesh pins: we send `X-Mesh-Version: 2026-08` on every call (without it Mesh serves its OLDEST contract).
19. Verified live (real Mesh key, tiny request): `mistral-7b` → meshapi, 1 attempt, 200. Catalog from real Mesh: 376 chat / 539 image / 30 audio / 35 embedding / 90 other; `/v1/models` now lists 387 chat models across providers.
