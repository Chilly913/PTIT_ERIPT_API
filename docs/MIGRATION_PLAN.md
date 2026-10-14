# Migration Plan: Laravel -> Go (PTIT_ERIPT_API)

This document plans the rewrite of the Laravel 13 backend (`backend/`) into a
standalone Go service living in this repository. Phase 0 (foundation) is
implemented; see README.md for the package layout.

## 1. Current state (source of truth)

- Framework: Laravel 13 (PHP 8.3), Sanctum, Spatie Permission, PayOS SDK,
  Socialite, Flysystem S3.
- Databases: MySQL 8 (primary), MongoDB 7 (chat/logs), Redis 7 (cache/queue/sessions).
- Storage: MinIO (S3-compatible).
- 43 controllers under `app/Http/Controllers/{AIManagement,CourseManagement,
  PaymentManagement,QuizManagement,Survey,UserManagement}`.
- ~85 migrations; models under `app/Models`.
- HTTP surface: 352 `Route::` declarations across `routes/api/*.php` (see
  `docs/LARAVEL_ROUTES.txt`). Frontend (`PTIT_ERIPT_FE`) and mobile
  (`PTIT_ERIPT_APP`) consume these under the `/api` prefix.
- External integration: `ai-service` (FastAPI) called via `AiServiceClient`,
  plus PayOS and Google OAuth.

## 2. Target stack

| Concern | Choice |
|---|---|
| HTTP router | Gin |
| Database | PostgreSQL 17, new schema (no MySQL, no MongoDB) |
| ORM | GORM over pgx (`gorm.io/driver/postgres`) |
| Auth | opaque bearer tokens, stored hashed in Postgres (Sanctum-like; no JWT) |
| RBAC | table-based roles/permissions (port of Spatie) |
| Cache/rate limit | go-redis (Redis) |
| Background jobs | `cmd/worker` ticker runner (asynq later if queues are needed) |
| Storage | minio-go |
| HTTP client | net/http + resty-like helpers |
| Config | env via `internal/config` |
| Migrations | golang-migrate (`migrations/*.sql`) |
| Logging | slog |
| Container | distroless final image |

## 3. Strategy (port per domain, cut over per release)

The Go service uses a new PostgreSQL schema, so it cannot share a database
with Laravel and a route-by-route strangler is not possible. Instead:

1. Freeze Laravel feature work; keep it as the reference implementation.
2. Shared contracts first (done in Phase 0): error JSON, 422 validation shape,
   paginator JSON, timestamp format.
3. Port domains phase by phase in Go, verified with contract tests (recorded
   Laravel responses replayed against Go).
4. Cut over per release: freeze writes, migrate data MySQL -> PostgreSQL with a
   one-off ETL (column mapping in `docs/db/*.md`), switch nginx `/api` to Go.
   Laravel + the MySQL snapshot stay available for rollback for one release.
5. Decommission Laravel once FE/APP smoke tests pass on Go.

## 4. Phasing

### Phase 0 - Foundations (done)
- Layout, typed config, GORM/pgx, Redis, MinIO, slog, health, graceful shutdown.
- `cmd/cli migrate ...` (golang-migrate, SQL embedded), `cmd/worker` skeleton.
- `internal/httpx`: Laravel-compatible errors, validation, pagination, timestamps.
- Dockerfile (server/cli/worker), compose (postgres/redis/minio/migrate/api/worker), CI.
- Schema: new PostgreSQL design in `migrations/` (rules in `docs/db/CONVENTIONS.md`);
  GORM AutoMigrate is never used.

### Phase 1 - Auth & RBAC (UserManagement/AuthController)
- Port register/login/logout; Sanctum tokens -> opaque bearer tokens (random,
  SHA-256 hashed in Postgres, revocable), same `Authorization: Bearer` header.
- Google OAuth (Socialite) -> golang.org/x/oauth2.
- User/role/permission models and middleware parity with Spatie.
- Acceptance: FE login flow works against Go for all roles.

### Phase 2 - Catalog & learning (CourseManagement + Course/Section/Lesson)
- Categories, courses, sections, lessons, attachments, scorm, virtual classes,
  progress, notes, Q&A.
- MinIO upload handling compatible with existing object keys.

### Phase 3 - Quiz & exam + proctoring (QuizManagement)
- Question bank, quiz configs, attempts, auto-grading, live proctor events
  (pause/resume/force-stop).

### Phase 4 - Academic / L&D (academic management, attendance, offline sessions,
  grades, tuitions, curricula)
- Largest surface; migrate after core LMS is stable.

### Phase 5 - Career / marketplace (CareerPath, orders, PayOS, certificates,
  points, reviews, news, notifications)

### Phase 6 - AI proxy & chat (AIChatController, AiManagement, AiServiceClient)
- Proxy to FastAPI ai-service, persist chat history, streaming relay via nginx
  (keep `AI_STREAM_RELAY` semantics).

### Phase 7 - Cleanup
- Remove Laravel code, update compose/frontend/mobile base URLs, rotate secrets.

## 5. Data layer mapping

- New PostgreSQL 17 schema designed from scratch (`migrations/`, conventions in
  `docs/db/CONVENTIONS.md`, per-domain notes with old -> new column mapping in
  `docs/db/*.md`). No database is shared with Laravel.
- Data moves once per cutover via an ETL from the MySQL snapshot (not written yet).
- MongoDB is dropped: chat history/logs move to PostgreSQL tables (`jsonb` where needed).
- Redis: cache + rate limiting only; no sessions (opaque tokens live in Postgres).
- MinIO: reuse bucket + key layout; DB stores object keys, URLs built at response time.

## 6. Auth contract (FE/APP compatibility)

- Keep response envelope: `{ "success", "message", "data" }` where FE expects it.
- Keep `/api/auth/login` path and `{ token, user }` shape to avoid FE changes.
- Opaque token in `Authorization: Bearer <token>`; middleware looks up the hash
  like Sanctum. Existing Sanctum tokens are not migrated (users log in again).

## 7. Background jobs mapping

| Laravel | Go |
|---|---|
| queue worker | `cmd/worker` (asynq if real queues are needed) |
| scheduler (`schedule:work`) | `worker.Runner` periodic jobs |
| `SendSystemNotificationEmail` job | worker job |
| `FlushAiRequestLogs` | worker periodic job |
| `avatars:publish` command | one-off CLI subcommand |

## 8. Streaming / AI relay

Keep the nginx `/_ai_relay/` + `X-Accel-Redirect` mechanism OR replace with a
direct SSE proxy in Go. Decision deferred to Phase 6; document the chosen
approach once latency measured.

## 9. Testing plan

- Unit: service layer per domain with repositories mocked.
- Integration: real PostgreSQL/Redis (CI service containers, `TEST_DATABASE_URL`).
- Contract: golden response comparison against Laravel for migrated endpoints
  (record Laravel responses, replay against Go, diff).
- E2E: run FE against Go staging for one full student + instructor + admin flow.

## 10. Risks

- Permission semantics (Spatie wildcards) must be replicated exactly.
- Date/timezone handling (Asia/Ho_Chi_Minh) in reports/attendance.
- File ownership & path style across OS (Windows dev vs Linux prod).
- PayOS webhook idempotency and callback URLs.
- Large seeder dataset; provide `make seed` parity.

## 11. Current scope

Phase 0 only: `/health` (liveness), `/api/health` (DB + Redis), JSON 404/405.
All domain endpoints are TODO and tracked via the route inventory.
