# PTIT_ERIPT_API

Go implementation of the ERIPT LMS backend API (rewrite of the Laravel backend).
Stack: Gin, GORM over pgx (PostgreSQL 17), go-redis, minio-go, golang-migrate, slog.

## Status
Phase 0 (foundation): config, DB/Redis/MinIO wiring, migrations CLI, worker
skeleton, Laravel-compatible HTTP contract (`internal/httpx`), health endpoints.
Domain endpoints are ported phase by phase — see `docs/MIGRATION_PLAN.md` and
`docs/LARAVEL_ROUTES.txt` (route inventory). Schema rules: `docs/db/CONVENTIONS.md`.

## Layout
```
cmd/server      HTTP API
cmd/worker      background job runner
cmd/cli         ops commands (migrate up|down [n|all]|steps n|version|force v)
migrations/     golang-migrate SQL (embedded into the binaries via embed.go)
internal/config typed env config (strict validation when APP_ENV=production)
internal/app    dependency container + Module interface (Register(api *gin.RouterGroup))
internal/db     GORM/pgx connection, slog query logger, migrator
internal/httpx  Laravel-compatible errors, 422 validation, paginator, Carbon timestamps
internal/server router, middleware (request id, access log, CORS), graceful shutdown
internal/storage MinIO wrapper (put, delete, presigned get, public URL)
internal/worker ticker-based job runner
```

## Run locally
Requires Go 1.26, PostgreSQL 17 and Redis (MinIO is only needed by upload features).

```bash
cp .env.example .env            # adjust DB_* / REDIS_* / S3_*
set -a; . ./.env; set +a        # the binaries read the process environment
make migrate-up                 # go run ./cmd/cli migrate up
make run                        # http://localhost:8080/health, /api/health
make worker                     # optional
```

Tests: `make test` (unit). DB integration tests run when `TEST_DATABASE_URL`
is set, e.g. `TEST_DATABASE_URL=postgres://eript:eript@127.0.0.1:5432/eript?sslmode=disable make test-db`.

## Docker Compose
```bash
docker compose up --build   # postgres, redis, minio, migrate (one-shot), api, worker
```

## HTTP contract (kept from Laravel)
- Errors: `{"message": "..."}` — 401 `Unauthenticated.`, 403, 404, 405, 429, 500 `Server Error`
  (details only when `APP_DEBUG=true`).
- Validation: 422 `{"message": "<first> (and N more errors)", "errors": {"field": ["..."]}}`
  using Laravel's English messages; use `httpx.BindJSON/Bind/BindQuery` with `binding:"..."` tags.
- Pagination: `httpx.ParsePage` + `httpx.Paginate` render LengthAwarePaginator JSON.
- Timestamps: `httpx.Time` → `2025-01-15T08:30:00.000000Z`; `httpx.Date` → `2025-01-15`.

## Consumers
- `PTIT_ERIPT_FE` (Nuxt) expects base URL `/api`
- `PTIT_ERIPT_APP` (Flutter) expects `API_URL=.../api`
- `PTIT_ERIPT_AI` (FastAPI) is called by this service via `AI_SERVICE_URL`
