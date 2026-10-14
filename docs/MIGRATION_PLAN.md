# Migration Plan: Laravel -> Go (PTIT_ERIPT_API)

This document plans the rewrite of the Laravel 13 backend (`backend/`) into a
standalone Go service living in this repository. It is the companion to the
containerised Go scaffold (currently a health-check skeleton).

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
| ORM | GORM (MySQL driver) |
| Auth | JWT (golang-jwt) replacing Sanctum |
| RBAC | table-based roles/permissions (port of Spatie) or casbin |
| Cache/sessions/queue | go-redis (Redis) |
| Mongo | official mongo-driver |
| Storage | minio-go |
| HTTP client | net/http + resty-like helpers |
| Config | env via `internal/config` |
| Migrations | golang-migrate (`migrations/*.sql`) |
| Logging | slog |
| Container | distroless final image |

## 3. Recommended strategy (strangler fig)

Do not big-bang. Run Laravel and Go side-by-side behind nginx/Cloudflare and
migrate domain-by-domain:

1. Freeze Laravel feature work; keep it as the reference implementation.
2. Implement shared contracts first: DB schema compatibility, auth token format
   decision, error response shape, pagination.
3. Migrate vertical slices (one controller group at a time) with parallel
   routes (`/api/v2/...` or header-based) behind a feature flag.
4. Move traffic slice-by-slice; keep Laravel as fallback for one release.
5. Decommission Laravel once FE/APP point at Go and smoke tests pass.

## 4. Phasing

### Phase 0 - Foundations (scaffold done in this repo)
- Project layout, config, health endpoints, Dockerfile, compose.
- CI: `go vet`, `go test`, build.
- Decision: keep MySQL schema identical initially (GORM AutoMigrate disabled in
  prod; use golang-migrate SQL files copied from Laravel migrations).

### Phase 1 - Auth & RBAC (UserManagement/AuthController)
- Port register/login/logout, Sanctum token -> JWT access + refresh.
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

- Keep **one** MySQL schema during migration to avoid dual-source-of-truth.
  Generate Go structs from the existing Laravel migrations; do not invent new
  columns unless required.
- Redis: Laravel uses redis for session/cache/queue. Go will use Redis for
  cache + rate limiting; sessions become stateless JWT; background jobs use a
  worker pool (asynq) instead of Laravel queue.
- MongoDB: chat history collections reused verbatim.
- MinIO: reuse bucket + key layout; only change is upload signing logic.

## 6. Auth contract (FE/APP compatibility)

- Keep response envelope: `{ "success", "message", "data" }` where FE expects it.
- Keep `/api/auth/login` path and `{ token, user }` shape to avoid FE changes.
- JWT in `Authorization: Bearer <token>`; middleware verifies like Sanctum.

## 7. Background jobs mapping

| Laravel | Go |
|---|---|
| queue worker | asynq worker process |
| scheduler (`schedule:work`) | cron library or asynq periodic tasks |
| `SendSystemNotificationEmail` job | asynq handler |
| `FlushAiRequestLogs` | asynq periodic handler |
| `avatars:publish` command | one-off CLI subcommand |

## 8. Streaming / AI relay

Keep the nginx `/_ai_relay/` + `X-Accel-Redirect` mechanism OR replace with a
direct SSE proxy in Go. Decision deferred to Phase 6; document the chosen
approach once latency measured.

## 9. Testing plan

- Unit: service layer per domain with repositories mocked.
- Integration: testcontainers-go against real MySQL/Redis/Mongo/MinIO.
- Contract: golden response comparison against Laravel for migrated endpoints
  (record Laravel responses, replay against Go, diff).
- E2E: run FE against Go staging for one full student + instructor + admin flow.

## 10. Risks

- Permission semantics (Spatie wildcards) must be replicated exactly.
- Date/timezone handling (Asia/Ho_Chi_Minh) in reports/attendance.
- File ownership & path style across OS (Windows dev vs Linux prod).
- PayOS webhook idempotency and callback URLs.
- Large seeder dataset; provide `make seed` parity.

## 11. Current scaffold scope

`cmd/server/main.go`, `internal/config`, `internal/server` implement only
health (`/health`, `/api/health`, `/api/ping`). All domain endpoints are TODO
and tracked via the route inventory. Incremental implementation follows the
phases above.
