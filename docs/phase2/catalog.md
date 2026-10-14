# Phase 2 — catalog module (`internal/modules/catalog`)

Ports `CategoryController`, `CourseController` (index/show/categories/myCoures/store/update/destroy/publish),
`SectionController` and `LessonController` from `backend/routes/api/course.php`.

Wiring (lead, `cmd/server/main.go`): `catalog.New(a, roles, tokens)` where `tokens` is the shared
`*auth.TokenService` (e.g. `authmod.Tokens()`).

Shared additions in `internal/learning` (new file `lesson.go`, existing signatures untouched):
`LessonRef`/`FindLessonRef`, `SectionRef`/`FindSectionRef`, `RouteID`, `ModelNotFoundMessage`,
`CourseNotFoundMessage`, `FormatSizeMB` (legacy `"12.5MB"` size strings for `video_size` / attachment `file_size`).

## Routes

| Method | Path | Middleware | Status |
|---|---|---|---|
| GET | /categories | — | done |
| GET | /categories/:category | — | done |
| GET | /courses/categories | — | done |
| GET | /courses | — | done |
| GET | /courses/:course | optional auth | done |
| GET | /courses/:course/lessons | optional auth | done |
| GET | /courses/:course/lessons/:lesson | optional auth | done |
| GET | /courses/:course/lessons/:lesson/video-url | optional auth | done |
| GET | /courses/:course/sections | optional auth | done |
| GET | /my-courses | auth | done |
| POST | /courses | auth, manage_courses | done |
| PUT | /courses/:course | auth, manage_courses | done (also POST + `_method=PUT`) |
| DELETE | /courses/:course | auth, manage_courses | done (also POST + `_method=DELETE`) |
| POST | /courses/:course/publish | auth, manage_courses | done |
| POST | /courses/:course/sections | auth, manage_courses | done |
| GET/PUT/DELETE | /sections/:section | auth, manage_courses | done |
| POST | /courses/:course/sections/reorder | auth, manage_courses | done |
| POST | /courses/:course/lessons | auth, manage_lessons\|manage_courses | done |
| PUT/DELETE | /courses/:course/lessons/:lesson | auth, manage_lessons\|manage_courses | done |
| POST | /courses/:course/lessons/:lesson/upload-video | auth, manage_lessons\|manage_courses | done |
| POST | /courses/:course/lessons/reorder | auth, manage_lessons\|manage_courses | done |

Middleware order follows Laravel's sorted middleware: auth (401) → route model binding (404) → permission (403).
Binding 404 messages are Laravel's: `No query results for model [App\Models\Course].` (custom
`resolveRouteBinding`, firstOrFail) and `No query results for model [App\Models\Lesson|Section|Category] <key>`.

## Output shape

Legacy names/order rebuilt from new columns: `user_id`←instructor_id, `thumbnail`←resolved thumbnail_path,
lesson `order`←position, `duration`←duration_seconds, `video_url`←resolved video_path,
`video_size`←`FormatSizeMB(video_size_bytes)`, virtual class / offline session `duration`←duration_minutes,
assignment `max_file_size`←max_file_size_kb and `allowed_extensions` joined with `,`, scorm `entry_url`←entry_path,
`price` as integer (legacy cast). `program_type_id` and `curriculum_id` (dropped columns) are emitted as `null`.

## Deliberate deviations

- **Media URLs resolved everywhere**: legacy returned raw `video_url` in lesson lists/section tree/course detail and
  raw `entry_url` in the section tree; Go always returns the public URL (keys are MinIO keys now).
- **Video uploads go to object storage** under `lessons/videos/<Y-m-d>/<time>_<rand>.<ext>` (legacy: local
  `public/uploads/<Y-m-d>/…`). The PHP-specific 413 checks (post_max_size / upload_max_filesize) are gone; size is
  enforced by the `max:1024000` KB rule. Old video objects are deleted only when they are our own keys.
- **Lesson `section_id` must belong to the course** (422 "The selected section id is invalid."); legacy accepted any
  section id, the new composite FK would otherwise 500.
- **Offline session `qr_token`/`qr_expires_at` hidden from non-owners** in lesson show (a learner could check in
  without scanning). Owners/admins still see them.
- **Prices rounded to whole units on write** (legacy column was decimal(12,0)).
- **Empty slug fallback**: titles without any ASCII-transliterable characters get slug `course` (+`-n`); legacy stored `""`.
  `Str::slug` transliteration covers Vietnamese and common Latin accents; other scripts are dropped.
- **Pagination tie-break** `id DESC` added to every course listing order (MySQL left ties unordered);
  `rating` sort uses `NULLS LAST` to match MySQL.
- **Store response** returns the full course row (legacy returned only the attributes it had set).
- `learning_outcomes`/`benefits`/`requirements` given as JSON objects are stored as their value list
  (the column CHECK requires an array); `null` is stored as `[]`.
- Malformed JSON bodies are treated as empty input (Laravel behaviour → 422), not 400.
- Method spoofing (`_method` / `X-HTTP-Method-Override`) is handled only for `POST /courses/:course` (the course
  editor sends `thumbnail_file` that way). A global method-override middleware may be preferable — lead decision.

## Kept from Laravel on purpose (flagged)

- `GET /courses?status=draft` lists unpublished courses publicly; `GET /courses/{course}` shows drafts.
- Course detail `lessons` contains every lesson with `video_url` for anonymous visitors (lesson index hides it).
- `PUT /courses/{course}` lets the owner set `status` directly (including `published`).
- `GET /sections/{section}` is readable by any `manage_courses` holder (no ownership check).
- Lesson show returns `virtual_classes.meeting_password` / `start_url` to learners (open question 5 of `docs/db/03_learning.md`).
- `POST /courses` re-checks `Authorize::allows([...])` and answers 403 `"Unauthorized"` (unreachable after the
  permission middleware, kept).

## Side effects / deferred

- No notifications or audit events are emitted by these controllers in Laravel; nothing deferred.
- Deleting a course does not delete its media objects (same as legacy).
- Attachments, virtual classes, SCORM, offline sessions, assignments and quizzes are only **read** here for embedding
  (lesson show, owner section tree); their CRUD lives in the other phase-2/3/4 modules.

## Tests

- Unit: slug, PHP casts, form bracket parsing, validator messages, shared helpers, route registration (gin wildcard
  conflicts with sibling routes).
- Integration (`TEST_DATABASE_URL`): categories tree/counts/filters, full course lifecycle (create, slug suffix,
  spoofed multipart update with thumbnail, sections, lessons, publish, search, my-courses, visibility, lesson show
  with every relation, video-url, upload-video, path suggestions, deletes). Run green against a scratch PG17 DB.

## Open questions

1. Hide `video_url` of locked lessons in `GET /courses/{course}` for non-enrolled visitors?
2. Restrict `?status=` on the public catalog to owners/admins?
3. Global `_method` override middleware in `internal/server` instead of the per-route handling here?
4. Should `published_at` be set when a course becomes `published` (admin approval lives in another module)?
