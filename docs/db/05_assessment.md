# 05 — Assessment (`000005_assessment`)

Sources read: `QuizController`, `QuestionBankController`, `ExamController`,
`ExamProctorController`, `UserManagement/GradebookController`,
`StudentDashboardController`, `AIChatController` (quiz averages), `AdminController`,
models `Quiz*`, `Question*`, `Answer`, `Exam*`, `Grade*`, migration
`2026_04_08_163027_restructure_lms_quizzes_and_questions`. Data profiled on the dev
MySQL (row counts below).

References only lower-numbered files: `users` (000002), `courses`, `lessons`,
`enrollments` (000003). **No deferred FKs** (no `000090_deferred_fks_assessment`).

## 1. Tables

| Table | Rows (dev) | Purpose |
|---|---|---|
| `question_banks` | 416 | Named question pool of a course. |
| `question_groups` | 832 | "Set" of questions inside a bank, or a course-level set when `question_bank_id IS NULL` (`ungrouped_sets` in the API). Used for UI grouping and as a filter in quiz random rules (`settings.random_rules[].group_id`). |
| `questions` | 7,696 | Question stem, type, difficulty (1-5), default score, feedback. |
| `question_answers` | 24,964 | Options / reference answers of a question (legacy `answers`). |
| `question_attachments` | 0 | Files (image/audio/file) attached to a question. |
| `exams` | 240 | Course-final or standalone exam: window, attempts, shuffle, review options, proctoring flag. |
| `exam_enrollments` | 2,423 | Candidate list for standalone exams (course exams use course enrollments). |
| `quizzes` | 862 | Question paper: lesson quiz, course quiz, or the (single) paper of an exam. |
| `quiz_questions` | 11,184 | Fixed questions of a quiz with position and points (legacy `quiz_question`). |
| `quiz_attempts` | 2,396 | One attempt: drawn question list, answer snapshot, score, proctoring state. |
| `exam_violations` | 196 | Proctoring events reported by the client / face check. |
| `grade_components` | 243 | Grade structure (weights) per course, shared by all its class sections. |
| `grade_entries` | 10,731 | Score of one enrollment for one component. |

## 2. Changes vs legacy

