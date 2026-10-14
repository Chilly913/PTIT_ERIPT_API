# PTIT_ERIPT_API

Go implementation of the ERIPT LMS backend API (migrating from Laravel).

## Status
Scaffold only: health endpoints (`/health`, `/api/health`, `/api/ping`). See
`docs/MIGRATION_PLAN.md` for the phased migration from Laravel and
`docs/LARAVEL_ROUTES.txt` for the full route inventory (352 endpoints).

## Run locally
```bash
cp .env.example .env
go mod download
go run ./cmd/server
# http://localhost:8080/health
```

## Run with Docker Compose
```bash
docker compose up --build
```

## Consumers
- `PTIT_ERIPT_FE` (Nuxt) expects base URL `/api`
- `PTIT_ERIPT_APP` (Flutter) expects `API_URL=.../api`
- `PTIT_ERIPT_AI` (FastAPI) is called by this service via `AI_SERVICE_URL`
