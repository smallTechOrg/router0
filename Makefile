.PHONY: dev test test-integration migrate build lint

dev:
	GO111MODULE=on go run ./cmd/router

build:
	GO111MODULE=on go build -o bin/router ./cmd/router

test:
	GO111MODULE=on go test ./internal/... -count=1 -v

test-integration:
	source .env && GO111MODULE=on go test ./internal/... -tags integration -count=1 -v

migrate:
	source .env && GO111MODULE=on go run ./cmd/router migrate

lint:
	golangci-lint run ./...