### Renames
- `answers` -> `question_answers` (avoid confusion with a student's answers). JSON relation name in the API stays `answers`.
- `quiz_question` -> `quiz_questions` (plural join table); `order` -> `position`.
- `answers.order` -> `position`; `answers.sort_order` -> `correct_position` (it is the *expected* position for `ordering` questions, not a display order).
- `question_groups.sort_order` -> `position`.
- `quiz_attempts.answers_json` -> `answers`.
- `question_attachments.file_size` (formatted string "1.2 MB") -> `file_size_bytes bigint`; the API string is formatted in Go.
- `exam_violations.snapshot_url` (absolute URL) -> `snapshot_path` (object key, per conventions); API resolves to URL and keeps the key `snapshot_url` (used by `frontend/app/pages/admin/reports/exam-tracking.vue`).

### Dropped
| Column / table | Evidence |
|---|---|
| `quiz_attempts.answers_data` | Left over from the 2026_04_08 restructure; superseded by `answers_json`. No reads/writes in `backend/app`, 0 non-null rows, no frontend use. |
| `exams.proctoring_settings` | Only in `$fillable`/`$casts`; never validated/written by any controller, never read; 0 non-null rows; no frontend/mobile use. Re-add as jsonb if proctoring configuration is built. |
| `quiz_question.id` | Pivot surrogate key never exposed/used (Laravel `sync` works on `(quiz_id, question_id)`); replaced by composite PK. |

### Kept but worth noting
- `quiz_attempts.user_agent`, `ip_address` (now `inet`): written on attempt creation, `ip_address` returned by live monitor.
- `quizzes.scope = 'course'` (208 rows) is only created by seeders, but rows exist, so it stays in the CHECK.
- `question_answers.correct_position`: in dev data `sort_order == order` on every row (seeders copy it); no `ordering` questions exist and `isAnswerCorrect()` has no `ordering` branch yet.

### Key design decisions
1. **Attempt answers stay a jsonb snapshot** (`quiz_attempts.answers`, `{"<question_id>": answer}`), not a normalized `attempt_answers` table, because every access is per attempt and whole-document:
   - auto-save (`ExamProctorController::autoSave`) overwrites the full map every few seconds — normalized it would be a delete+insert of ~15 rows per tick;
   - grading (`gradeAndSubmit`) loads the attempt's questions + answers and compares in application code (answer shapes differ per type: id, id[], text, number, `{left_id: right}` map) — one polymorphic value per question fits jsonb, not typed columns;
   - review (`examResults`) returns the map as `student_answers` / `saved_answers`;
   - no report reads answers across attempts (stats only use `score`/`passed`).
   A CHECK keeps it a JSON object. If item analysis (per-question difficulty / discrimination) is ever needed, add a derived `attempt_question_results` table filled at submit.
2. **`question_ids bigint[]`** (ordered, NOT NULL DEFAULT `'{}'`) instead of JSON: it is a typed, ordered list read with `WHERE id = ANY(...)` and `array_position` for ordering. Legacy NULL (729 seeded rows) -> `'{}'`.
3. **Attempt status** `text` + CHECK `in_progress | paused | submitted | force_stopped` (exact set written in code; `time_expired` / `not_started` are response-only values). State invariants as CHECKs:
   - `completed_at IS NOT NULL` iff status in (`submitted`,`force_stopped`);
   - `paused_at IS NOT NULL` iff status = `paused` (resume/force-stop clear it);
   - `force_stop_reason` only when `force_stopped`;
   - `score` 0..100 (percentage), `paused_duration`/`time_extensions` (seconds) >= 0.
4. **At most one open attempt per (user, quiz)**: partial unique index `WHERE status IN ('in_progress','paused')`. The code does find-or-create without a lock; the index turns the race into a unique violation (retry = re-read). Dev data has no duplicates.
5. **One quiz per lesson and per exam**: partial unique indexes on `quizzes.lesson_id`, `quizzes.exam_id` (`Quiz::where(lesson_id)->first()`, `Exam hasOne Quiz`). Because exam -> quiz is 1:1, attempts are not denormalized with `exam_id`; proctor listing goes exam -> quiz id -> `ix_quiz_attempts_quiz_id_status`.
6. **`ck_quizzes_scope_owner`**: `lesson` requires `lesson_id`; `exam` requires `exam_id`; `course` requires `course_id` and no lesson/exam. Therefore `quizzes.exam_id` / `course_id` FKs are `ON DELETE CASCADE` (legacy `SET NULL` would leave orphaned exam quizzes and now violate the CHECK).
7. **Exam rules**: `type` in (`course_final`,`standalone`), `course_final` requires `course_id` (standalone may still carry a course: 36 seeded rows do); status CHECK with the 5 validated values; `pass_score` 0..100, `max_attempts` 1..99, `variant_count` 1..26 (validation rules); `ends_at >= starts_at`. Trigram index on `title` for the admin search.
8. **Correct-answer model** stays row-based (`question_answers.is_correct` + `sub_content` + `correct_position`, see comment in the migration for each type). "Exactly one correct option" for single_choice/true_false is enforced by the Go service, not the DB (persistQuestion deletes and recreates all options in one transaction). `uq_question_answers_question_id_position` gives deterministic option order.
9. **Question codes** are `citext` unique (auto `Q-C<course>-XXXXXX`, max 50 per validation); dev data has no case-insensitive duplicates.
10. **Violations**: composite FK `(attempt_id, user_id) -> quiz_attempts(id, user_id)` guarantees the denormalized `user_id` matches the attempt owner (0 mismatches in data). Type/severity CHECKs from the controller's `in:` rules.
11. **Gradebook**: unique `(enrollment_id, grade_component_id)` (upsert key). `score numeric(5,2)` with CHECK `>= 0` (validation `min:0`) plus trigger `grade_entries_check_score()` enforcing `score <= grade_components.max_score` (cross-table; 0 violations in data). `weight` 0..100, `max_score > 0`, `name` <= 150 chars (validation). Components remain a per-course template (the section gradebook reads `course_id = class_section.course_id`), so no `class_section_id` here.
12. **Numeric types**: `pass_score` is an integer percentage in validation -> `smallint` 0..100; `default_score` `numeric(6,2)`; `quiz_questions.points` int -> `numeric(6,2)`.

### Hot-path indexes
| Path | Query | Index |
|---|---|---|
| Attempts of user for quiz | `user_id = ? AND quiz_id = ? [AND status IN ...]` (start, max-attempt count, best score, dashboard) | `ix_quiz_attempts_user_id_quiz_id (user_id, quiz_id, status)` + partial `uq_quiz_attempts_user_id_quiz_id_open` |
| AI context | `user_id = ? AND status IN (...) ORDER BY completed_at` | leading `user_id` of the index above |
| Proctor live monitor / exam results | `quiz_id = ? ORDER BY status` / `ORDER BY id DESC` | `ix_quiz_attempts_quiz_id_status` |
| Recent violations + count | `attempt_id = ? ORDER BY created_at DESC LIMIT 5` | `ix_exam_violations_attempt_id_created_at` |
| Gradebook by class section | components `course_id = ? ORDER BY position`; entries `enrollment_id IN (enrollments of section)` | `ix_grade_components_course_id_position`, `uq_grade_entries_enrollment_id_grade_component_id`. **Requires `000003` to index `enrollments(class_section_id)`.** |
| Random question rules / bank stats | `question_bank_id = ? [AND difficulty = ?]` | `ix_questions_question_bank_id_difficulty` |
| Bank tree | groups by bank/course ordered | `ix_question_groups_*_position` |

## 3. Old -> new column mapping

Columns not listed map 1:1 with the same name. `created_at`/`updated_at` NULL -> `now()`. MySQL `tinyint(1)` -> `boolean`.

| Old table.column | New table.column | Transform |
|---|---|---|
| question_banks.* | question_banks.* | — |
| question_groups.sort_order | question_groups.position | — |
| questions.code | questions.code (citext) | NULL -> generate `Q-C<course_id>-<6 hex>` |
| questions.difficulty int | questions.difficulty smallint | — |
| questions.default_score decimal(8,2) | questions.default_score numeric(6,2) | — |
| questions.metadata json | questions.metadata jsonb | — |
| answers (table) | question_answers | — |
| answers.order | question_answers.position | — |
| answers.sort_order | question_answers.correct_position | copy only when `questions.type = 'ordering'`, else NULL |
| answers.content / sub_content / is_correct | same | — |
| question_attachments.file_size varchar | question_attachments.file_size_bytes bigint | parse "N KB/MB" -> bytes (table empty in dev) |
| question_attachments.type / file_path / mime_type / original_name | same | `file_path` stays object key |
| quizzes.* | quizzes.* | `time_limit`, `pass_score` (smallint), `settings` jsonb |
| quiz_question (table) | quiz_questions | drop `id` |
| quiz_question.order | quiz_questions.position | — |
| quiz_question.points int | quiz_questions.points numeric(6,2) | — |
| quiz_attempts.question_ids json | quiz_attempts.question_ids bigint[] | JSON array -> array; NULL -> `'{}'` |
| quiz_attempts.answers_json json | quiz_attempts.answers jsonb | — |
| quiz_attempts.answers_data | — | dropped (unused) |
| quiz_attempts.started_at | quiz_attempts.started_at NOT NULL | NULL -> `created_at` |
| quiz_attempts.ip_address varchar(45) | quiz_attempts.ip_address inet | invalid -> NULL |
| quiz_attempts.status, score, passed, completed_at, paused_at, resumed_at, paused_duration, time_extensions, auto_saved_at, force_stop_reason, user_agent | same | — |
| exams.duration, pass_score, max_attempts, variant_count | same (int/smallint) | — |
| exams.review_options json | exams.review_options jsonb | — |
| exams.proctoring_settings | — | dropped (never written/read) |
| exams.room varchar(64) | exams.room text | — |
| exam_enrollments.* | exam_enrollments.* | — |
| exam_violations.snapshot_url | exam_violations.snapshot_path | strip scheme/host and `/storage/` prefix -> object key |
| exam_violations.* (rest) | same | — |
| grade_components.max_score decimal(6,2) | grade_components.max_score numeric(5,2) | — |
| grade_components.position smallint unsigned | grade_components.position integer | — |
| grade_entries.score decimal(6,2) | grade_entries.score numeric(5,2) | — |

API notes for the Go port: return `question_answers` as relation `answers` with `order` = `position` and `sort_order` = `correct_position ?? position`; pivot `order` = `quiz_questions.position`; attempt `answers_json` / `saved_answers` / `student_answers` = `answers`; violation `snapshot_url` = resolved URL of `snapshot_path`; attachment `file_size` = formatted `file_size_bytes`; exam `proctoring_settings` = `null` if a client still expects the key.

## 4. Open questions

1. **Exam deletion**: deleting an exam cascades to its quiz, attempts and violations (legacy set `quizzes.exam_id` NULL and left orphaned attempts). Should exams with completed attempts be blocked (`RESTRICT` / soft-archive via `status='archived'`) instead?
2. **Grading weight**: grading uses `questions.default_score`; `quiz_questions.points` (default 10) is stored and returned but ignored. Should the Go port grade with `points`?
3. **Sum of component weights = 100** per course is not enforced (upsert replaces the whole set; a deferred constraint trigger is possible if wanted).
4. **`grade_components.max_score > 0`** is stricter than the legacy validation (`min:0`). OK?
5. **Group/bank consistency**: a question's group should belong to the same bank (0 mismatches). Could be enforced with a composite FK `(question_group_id, question_bank_id)`; left out because `updateGroup` can move a group to another bank. Enforce?
6. Should `ordering` grading be implemented (uses `correct_position`)? Currently no such questions exist.
