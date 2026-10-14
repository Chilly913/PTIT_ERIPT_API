# 03 — Learning domain (`000003_learning`)

Files: `migrations/000003_learning.{up,down}.sql`, `migrations/000090_deferred_fks_learning.{up,down}.sql`.
References lower files: `users` (000002). Deferred FKs: see §2.6.

## 1. Tables

| Table | Purpose |
|---|---|
| `categories` | Course category tree (self FK, API walks up to 3 levels). |
| `courses` | Course catalog entry (marketplace "extension" or academic "core"). |
| `skills` | Skill dictionary used by the recommendation engine. |
| `course_skills` | Course ↔ skill with weight (pure join, composite PK). |
| `course_learning_outcomes` | CLOs per course (mapped to PLOs by academic `clo_plo_map`). |
| `sections` | Ordered chapters of a course. |
| `lessons` | Ordered lesson; common columns + video columns; `type` discriminator. |
| `lesson_attachments` | Downloadable files of a lesson. |
| `scorm_packages` | 1:1 lesson: SCORM 1.2/2004 package or H5P embed. |
| `virtual_classes` | 1:1 lesson: online meeting (Zoom/Meet/Jitsi). |
| `lesson_assignments` | 1:1 lesson: assignment settings. |
| `assignment_submissions` | One (latest) submission per learner per assignment. |
| `enrollments` | Learner ↔ course enrollment, with academic/commerce provenance. |
| `lesson_progress` | Per learner per lesson progress (write-hot). |
| `scorm_runtime_states` | **New.** SCORM runtime data per learner per lesson (was `lesson_progress.metadata`). |
| `lesson_notes` | Timestamped personal notes on a lesson. |
| `reviews` | One rating/comment per learner per course. |
| `course_qas` | Q&A threads on a course (optionally a lesson). |
| `course_qa_replies` | Replies to a Q&A thread. |
| `qa_reactions` | Like/dislike on a thread or reply (de-polymorphised). |

Helper: `learning_search_fold(text)` — IMMUTABLE `lower(translate(normalize(x,NFC), <Vietnamese diacritics>, <base letters>))`.

## 2. Changes vs legacy

### 2.1 General
- All `timestamp NULL` → `timestamptz NOT NULL DEFAULT now()`; `datetime` → `timestamptz`; tinyint(1) → boolean; varchar → text + CHECK only where the validator imposes a limit.
- `set_updated_at()` trigger on every table.
- Media columns store object keys (`*_path`). `PublicMediaUrl::toPublic` / `MediaService::getUrl` build the URL at read time; absolute `http(s)` values are tolerated for external media (YouTube, Unsplash, H5P), exactly as the legacy reader already handles them.
- `learning_search_fold`: legacy search used `LIKE ... COLLATE utf8mb4_0900_ai_ci` (accent+case insensitive: "toan" matches "Toán"). PG equivalent without the `unaccent` extension: query `learning_search_fold(col) LIKE '%'||learning_search_fold($1)||'%'`; GIN trigram expression indexes on `courses.title` and `courses.description` (search ORs both, so both need an index for a bitmap-OR plan). Verified the planner uses `ix_courses_title_trgm`.

### 2.2 courses (27 → 25 columns)
- `user_id` → `instructor_id` (it is the `instructor()` relation). FK changed CASCADE → **RESTRICT**: deleting a user must not silently delete courses with enrollments/orders.
- **Dropped `program_type_id`**: only in `$fillable` and an unused `programType()` relation; never filtered, validated or returned by a controller; no consumer hit; derivable via `programs.program_type_id` (3 seed rows non-null).
- **Dropped `curriculum_id`**: only in `$fillable`; `Course::curriculum()` is never loaded; all curriculum logic goes through `curriculum_courses` (M:N, authoritative). Data migration must make sure each non-null `(curriculum_id, course_id)` pair exists in `curriculum_courses` (114 seed rows).
- Kept `major_id` (0 rows non-null but read by `RecommendationService` and selected in `EnrollmentManagementController`) and `program_id` (admin list, recommendation).
- `status` CHECK `draft|pending_review|published|rejected|closed` (CourseController validator). `course_mode` CHECK `core|extension`. `level` CHECK `beginner|intermediate|advanced` (nullable).
- `price` decimal(12,0) → numeric(14,2) CHECK ≥ 0. `credit_value` smallint CHECK > 0.
- `learning_outcomes`, `benefits`, `requirements` json NULL → jsonb NOT NULL DEFAULT `[]` CHECK array (API returns arrays; null → `[]`).
- `thumbnail` → `thumbnail_path`.
- Not split: the academic columns (`course_mode`, `is_credit_bearing`, `credit_value`, `program_id`, `major_id`) are filtered/selected together with catalog columns in most queries (`course:id,title,course_mode,credit_value,...`); a 1:1 split would add joins everywhere for no gain at ~200 rows.
- Indexes: `(instructor_id, created_at DESC)` instructor "my courses"; `(status, is_featured DESC, created_at DESC)` public catalog default sort; `(course_mode, status)`; FK indexes (partial where sparse); trigram indexes above.

