# Phase 2 — module `study` (`internal/modules/study`)

Ports the learner-side lesson endpoints: lesson progress (both `LessonProgressController`s), `GET /enrollments`,
lesson notes, lesson attachments and lesson assignments (`AssignmentController`).

Wiring (not done here, `cmd/server/main.go` is not owned by this module):

```go
study.New(a, rbacSvc, authModule.Tokens()) // options: study.WithStore, study.WithClock
```

## Routes

All routes are behind `auth:sanctum` (`TokenService.Required()`). "manage" = `permission:manage_lessons|manage_courses`
(`rbac.RequirePermission`, admin bypass). Route params: `:course` (id or slug), `:lesson`, `:note`, `:attachment`, `:submission`.

| Method | Path | Legacy | Status |
|---|---|---|---|
| GET | /courses/{course}/lessons/{lesson}/progress | CourseManagement\LessonProgressController@show | done |
| PUT | /courses/{course}/lessons/{lesson}/progress | …@upsert | done |
| POST | /courses/{course}/lessons/{lesson}/progress | UserManagement\LessonProgressController@updateProgress | done (+ completion side effects) |
| GET | /courses/{course}/progress | …@courseProgress | done |
| GET | /enrollments | …@enrollments (incl. planned curriculum courses) | done |
| GET/POST | /courses/{course}/lessons/{lesson}/notes | LessonNoteController@index/store | done |
| PUT/DELETE | /courses/{course}/lessons/{lesson}/notes/{note} | …@update/destroy | done |
| GET | …/lessons/{lesson}/attachments | LessonAttachmentController@index | done |
| GET | …/attachments/{attachment}/download | …@download | done |
| POST | …/lessons/{lesson}/attachments (manage) | …@store | done |
| DELETE | …/attachments/{attachment} (manage) | …@destroy | done |
| GET | …/lessons/{lesson}/assignment | AssignmentController@show | done |
| POST | …/lessons/{lesson}/assignment/submit | …@submit | done |
| POST | …/lessons/{lesson}/assignment (manage) | …@store | done |
| GET | …/lessons/{lesson}/assignment/submissions (manage) | …@indexSubmissions | done |
| GET | /courses/{course}/assignments/submissions (manage) | …@courseSubmissions | done |
| PUT | /courses/{course}/assignments/submissions/{submission} (manage) | …@grade | done |

