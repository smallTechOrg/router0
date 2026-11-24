# router0 — bare-local workflow: Homebrew postgresql@14 on 127.0.0.1:5432, no Docker, no Redis.
# `make dev` reads .env if it exists (copy .env.example first).

export GO111MODULE := on

PG_USER := $(shell whoami)
TEST_ENV := DATABASE_URL="postgres://$(PG_USER)@127.0.0.1:5432/router0_test?sslmode=disable" \
	AES_KEY='000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f' \
	JWT_SECRET='router0-gate-jwt-secret-0123456789abcdef'
# Works around a broken pnpm shim under the default Node 22 on some machines.
NODE_ENV_FIX := ASDF_NODEJS_VERSION=23.11.0

.PHONY: db dev mock build test gate-phase1 gate-phase2 gate-phase3 smoke lint

# Create the dev and test databases (idempotent).
db:
	@createdb router0 2>/dev/null || echo "router0 already exists"
	@createdb router0_test 2>/dev/null || echo "router0_test already exists"

# Run the gateway against the dev database (loads .env when present).
dev:
	@if [ -f .env ]; then set -a; . ./.env; set +a; fi; go run ./cmd/router

# Keyless mock providers: Groq :9901, OpenRouter :9902, Anthropic :9903, Gemini :9904, Mesh API :9905.
mock:
	go run ./cmd/mockprovider

build:
	go build -o bin/router ./cmd/router

# Unit tests (those needing Postgres use DATABASE_URL and skip otherwise).
test:
	@createdb router0_test 2>/dev/null || true
	$(TEST_ENV) go test ./internal/... -count=1

# The exact Phase 1 gate from spec/roadmap.md. Needs only local Postgres 14.
gate-phase1:
	pg_isready -h 127.0.0.1 -p 5432 && \
	(createdb router0_test 2>/dev/null || true) && \
	export GO111MODULE=on \
	  DATABASE_URL="postgres://$$(whoami)@127.0.0.1:5432/router0_test?sslmode=disable" \
	  AES_KEY='000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f' \
	  JWT_SECRET='router0-gate-jwt-secret-0123456789abcdef' && \
	go build ./... && \
	go vet ./... && \
	go vet -tags 'integration smoke' ./internal/integration/... && \
	go run ./cmd/migrate && \
	go test ./internal/... -count=1 && \
	go test -tags integration ./internal/integration/... -run 'TestPhase1' -count=1 -v && \
	! grep -q 'go-redis' go.mod && \
	! grep -rIqi 'zer0' dashboard/app dashboard/components && \
	(cd dashboard && $(NODE_ENV_FIX) pnpm install --frozen-lockfile && $(NODE_ENV_FIX) pnpm run build)

# The exact Phase 2 gate from spec/roadmap.md. Needs only local Postgres 14.
gate-phase2:
	pg_isready -h 127.0.0.1 -p 5432 && \
	(createdb router0_test 2>/dev/null || true) && \
	export GO111MODULE=on \
	  DATABASE_URL="postgres://$$(whoami)@127.0.0.1:5432/router0_test?sslmode=disable" \
	  AES_KEY='000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f' \
	  JWT_SECRET='router0-gate-jwt-secret-0123456789abcdef' && \
	go build ./... && \
	go vet ./... && \
	go vet -tags 'integration smoke' ./internal/integration/... && \
	go run ./cmd/migrate && \
	go test ./internal/... -count=1 && \
	go test -tags integration ./internal/integration/... -run 'TestPhase1|TestPhase2' -count=1 -v && \
	! grep -q 'go-redis' go.mod && \
	! grep -rIqi 'zer0' dashboard/app dashboard/components && \
	(cd dashboard && $(NODE_ENV_FIX) pnpm install --frozen-lockfile && $(NODE_ENV_FIX) pnpm run build)

# The exact Phase 3 gate from spec/roadmap.md. Needs only local Postgres 14.
gate-phase3:
	pg_isready -h 127.0.0.1 -p 5432 && \
	(createdb router0_test 2>/dev/null || true) && \
	export GO111MODULE=on \
	  DATABASE_URL="postgres://$$(whoami)@127.0.0.1:5432/router0_test?sslmode=disable" \
	  AES_KEY='000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f' \
	  JWT_SECRET='router0-gate-jwt-secret-0123456789abcdef' && \
	go build ./... && \
	go vet ./... && \
	go vet -tags 'integration smoke' ./internal/integration/... && \
	go run ./cmd/migrate && \
	go test ./internal/... -count=1 && \
	go test -tags integration ./internal/integration/... -run 'TestPhase1|TestPhase2|TestPhase3' -count=1 -v && \
	! grep -q 'go-redis' go.mod && \
	! grep -rIqi 'zer0' dashboard/app dashboard/components && \
	(cd dashboard && $(NODE_ENV_FIX) pnpm install --frozen-lockfile && $(NODE_ENV_FIX) pnpm run build)

# Optional real-key smoke tests (not part of the gate). Each skips without its key.
smoke:
	@createdb router0_test 2>/dev/null || true
	$(TEST_ENV) TEST_GROQ_KEY="$$TEST_GROQ_KEY" TEST_OPENROUTER_KEY="$$TEST_OPENROUTER_KEY" \
	TEST_ANTHROPIC_KEY="$$TEST_ANTHROPIC_KEY" TEST_GEMINI_KEY="$$TEST_GEMINI_KEY" TEST_MESHAPI_KEY="$$TEST_MESHAPI_KEY" \
	go test -tags 'integration smoke' ./internal/integration/... -run 'TestSmoke' -count=1 -v

lint:
	golangci-lint run ./...
