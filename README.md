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

Quick start: `go run .` starts the API (same server as `./cmd/server`). It reads `.env` from the
working directory automatically; variables already set in the environment win over `.env`.

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
- `eript-web` (Next.js) reaches the API server-side via `API_URL` (see `eript-web/docs/frontend.md`)
- `PTIT_ERIPT_APP` (Flutter) expects `API_URL=.../api`
- `PTIT_ERIPT_AI` (FastAPI) is called by this service via `AI_SERVICE_URL`

## Dev data, uploads and chatbot
```bash
go run ./cmd/cli seed users      # demo accounts, one per role (password Demo@1234)
go run ./cmd/cli seed content    # Viện Kinh tế Bưu điện news, partners, site settings
go run ./cmd/cli seed academic   # program types, programs, years, terms, cohorts, classes
```
- Admin CRUD with sort/filter/CSV export+import: `/api/admin/news`, `/api/admin/partners`,
  `/api/admin/academic/{program-types,programs,academic-years,terms,cohorts,classes}`
  (built on the declarative toolkit in `internal/crud`).
- Images: `POST /api/uploads` (any signed-in user, png/jpg/gif/webp ≤ 5 MB) stores to `UPLOAD_DIR`
  and returns `/uploads/...`; the web app rewrites `/uploads/*` to `/api/uploads/*`.
- Chatbot: `POST /api/chatbot` forwards to the Python service (`AI_SERVICE_URL`).
  Start it with `cd ../PTIT_ERIPT_AI && python -m uvicorn main:app --port 8001` and choose the model with
  `CHATBOT_PROVIDER` / `CHATBOT_MODEL` / `CHATBOT_API_KEY`.

## Tệp tải lên, SCORM và bài nộp khi triển khai

Mọi tệp nằm dưới `UPLOAD_DIR` (mặc định `./uploads`), cần **ổ đĩa bền vững** (volume) khi chạy bằng Docker/K8s:

| Thư mục | Nội dung | Truy cập |
|---|---|---|
| `uploads/*` | ảnh tin tức, đối tác, logo | công khai qua `/api/uploads/*` (web rewrite `/uploads/*`) |
| `scorm/<uuid>/` | gói SCORM đã giải nén | công khai theo UUID qua `/api/scorm/<uuid>/*` (web: `/bff/scorm/<uuid>/*`) |
| `private/assignments/` | bài nộp của sinh viên | **không công khai**, chỉ tải qua API có xác thực |

- Gói SCORM phải chạy cùng origin với trang học (để tìm được SCORM API qua `window.parent`). Web đã proxy `/bff/scorm/*` về API nên chỉ cần reverse proxy chuyển `/bff/*` về web, không đặt SCORM ở tên miền khác.
- Reverse proxy cần cho phép body yêu cầu lớn: tệp SCORM tối đa 300 MB, bài nộp tối đa theo cấu hình bài tập (mặc định 10 MB), tệp import Excel/Word 10 MB. Với Nginx: `client_max_body_size 320m;` cho `/bff/` và `/api/`.
- Đừng cấu hình web server tĩnh phục vụ thẳng `UPLOAD_DIR`, vì sẽ lộ `private/`.
- Gửi email (đặt lại mật khẩu, xác nhận email) dùng `MAIL_MAILER`; `log` chỉ ghi nội dung email vào log, đặt `smtp` và `MAIL_*` cho môi trường thật.

## Certificates, reports and question import

- **Certificates**: an instructor ticks "Cấp chứng chỉ" in the course info tab (`PUT /courses/:ref` with `issue_certificate`). A learner who has completed every lesson claims it with `POST /courses/:ref/certificate`; `GET /my/certificates` lists them and `GET /certificates/:credential` (public) verifies one. The web app renders a printable page at `/certificates/<credential>`.
- **Progress report**: `GET /courses/:ref/report` (owner/admin) returns class totals and one row per learner (lessons, assignments, quizzes, best exam score, attendance, certificate, "at risk" when inactive for 14 days); `GET /courses/:ref/report/export` is the same table as Excel.
- **Dashboard**: `GET /me/dashboard` fills `pending_assignments` (unsubmitted, overdue ones included) and `recent_certificates`.
- **Word import** reads typed "Câu 1:" lists (including Word auto-numbering) and tables with one question per row (header: Câu hỏi | A | B | C | D | Đáp án, optional Loại, Nhóm, Độ khó, Điểm, Giải thích). Images become `[hình ảnh]` placeholders, equations are read as plain text, questions without options or key become essays. Samples: `docs/mau-nhap-cau-hoi/`.
- Tests: `go test ./...`; set `TEST_DATABASE_URL` to also run the DB tests (each runs in a rolled-back transaction).
