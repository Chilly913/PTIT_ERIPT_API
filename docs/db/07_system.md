# 07 — System (`000007_system`)

## 1. Tables

| Table | Purpose |
|---|---|
| `notifications` | In-app notifications, one row per recipient (custom table, not Laravel's polymorphic `notifications`). |
| `news_posts` | Public news / announcements (draft / published), optional notify-students fan-out. |
| `site_settings` | Key/value site configuration (branding, contact, SMTP, locale). |
| `ai_settings` | Singleton AI provider configuration + monthly token quota counter. |
| `ai_request_logs` | Append-only log of AI calls (bulk-flushed from Redis every 15 s). |
| `experience_survey_responses` | One experience-survey answer set per user (JSON). |

No deferred FKs: all references go to `users` (000002).

## 2. Changes vs legacy

### notifications
- Model `App\Models\Notification` + `NotificationController`: list newest-first per user (paginated), unread count, mark one read, mark all read, admin broadcast fan-out (`Notification::send` per user), admin dashboard "latest 5".
- Indexes: `(user_id, id DESC)` for the paginated list (Go orders by `id DESC`, equivalent to legacy `created_at DESC` since ids are monotonic) and partial `(user_id) WHERE read_at IS NULL` for the unread badge (index-only) and "mark all read". Replaces legacy `(user_id, read_at)`.
- `type` CHECK with the 11 values written in `backend/app` (`announcement, news, enrollment, academic_warning, course_approved, course_rejected, exam_paused, exam_resumed, exam_force_stopped, exam_time_extended, exam_violation`). New types need a migration.
- Length CHECKs from validators (title 255, message 5000, link 500).
- `updated_at` **dropped**: rows only change by setting `read_at`; no consumer reads `updated_at` (grep frontend/admin-ui/mobile). If the JSON must still carry it, Go emits `coalesce(read_at, created_at)`.

### news_posts
- `status` CHECK `IN (draft, published)`; CHECK published ⇒ `published_at IS NOT NULL` (the controller always sets it when publishing; the public scope requires it).
- `cover_image` -> `cover_image_path` (object key).
- Indexes: partial `(published_at DESC) WHERE status='published'` for the public feed (replaces `(status, published_at)`); GIN trigram on `title` and `excerpt` for the admin `LIKE %q%` search; `author_id`.
- `slug` stays `text` unique (generated lowercase by `Str::slug`).

### site_settings
- Kept key/value (31 keys, set-many/get-all API that echoes arbitrary keys).
- Key CHECK `^[a-z][a-z0-9_]{0,99}$`.
- **New `value_encrypted bytea`**: legacy stored `smtp_password` in plaintext. Go encrypts secret keys (currently only `smtp_password`) with its own key into `value_encrypted`; CHECK ensures only one of `value` / `value_encrypted` is set. Secret keys must never be echoed by the admin GET.

### ai_settings
- Singleton enforced by `uq_ai_settings_singleton ON ((true))` (legacy relied on `firstOrCreate`).
- `api_key` (Laravel `encrypted` cast, APP_KEY-bound, 256-char base64 payload) -> `api_key_encrypted bytea`. The Go app encrypts with its own key (e.g. AES-256-GCM, nonce prefixed); legacy ciphertext cannot be decrypted without the Laravel APP_KEY — re-enter the key after migration (the code already falls back to env keys).
- `provider` CHECK `IN (chatgpt, gemini, claude, openrouter, ollama)` (validator). Default aligned with `AiSetting::current()` (`gemini`, `gemini-2.5-flash`) instead of the migration's stale `chatgpt/gpt-4o-mini`.
- `monthly_token_quota`, `tokens_used` int -> `bigint` (tokens_used grows monotonically between resets); CHECKs ≥ 0; `max_requests_per_minute` CHECK 1..1000.

### ai_request_logs
- Writers: `FlushAiRequestLogs` (Redis list -> batch INSERT every 15 s, plus `ai_settings.tokens_used += sum`) and direct inserts from `AiServiceClient` / `CareerAdvisorController`. Readers: admin AI dashboard only (all-time count/sum/avg, group by endpoint/provider, daily counts for 14 days, latest 20 with user).
- Lean, append-only: `updated_at` dropped (always equal to `created_at`); only two secondary indexes: `created_at` (latest-N and 14-day window) and partial `user_id WHERE user_id IS NOT NULL` (FK maintenance + per-user; guest-chat rows unindexed). No partitioning.
- FK `user_id -> users ON DELETE SET NULL` kept. Legacy batch inserts failed when a user had just been deleted; Go should insert via `INSERT ... SELECT ... FROM unnest($1::bigint[], ...) LEFT JOIN users` (unknown user -> NULL) or `COPY` into a temp table, so one bad row never blocks the batch.
- `status` CHECK `IN (success, error)`; `tokens_used`, `response_time_ms` CHECK ≥ 0. `endpoint`, `provider`, `model` free text (logged as reported by ai-service, includes fallback providers).

### experience_survey_responses
- `answers json` -> `jsonb` with CHECK object (keyed by question id, see `ExperienceSurveyDefinition`).
- `submitted_at` gets `DEFAULT now()`; index changed to `submitted_at DESC` (admin list order).

### Dropped
| Item | Reason |
|---|---|
| `notifications.updated_at` | Only `read_at` changes; unused by consumers. |
| `ai_request_logs.updated_at` | Append-only; always equal to `created_at`. |
| `ai_settings.api_key` (plaintext semantics) | Replaced by `api_key_encrypted` (Go-side encryption). |

## 3. Old -> new column mapping

| Legacy | New | Transform |
|---|---|---|
| `notifications.id, user_id, type, title, message, link, read_at, created_at` | same | timestamps -> timestamptz |
| `notifications.updated_at` | — | dropped |
| `news_posts.id, author_id, title, slug, excerpt, content, status, published_at, created_at, updated_at` | same | |
| `news_posts.cover_image` | `news_posts.cover_image_path` | strip media base URL -> object key |
| `news_posts.is_featured` | `news_posts.is_featured` | tinyint -> boolean |
| `site_settings.id, key, value, created_at, updated_at` | same | `smtp_password`: encrypt into `value_encrypted`, set `value = NULL` |
| `ai_settings.id, provider, model, monthly_token_quota, tokens_used, max_requests_per_minute, is_active, quota_reset_at, created_at, updated_at` | same | ints -> bigint where noted |
| `ai_settings.api_key` | `ai_settings.api_key_encrypted` | decrypt with Laravel APP_KEY, re-encrypt with Go key (or leave NULL and re-enter) |
| `ai_request_logs.id, user_id, endpoint, provider, model, tokens_used, response_time_ms, status, error_message, created_at` | same | |
| `ai_request_logs.updated_at` | — | dropped |
| `experience_survey_responses.*` | same | `answers` json -> jsonb |

## 4. Open questions

1. Broadcast to "all users" writes N rows synchronously. Acceptable at current scale (~hundreds of users); if it grows, move to a `broadcasts` table + per-user read receipts. Keep fan-out for now?
2. Retention for `ai_request_logs` (and old read notifications)? Dashboard aggregates are all-time full scans; if logs grow large, add a daily rollup table or limit stats to a window.
3. Should `site_settings` secrets beyond `smtp_password` exist (e.g. PayOS keys currently in env)?