### 2.3 lessons model
Decision: **`type` discriminator + video columns on `lessons` + 1:1 child tables** (kept, not jsonb):
- Code reads type data through `hasOne` relations (`virtualClass`, `scormPackage`, `assignment`, `offlineSession`, `quiz`) that each have their own CRUD endpoints (`VirtualClassController`, `ScormPackageController`, `AssignmentController`, `LessonOfflineSessionController`) and are serialised as nested objects; `SectionController` eager-loads them for the curriculum tree.
- Children are not mutually exclusive in practice: seed data attaches SCORM packages to `video` lessons (414 rows), attachments exist on all types; a single jsonb `content` would mix independent lifecycles and lose FKs/constraints (`uuid` unique, provider CHECK, submissions FK to assignment).
- Video stays on `lessons`: it is the dominant type (828/1861) plus `live` recordings, the API returns `video_url/video_status/video_size` flat on the lesson, and the stream endpoint reads only the lesson row.
- Changes: `order` → `position`; `duration` → `duration_seconds`; `video_url` → `video_path`; `video_size` varchar ("194 MB") → `video_size_bytes` bigint (Go formats it); `video_status` MySQL enum → text CHECK.
- `type` CHECK = LessonController validator list (`video,audio,file,page,document,quiz,assignment,forum,survey,scorm,h5p,virtual_class,zoom,meet,live,offline`).
- **Composite FK** `(section_id, course_id) → sections(id, course_id)` guarantees a lesson's section belongs to the same course (legacy could not). `section_id` stays nullable (validator allows it).
- 1:1 children now have `UNIQUE (lesson_id)` (legacy only a plain index; code uses `updateOrCreate(['lesson_id'=>..])`).
- `scorm_packages`: `uuid` varchar → uuid; `entry_url` → `entry_path`; `version` CHECK `1.2|2004|h5p`.
- `virtual_classes`: `duration` → `duration_minutes` CHECK > 0; `provider` CHECK `zoom|google_meet|jitsi|other`; index on `start_at` for upcoming-session lists.
- `lesson_assignments`: `max_file_size` → `max_file_size_kb`; `allowed_extensions` CSV varchar → `text[]` (Go joins with `,` for the API); ordering CHECKs mirror validator (`submission_open_at ≥ available_from`, `due_at ≥ submission_open_at`).
- `lesson_attachments`: `file_size` varchar ("1.8 MB") → `size_bytes` bigint.
- `assignment_submissions`: `file_url` (full URL) → `file_path` (object key); new `UNIQUE (lesson_assignment_id, user_id)` (code upserts on that pair; seed data has no duplicates).

### 2.4 Progress (write-hot)
- `lesson_progress`: **composite PK `(user_id, lesson_id)`**, no surrogate `id` (deviation from the PK convention: saves one index on every insert; no consumer reads `progress.id`). Extra index `(lesson_id)` for FK/course stats.
- **Dropped `completed`** boolean → derived as `completed_at IS NOT NULL`. Legacy had two writers that disagreed (UserManagement `updateProgress` set `completed` without `completed_at`); data migration: `completed_at = COALESCE(completed_at, updated_at) WHERE completed`.
- **`metadata` moved to `scorm_runtime_states`** (typed columns: version, lesson/completion/success status with the ScormController validator values, score, lesson_location, suspend_data). Only `ScormController@track` wrote it, nothing reads it back. Seed metadata `{"persona","seeded_by"}` is dropped.
- `progress_percent` tinyint → smallint CHECK 0..100. `fillfactor = 85`, and `updated_at` intentionally **not indexed** so heartbeat upserts stay HOT. "Active in last 14 days" / "last activity" admin queries scan (7k rows) — revisit with a BRIN on `updated_at` if it grows large.

### 2.5 Enrollments
- `UNIQUE (user_id, course_id)` serves "my courses" and the enrollment check done in ~15 controllers.
- `(course_id, enrolled_at DESC)` for instructor/admin learner lists and `enrollments_count`.
- **`class_section_id` kept**, with partial index `ix_enrollments_class_section_id` (gradebook-by-class-section joins through it; academic dropped `class_sections.enrolled_count`, Go computes it from this index).
- `(term_id, cohort_id)` partial, `(cohort_id)` partial, `(order_id)` partial, `(enrollment_source, enrolled_at DESC)`.
- `enrollment_source` CHECK `marketplace|academic|manual|excel_import` (all values written in code).
- Commerce link is only `order_id` (career-path purchases also set it via `CareerPathFulfillmentService`); course/career-path detail lives in commerce `order_items`.

### 2.6 Deferred FKs (`000090_deferred_fks_learning`)
| Column | Target | ON DELETE |
|---|---|---|
| `courses.program_id` | `programs` | SET NULL |
| `courses.major_id` | `majors` | SET NULL |
| `courses.certificate_template_id` | `certificate_templates` | SET NULL |
| `enrollments.term_id` | `terms` | SET NULL |
| `enrollments.cohort_id` | `cohorts` | SET NULL |
| `enrollments.class_section_id` | `class_sections` | SET NULL |
| `enrollments.order_id` | `orders` | SET NULL (legacy had no FK) |

