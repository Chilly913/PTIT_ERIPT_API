# Phase 2 — module `interaction`

Package `internal/modules/interaction` ports `ReviewController`, `CourseQaController`,
`VirtualClassController`, `ScormPackageController` and `ScormController`
(`backend/routes/api/course.php`).

Constructor: `interaction.New(a, roles *rbac.Service, tokens *auth.TokenService, opts...)`,
options `WithStore(ObjectStore)` and `WithScormLimits(ScormLimits)`. Not yet wired in
`cmd/server/main.go`.

## Routes

| Method | Path | Middleware | Status |
|---|---|---|---|
| GET | /courses/:course/reviews | public | done |
| POST | /courses/:course/reviews | auth, permission:submit_reviews | done |
| PUT | /courses/:course/reviews/:review | auth, permission:submit_reviews | done |
| DELETE | /courses/:course/reviews/:review | auth, permission:submit_reviews | done |
| GET | /courses/:course/qas | auth | done |
| POST | /courses/:course/qas | auth | done |
| GET | /courses/:course/qas/:qa | auth | done |
| POST | /courses/:course/qas/:qa/replies | auth | done |
| POST | /courses/:course/qa-reactions | auth | done |
| GET | /courses/:course/lessons/:lesson/virtual-class | auth | done |
| POST | /courses/:course/lessons/:lesson/virtual-class | auth, permission:manage_lessons\|manage_courses | done |
| DELETE | /courses/:course/lessons/:lesson/virtual-class | auth, permission:manage_lessons\|manage_courses | done |
| GET | /courses/:course/lessons/:lesson/scorm-package | auth | done |
| POST | /courses/:course/lessons/:lesson/scorm-package | auth, permission:manage_lessons\|manage_courses | done |
| DELETE | /courses/:course/lessons/:lesson/scorm-package | auth, permission:manage_lessons\|manage_courses | done |
| POST | /courses/:course/lessons/:lesson/scorm/track | auth | done |

Middleware order is the same as Laravel: auth (401), then route-model bindings (404),
then Spatie permission (403), then the controller. Binding 404 bodies use Laravel's
ModelNotFound messages: `No query results for model [App\Models\Course].` (course
binding uses `firstOrFail`) and `No query results for model [App\Models\Lesson|Review|CourseQa] <key>`.
The controllers' own checks keep their messages (`Not found`, `Forbidden`, Vietnamese texts).

Validation is a small Laravel-rule engine in `input.go` (required, sometimes, nullable,
string, integer, numeric, min/max, in, url, date, file, mimes:zip, exists). It reads JSON,
urlencoded and multipart bodies plus the query string, trims strings and turns `""` into
null (TrimStrings / ConvertEmptyStringsToNull), and gives Laravel's 422 messages in rule order.

## Mapping to the new schema
- `qa_reactions.course_qa_id / course_qa_reply_id` → legacy `reactable_type`
  (`App\Models\CourseQa` / `App\Models\CourseQaReply`) and `reactable_id`, rebuilt in the
  `reactions` arrays of `GET /qas`.
- `virtual_classes.duration_minutes` → `duration`.
- `scorm_packages.entry_path` → `entry_url`, resolved like `resolvePublicUrl`
  (`/minio/<bucket>/<key>`; absolute URLs and `/minio/` paths pass through).
- `users.avatar_path` → `user.avatar` through `media.Resolver.Public` (PublicMediaUrl).
- SCORM track: `lesson_progress.completed_at` (now() if completed, NULL otherwise, like the
  legacy `updateOrCreate`) + `scorm_runtime_states` (was `lesson_progress.metadata`).

## Deviations from Laravel
1. **IDOR fix**: `DELETE .../virtual-class` and `DELETE .../scorm-package` now check that the
   lesson belongs to `{course}` (404 `Not found`). Laravel only checked ownership of `{course}`,
   so any instructor could delete another course's virtual class / SCORM files.
2. SCORM zip problems (cannot open, no entry file, unsafe path, too large, too many files)
   answer **422** `{"message": ...}`; Laravel threw a RuntimeException (500).
