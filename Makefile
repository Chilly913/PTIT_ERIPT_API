GO        ?= go
BIN       ?= bin
MIGRATE_N ?= 1

.PHONY: run worker build test test-db lint fmt tidy migrate-up migrate-down migrate-down-all migrate-version docker-build docker-up docker-down

## run: start the API (reads env; copy .env.example to .env and export it)
run:
	$(GO) run ./cmd/server

## worker: start the background worker
worker:
	$(GO) run ./cmd/worker

build:
	$(GO) build -o $(BIN)/server ./cmd/server
	$(GO) build -o $(BIN)/cli ./cmd/cli
	$(GO) build -o $(BIN)/worker ./cmd/worker

test:
	$(GO) test -race ./...

## test-db: also run DB integration tests (TEST_DATABASE_URL=postgres://...)
test-db:
	TEST_DATABASE_URL=$(TEST_DATABASE_URL) $(GO) test -race ./...

lint:
	$(GO) vet ./...
	@command -v staticcheck >/dev/null 2>&1 && staticcheck ./... || echo "staticcheck not installed, skipped"

fmt:
	gofmt -s -w .

tidy:
	$(GO) mod tidy

migrate-up:
	$(GO) run ./cmd/cli migrate up

migrate-down:
	$(GO) run ./cmd/cli migrate down $(MIGRATE_N)

migrate-down-all:
	$(GO) run ./cmd/cli migrate down all

migrate-version:
	$(GO) run ./cmd/cli migrate version

docker-build:
	docker build -t ptit-eript-api:local .

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down