### 2.7 Community
- `reviews.rating` CHECK 1..5; index `(course_id, created_at DESC) INCLUDE (rating)` (list + AVG/COUNT).
- `qa_reactions`: polymorphic `reactable_type/reactable_id` → `course_qa_id` / `course_qa_reply_id`, CHECK `num_nonnulls(...) = 1`, partial unique `(target, user_id)` per kind of target (also serves per-item counts). API body (`reactable_type: qa|reply`) maps directly.
- `course_qas` / `course_qa_replies`: listing indexes `(course_id, created_at DESC)`, `(course_qa_id, created_at)`.
- `skills.code`, `course_learning_outcomes.code` → citext.
- `course_skills`: surrogate `id` dropped, PK `(course_id, skill_id)`.

### 2.8 user_assignments
- Legacy `user_assignments` was listed in this domain but is **owned by `000002_identity`** (org/RBAC unit scoping); not created here. Its mapping is recorded in the identity notes.

## 3. Old → new column mapping

Columns not listed map 1:1 with the same name (type changes per §2.1).

| Legacy table.column | New table.column | Transform |
|---|---|---|
| categories.* | categories.* | — |
| courses.user_id | courses.instructor_id | — |
| courses.program_type_id | — | dropped (via programs.program_type_id) |
| courses.curriculum_id | — | dropped; ensure pair in curriculum_courses |
| courses.thumbnail | courses.thumbnail_path | strip `/storage/` or own-host prefix → key (`PublicMediaUrl::toStorageKey`), keep external URLs |
| courses.price | courses.price | decimal(12,0) → numeric(14,2) |
| courses.learning_outcomes/benefits/requirements | same | NULL → `[]` |
| sections.* | sections.* | — |
| lessons.order | lessons.position | — |
| lessons.duration | lessons.duration_seconds | — |
| lessons.video_url | lessons.video_path | URL → key when internal |
| lessons.video_size | lessons.video_size_bytes | parse "N MB"/"N.NNMB" × 1048576 |
| lesson_attachments.file_size | lesson_attachments.size_bytes | parse "N MB" |
| scorm_packages.uuid | scorm_packages.uuid | cast to uuid |
| scorm_packages.entry_url | scorm_packages.entry_path | — |
| virtual_classes.duration | virtual_classes.duration_minutes | — |
| virtual_classes.start_at | virtual_classes.start_at | datetime as Asia/Ho_Chi_Minh → timestamptz |
| lesson_assignments.max_file_size | lesson_assignments.max_file_size_kb | — |
| lesson_assignments.allowed_extensions | lesson_assignments.allowed_extensions | `string_to_array(lower(x), ',')` trimmed |
| lesson_assignments.available_from/submission_open_at/due_at | same | datetime → timestamptz (local tz) |
| assignment_submissions.file_url | assignment_submissions.file_path | URL → key |
| enrollments.* | enrollments.* | — |
| lesson_progress.id | — | dropped (PK is (user_id, lesson_id)) |
| lesson_progress.completed | lesson_progress.completed_at | `completed_at = COALESCE(completed_at, updated_at)` when completed=1, else NULL |
| lesson_progress.metadata.scorm_* / last_location / suspend_data | scorm_runtime_states.scorm_version, lesson_status, completion_status, success_status, score, lesson_location, suspend_data | only rows with `metadata ? 'scorm_version'` |
| lesson_progress.metadata (other keys) | — | dropped (seed markers) |
| lesson_notes.* / reviews.* / course_qas.* / course_qa_replies.* | same | — |
| qa_reactions.reactable_type='App\Models\CourseQa', reactable_id | qa_reactions.course_qa_id | — |
| qa_reactions.reactable_type='App\Models\CourseQaReply', reactable_id | qa_reactions.course_qa_reply_id | — |
| course_skills.id | — | dropped (composite PK) |
| skills.*, course_learning_outcomes.* | same | — |
| user_assignments.* | — | owned by identity (000002), see 02_identity.md |

## 4. Open questions
1. (Resolved) `user_assignments` is owned by `000002_identity`.
2. (Resolved) `offline_sessions` / `offline_session_attendances` are owned by `000004_academic` (1:1 child of `type='offline'` lessons).
3. (Decided: keep for now) `learning_search_fold` could later be replaced by a shared `immutable_unaccent()` in `000001_base`.
4. (Accepted) Composite PKs on `lesson_progress`, `course_skills`, `scorm_runtime_states`.
5. `virtual_classes.meeting_password` / `start_url` (host link) are stored in plaintext and returned to learners by `LessonController@show`; should the Go port hide them from non-owners?
6. `is_credit_bearing` matches `course_mode = 'core'` in 100% of data; could be derived, kept for now because several services read it explicitly.