3. SCORM upload safety: rejects zip-slip paths (`..`, absolute, drive letters, NUL), symlinks
   and other non-regular entries; limits: zip 200 MB (Laravel `max:204800`), 1 GiB unpacked,
   20 000 files, 8 MB manifest. Request body capped at limit + 8 MB (413 above).
   Dot files are not uploaded (Laravel's `File::allFiles` skipped them too). Backslashes in
   entry names are treated as `/`. Duplicate entries: last one wins (as `extractTo`).
4. The new package is inspected **before** the old `scorm/<uuid>/` objects are deleted, so a
   bad re-upload keeps the previous package (Laravel deleted first). On a failed upload the
   partially-written prefix is cleaned up.
5. `version`: the schema CHECK only allows `1.2|2004|h5p`, Laravel accepted any string ≤ 50.
   Client values are normalised (`1.2*` → 1.2, contains `2004`/`CAM 1.3` → 2004, `h5p`), anything
   else is 422 `The selected version is invalid.`. Manifest titles longer than 255 are truncated.
6. `mimes:zip` is checked by the ZIP signature (`PK\x03\x04` / `PK\x05\x06`); Laravel used finfo.
   Entry order of the "first HTML file" fallback is sorted path order (Finder order was
   filesystem-dependent).
7. Virtual class `start_at` with an explicit offset is converted to UTC (Laravel stored the wall
   time and dropped the offset). Values without offset are UTC, as before (app.timezone UTC).
8. Virtual class create returns all columns (absent optional ones as `null`); Laravel's
   freshly created model omitted keys that were not sent.
9. Review update only writes when rating/comment changed (Eloquent dirty check); virtual-class /
   SCORM upserts always bump `updated_at` (DB trigger).
10. Paginators: `reviews` fixed 10/page, `qas` fixed 20/page (`?per_page` ignored, like
    `paginate(n)`); ties in `created_at` ordered by `id DESC`.
11. `?lesson_id=abc` on `GET /qas` returns no rows (MySQL compared it as 0); `?lesson_id=`
    filters `lesson_id IS NULL` like Eloquent `where(col, null)`.

## Kept from Laravel on purpose (flag for review)
- `GET virtual-class`, `GET scorm-package` and `POST scorm/track` have **no enrollment check**:
  any authenticated user can read meeting links/passwords (decision: fields not hidden) and
  create progress rows for any lesson.
- `POST qa-reactions` has no enrollment check.
- `PUT reviews/:review` does not check that the review belongs to `{course}`; only the author
  may edit. `DELETE` allows author or admin.
- `POST reviews` checks enrollment only (no admin/owner bypass).

## Side effects
None of these controllers award points, send notifications or e-mails, so nothing is deferred.
There is no cached rating aggregate: `reviews` has an index for AVG/COUNT and the catalog
computes `avg_rating` / `reviews_avg_rating` itself.

**Overlap with `study`**: `trackScorm` (scorm.go) upserts `lesson_progress` directly with its
own SQL (`completed_at` only). The lead should dedupe with study's progress upsert (and decide
whether SCORM completion should trigger whatever study does on completion, e.g. points —
legacy ScormController did not).

## Storage
`ObjectStore` = `media.ObjectStore` + `DeletePrefix`. `minioStore` adapts `*storage.Storage`
(ListObjects + RemoveObjects via `Client()`); a shared `Storage.DeletePrefix` would be cleaner.
Objects are written with a content type from the file extension.

## Tests
- Unit: zip-slip/limits/symlinks, manifest entry/version/title detection, URL resolution,
  version normalisation, reaction counts/morph rebuild, SCORM completion rule, validation
  messages (`scormpkg_test.go`, `logic_test.go`).
- Integration (`integration_test.go`, `TEST_DATABASE_URL`): reviews, Q&A + reactions,
  virtual class, SCORM upload/re-upload/H5P/delete/track. Passed against PostgreSQL 17
  (scratch DB `eript_p2_interaction`, dropped afterwards).

## Open questions
1. Should the read endpoints (virtual class, SCORM package) and SCORM track require
   enrollment/ownership like the lesson content endpoints?
2. Is 422 for broken SCORM zips acceptable for the frontend (was 500)?
3. Should a type switch SCORM → H5P delete the old `scorm/<uuid>/` objects? (Laravel kept them.)