Error messages kept from legacy: `{"message":"Not found"}` (lesson of another course / attachment of another lesson),
`"Enrollment required"` (403), `"Forbidden"` (403, non-owner instructor / someone else's note),
`"No assignment found"`, `"No assignment for this lesson"`, `"Assignment not found"`, grade's `abort_unless(...,404)` →
`{"message":""}`. Failed route bindings render Laravel's `ModelNotFoundException` text:
`No query results for model [App\Models\Course].` (course binding uses `firstOrFail`, no key) and
`No query results for model [App\Models\Lesson] 5` (Lesson / LessonNote / LessonAttachment / AssignmentSubmission).
Order of checks: auth → permission middleware → bindings → controller checks.

Validation is done on a loosely typed payload (JSON, urlencoded or multipart; TrimStrings + ConvertEmptyStringsToNull),
so `"5"` passes `integer`, `0/1/"0"/"1"` pass `boolean`, and messages follow `lang/en/validation.php`
(e.g. `after_or_equal` → "The due at field must be a date after or equal to submission open at.").

## JSON rebuilt from the new schema

- **lesson_progress**: `completed` = `completed_at IS NOT NULL`; `metadata` always `null` (moved to `scorm_runtime_states`);
  legacy `id` **omitted** (composite PK, no surrogate). Always the full row shape (legacy returned a partial attribute set
  right after `firstOrCreate`).
- **lesson_attachments**: `file_size` rebuilt from `size_bytes` as `round(MB, 2) . "MB"` ("1.8MB", "2MB"); `url` = public URL of `file_path`.
- **lesson_assignments**: `max_file_size` ← `max_file_size_kb`; `allowed_extensions` = `text[]` joined with `,`.
- **assignment_submissions**: `file_url` = public URL rebuilt from `file_path` (key stored, not URL); `grade` keeps the
  `decimal:2` string ("8.50").
- **courseProgress / enrollments**: `percent` / `progress` render like PHP floats (`100.0`, `33.3`); integer `0` when
  the course has no lessons. `order`/`duration` ← `position`/`duration_seconds`.
- **enrollments**: `course` = `id,user_id(instructor_id),title,thumbnail(public URL),price(int, truncated),status,category_id`
  + `category{id,name}` + `instructor{id,name,avatar}`; `term` is the `termWindow` object
  (`display_name`, `academic_year{id,name}`), plus `starts_at/ends_at/window_status/is_planned`. Planned items: id = −course_id.
  Term window uses UTC dates (legacy `app.timezone` = UTC).

## Side effects

Implemented (tables exist):
- `updateProgress` with `completed`: course certificate row (`user_certificates`, `CERT-XXXXXXXXXX`) when the course has a
  `certificate_template_id` and all lessons are completed — race-free via the partial unique index.
- `CareerPathFulfillmentService::refreshProgressForUserCourse`: recompute `user_career_paths.progress_percent` for paths
  where the course is required; on 100% set `completed`/`completed_at` and issue the path certificate row (`PATH-…`).
- `saveAssignment` sets `lessons.type = 'assignment'` (as legacy).
- Upload/delete of objects through `media.ObjectStore`; failed DB insert after upload removes the object.

All completion hooks live in `completion.go` → `Module.onLessonCompleted` (the hook point for future notifications,
certificate rendering, points). Side-effect failures are logged; the progress save still returns 200 (legacy would 500).

Deferred / not applicable: none of these controllers award points, send notifications or mail, and legacy only inserts the
certificate row (no PDF at issue time) — so nothing is deferred beyond what a future certificate-rendering/notification
module may want to add at the hook.

## Deviations from Laravel

1. Hardening: instructor writes check that the lesson belongs to `{course}` (404 "Not found"): attachment upload/delete,
   assignment save, lesson submissions. Legacy let the owner of course A modify lessons of course B via A's URL.
2. `lesson_progress.id` absent from JSON; `metadata` always null (see above).
3. `allowed_extensions` is normalised (lower-case, trimmed, empties dropped); a value with no extension falls back to the default
   (`text[]` must be non-empty).
4. Explicit `position_seconds: null` on note update keeps the stored value (legacy hit NOT NULL → 500).
5. Integer inputs stored in `integer` columns get `max:2147483647` (legacy: DB error).
6. Uploaded file MIME is sniffed with `http.DetectContentType`, falling back to the extension for zip/octet-stream/text
   (legacy used finfo) — docx/xlsx may differ slightly.
7. Note update always bumps `updated_at` (trigger), even when nothing changed.
8. `POST …/progress` with `completed=false` clears `completed_at` (legacy kept a stale `completed_at` with `completed=0`).

## Open questions

1. Legacy has **no enrollment check** on `GET …/attachments`, `…/download`, `GET …/assignment` and
   `POST …/assignment/submit` (any authenticated user can read files or submit to any lesson's assignment; submit does not
   even check the lesson belongs to `{course}`). Kept for compatibility — should these require `CanAccessLessons`?
2. `submit` ignores the assignment's `max_file_size`, `allowed_extensions` and open/due dates (legacy). Enforce?
3. Download returns the public `/minio/...` URL like legacy; switch to `storage.PresignedGet` if the bucket becomes private?
4. Resubmission keeps the previous grade/feedback and does not delete the old file (legacy). Intended?

## Tests

- Unit (`study_test.go`): upsert/auto-completion rules, percent rounding + PHP float JSON, file size format, extensions,
  loose type rules and messages, term window/display name, MIME detection.
- Integration (`integration_test.go`, gated by `TEST_DATABASE_URL`): progress flow incl. certificates and career path,
  course progress, enrollments (enrolled + planned curriculum), notes, attachments (fake store), assignments/grading.
  Ran green against a scratch PG17 DB (`eript_p2_study` on eript-go-pg, dropped afterwards).
