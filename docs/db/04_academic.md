# 04 — Academic domain (`000004_academic`)

Sources reviewed: `AcademicManagementController`, `AdminClassSectionController`,
`CurriculumBuilderController`, `EnrollmentManagementController`, `GradebookController`,
`OfflineSessionController`, `LessonOfflineSessionController`, `StudentDashboardController`
(schedule + check-in), `CurriculumEvaluationService`, `app/Concerns/ScopesByUnit.php`,
the Nuxt admin pages under `frontend/app/pages/admin/lnd/*`, and the dev MySQL data.

No deferred FK file is needed: nothing in this domain points to a later-numbered file.
`000090_deferred_fks_academic.*` is therefore **not** created.

## 1. Tables

| Table | Purpose |
|---|---|
| `academic_years` | School year per institution (e.g. `2025-2026`), one may be current. |
| `terms` | Semesters inside an academic year (HK1/HK2…), enrollment and exam windows. |
| `program_types` | Training mode catalog (Chính quy, Từ xa, VHVL…). |
| `programs` | Training program of an institution, owned by a unit (faculty). |
| `majors` | Major inside a program, optionally owned by a unit. |
| `specializations` | Specialization inside a major. |
| `cohorts` | Intake group (`D23CNTT`) of a program/major with start/end year. |
| `curricula` | Training framework (CTĐT) of a program/major/specialization, with validity range. |
| `curriculum_courses` | Course slots in a curriculum: term number 1..12, required/elective, credits, order. |
| `program_learning_outcomes` | PLOs of a program. |
| `clo_plo_mappings` | Weighted CLO→PLO map (legacy `clo_plo_map`). |
| `administrative_classes` | Homeroom class (lớp hành chính): program, cohort, curriculum, advisor, unit. |
| `administrative_class_terms` | Per class: curriculum term number → real `terms` row. |
| `class_sections` | Credit class (lớp tín chỉ) of a course in a term with a lecturer. |
| `administrative_class_sections` | Which class sections an administrative class attends (legacy `administrative_class_class_section`). |
| `class_schedules` | Weekly timetable slots of an administrative class. |
| `offline_sessions` | In-person session with QR/GPS check-in (lesson, class-section or instructor mode). |
| `offline_session_attendances` | One check-in record per (session, user). |

Hierarchy enforced by the schema:

```
institutions ─┬─ academic_years ── terms
              └─ programs (program_types, units)
                   ├─ majors ── specializations
                   ├─ cohorts            (major ∈ program, institution = program.institution)
                   ├─ curricula ── curriculum_courses
                   ├─ program_learning_outcomes ── clo_plo_mappings ── course_learning_outcomes
                   └─ administrative_classes (major, cohort, curriculum ∈ same program)
                         ├─ administrative_class_terms ── terms
                         ├─ administrative_class_sections ── class_sections (course, term, lecturer)
                         ├─ class_schedules
                         └─ offline_sessions ── offline_session_attendances
```

## 2. Changes vs legacy

### Renames
- `clo_plo_map` → `clo_plo_mappings` (plural snake_case rule).
- `administrative_class_class_section` → `administrative_class_sections` (join-table naming).
- `offline_sessions.duration` → `duration_minutes` (unit was implicit; API key `duration` must still be emitted by Go).

### Dropped columns / surrogate keys (with evidence)
| Column | Reason |
|---|---|
| `class_sections.administrative_class_id` | Duplicates the M:N pivot `administrative_class_sections`. Never written by any endpoint (`storeClassSection`/`updateClassSection` validation does not accept it); only seeders set it, and in dev data it equals the pivot row in 584/584 cases. No frontend reads `section.administrative_class_id`. Read the pivot instead. |
| `class_sections.enrolled_count` | Denormalised counter that no code path maintains (only seeded; values 10–11 vs the real enrollment counts). Still shown by the UI (`admin/index.vue`, `course-sections.vue`) → Go must compute `COUNT(*) FROM enrollments WHERE class_section_id = ?` and keep emitting the key `enrolled_count`. Needs `enrollments(class_section_id)` index (owned by 000003). |
| `clo_plo_map.id` | Pure pivot (`belongsToMany` … `withPivot('weight')`); PK is now `(clo_id, plo_id)`. |
| `administrative_class_terms.id` | Natural key `(administrative_class_id, term_number)` (was already UNIQUE); all access is by that pair (`updateOrCreate`, `pluck('term_id','term_number')`). |
| `administrative_class_class_section.id`, `.created_at`, `.updated_at` | Pivot; PK is now `(administrative_class_id, class_section_id)`. `assigned_at` carries the same information (set on every attach); no consumer reads `pivot.*` (grep of frontend/mobile: 0 hits). |

### Type / nullability changes
- All `code` columns → `citext` (MySQL `utf8mb4_unicode_ci` uniqueness was case-insensitive; this preserves it).
- `tinyint(1)` → `boolean`; `datetime`/`timestamp` → `timestamptz`; `varchar(n)` → `text`.
- `term_number`, `credits`, `position`, `weekday`, years → `smallint` with CHECKs.
- `offline_sessions.duration_minutes` is `NOT NULL` (every create endpoint requires it; 0 NULLs in data).
- `administrative_class_sections.assigned_at` is `NOT NULL DEFAULT now()` (always set by `attach`; 0 NULLs).

### New constraints
- Enum CHECKs (values taken from validators + Nuxt option lists + data):
  - `academic_years.status`: planned, active, inactive, completed
  - `terms.status`: planned, upcoming, ongoing, active, completed
  - `cohorts.status`: active, inactive, graduated
  - `administrative_classes.status`: active, graduated, suspended
  - `class_sections.status`: planned, open, closed, cancelled
  - `program_learning_outcomes.level`: knowledge, skill, attitude
  - `offline_sessions.qr_mode`: manual, rotating, static
  - `offline_session_attendances.status`: present, late, absent, excused (`absent` is what reports show for missing rows; `excused` reserved for manual marking)
- Date/time sanity: `start_date < end_date` (years, terms), enrollment/exam windows ordered, curriculum `effective_from <= effective_to`, cohort `end_year >= start_year`, `class_schedules.start_time < end_time`, `weekday BETWEEN 1 AND 7` (ISO, 1 = Monday — matches the Nuxt weekday picker and PG `isodow`).
- Range CHECKs mirroring validators: `term_number 1..12`, `check_in_radius_meters 5..500`, `qr_rotate_seconds 15..600`, `max_participants > 0`, `capacity >= 0`, lat/long ranges, lat/long both-or-neither.
- **At most one current** academic year per institution and one current term per academic year (partial unique indexes on `... WHERE is_current`). The legacy code already clears the others (`ensureAcademicCalendarForCohort`); the Go port must do the same in one transaction for the generic CRUD update.
- **Hierarchy consistency via composite FKs** (dev data has 0 violations):
  - `cohorts (program_id, institution_id) → programs (id, institution_id)` and same for `administrative_classes` (ON UPDATE CASCADE so moving a program to another institution carries its children). Replaces the separate FK to `institutions`.
  - `(major_id, program_id) → majors (id, program_id)` on cohorts, curricula, administrative_classes.
  - `curricula (specialization_id, major_id) → specializations (id, major_id)` + CHECK specialization requires major.
  - `administrative_classes (cohort_id, program_id) → cohorts`, `(curriculum_id, program_id) → curricula`.
  - These use PG15+ `ON DELETE SET NULL (col)` so deleting a major/cohort/curriculum only nulls that column.
- `class_sections UNIQUE NULLS NOT DISTINCT (course_id, term_id, code)` (MySQL let duplicates through when `term_id` was NULL).
- `class_schedules UNIQUE NULLS NOT DISTINCT (administrative_class_id, term_id, weekday, start_time)`: no two slots for one class starting at the same moment (dev data: 666 rows, 0 duplicates, 0 overlaps).
- `offline_sessions`:
  - CHECK at least one owner (`lesson_id`, `class_section_id` or `course_id`).
  - Partial unique `(lesson_id) WHERE class_section_id IS NULL AND course_id IS NULL` = one standalone lesson session (`LessonOfflineSessionController` uses `updateOrCreate(['lesson_id'])`). Section sessions may share a lesson (dev data: 236 section rows over 164 lesson/section pairs).
- `offline_session_attendances UNIQUE (offline_session_id, user_id)` (column order flipped vs legacy so the per-session report uses it; separate index on `user_id` for the student history).

### ON DELETE changes (deliberate)
| FK | Legacy | New | Why |
|---|---|---|---|
| `academic_years/programs.institution_id` | CASCADE | RESTRICT | Master data; deleting an institution must not silently wipe programs, classes, sections, grades. |
| `programs.program_type_id` | CASCADE | RESTRICT | Deleting a catalog entry wiped every program. |
| `class_sections.term_id`, `class_schedules.term_id` | SET NULL | RESTRICT | NULL term on a schedule means "every term" (`StudentDashboardController` uses `orWhereNull('term_id')`), so SET NULL silently changed meaning; for sections it would also collide with the NULLS NOT DISTINCT unique. |
| `offline_sessions.class_section_id`, `.course_id` | SET NULL | CASCADE | Owner columns; with the owner CHECK, SET NULL could leave an ownerless row. Attendance is lost with the session (as for lesson-mode, which was already CASCADE). |
| Others | — | unchanged | |

### Indexes
- **ScopesByUnit**: `programs(unit_id)`, `majors(unit_id)`, `administrative_classes(unit_id, status)`. `scopeCoursesByUnits` / `scopeEnrollmentsByUnits` join `courses.program_id/major_id → programs/majors.unit_id`; `applyScope` filters `programs/majors/administrative-classes` by `unit_id` and `curricula/cohorts` via program/major. No other table in this domain needs its own `unit_id`.
- `terms(start_date, end_date)` for `Term::operational()`.
- `curriculum_courses(curriculum_id, term_number, position)` matches the builder ordering.
- `class_sections`: trigram GIN on `(code::text)` and `name` for `SearchQuery::like(['code','name'])`; Go must query `code::text ILIKE $1` to hit the index.
- `offline_sessions`: `(class_section_id, start_at)`, `(administrative_class_id, start_at)`, `(course_id, administrative_class_id)`, `(lesson_id)`.
- Every FK column is indexed or covered by a leading index column.

### Not modelled / considered
- Exclusion constraints against overlapping schedule slots / overlapping academic years would need `btree_gist`, which `000001_base` does not install. Data has 0 overlaps; the unique on start time covers the common duplicate case. See open questions.
- `class_schedules` keeps `(administrative_class_id, course_id, term_id, lecturer_id)` instead of pointing to `class_sections`, to keep the API contract unchanged.

## 3. Old → new column mapping

Columns not listed map 1:1 with the same name (type changes as described above: `tinyint(1)`→`boolean`, `timestamp/datetime`→`timestamptz`, `varchar`→`text`/`citext`).

| Legacy table.column | New table.column | Note |
|---|---|---|
| academic_years.* | academic_years.* | `status` CHECK |
| terms.* | terms.* | `code` citext |
| program_types.* | program_types.* | `code` citext |
| programs.* | programs.* | `code` citext |
| majors.*, specializations.* | same | `code` citext |
| cohorts.* | cohorts.* | `start_year/end_year` smallint |
| curricula.* | curricula.* | |
| curriculum_courses.* | curriculum_courses.* | smallint columns |
| program_learning_outcomes.* | program_learning_outcomes.* | |
| clo_plo_map.id | — | dropped (PK = clo_id, plo_id) |
| clo_plo_map.{clo_id, plo_id, weight, created_at, updated_at} | clo_plo_mappings.same | |
| administrative_classes.* | administrative_classes.* | `code` citext |
| administrative_class_terms.id | — | dropped (PK = administrative_class_id, term_number) |
| administrative_class_terms.{administrative_class_id, term_number, term_id, created_at, updated_at} | administrative_class_terms.same | |
| administrative_class_class_section.id | — | dropped |
| administrative_class_class_section.{administrative_class_id, class_section_id, term_number, assigned_by} | administrative_class_sections.same | |
| administrative_class_class_section.assigned_at | administrative_class_sections.assigned_at | `COALESCE(assigned_at, created_at, now())` |
| administrative_class_class_section.created_at / updated_at | — | dropped (see §2) |
| class_sections.administrative_class_id | — | dropped; if a row has it but no pivot row, insert `administrative_class_sections(administrative_class_id, class_section_id)` during migration |
| class_sections.enrolled_count | — | dropped; computed from `enrollments` |
| class_sections.(other) | class_sections.same | |
| class_schedules.* | class_schedules.* | `weekday` smallint |
| offline_sessions.duration | offline_sessions.duration_minutes | NOT NULL |
| offline_sessions.start_at, qr_expires_at (`datetime`, app TZ) | timestamptz | convert from app timezone (Asia/Ho_Chi_Minh) |
| offline_sessions.(other) | offline_sessions.same | |
| offline_session_attendances.checked_in_at (`datetime`) | timestamptz | convert from app timezone |
| offline_session_attendances.(other) | same | |

Inbound references owned by other domains (they must add them in their own `000090_deferred_fks_<domain>` since 000004 is later than them, or directly if later):
`users.{program_id, major_id, specialization_id, cohort_id, administrative_class_id}` (identity),
`courses.{program_type_id, program_id, major_id, curriculum_id}` and `enrollments.{term_id, cohort_id, class_section_id}` (learning),
`tuitions.term_id` (commerce). Legacy all used `ON DELETE SET NULL`. Note that `enrollments.class_section_id` needs an index for the computed `enrolled_count`.

## 4. Open questions

1. **Ownership of `offline_sessions` / `offline_session_attendances`.** `CONVENTIONS.md` lists "offline sessions" under `000003_learning`, but this task assigned them to `000004_academic`. They are placed here (they reference `class_sections` and `administrative_classes`, which live in 000004; in 000003 they would need deferred FKs). Learning must not create them too.
2. Should `btree_gist` be added to `000001_base`? It would allow `EXCLUDE` constraints for non-overlapping `class_schedules` slots (per class and per lecturer) and non-overlapping academic years per institution.
3. Is `RESTRICT` on term deletion (sections/schedules) and on institution/program-type deletion acceptable? Legacy cascaded/nullified silently; the Go API should return 409 instead.
4. Cohort code uniqueness is kept per program (`program_id, code`); should it be per institution like administrative classes?
5. `administrative_class_terms` allows two term numbers mapped to the same term (legacy code detects and repairs such "collisions"), so no UNIQUE `(administrative_class_id, term_id)` was added. Add it once the repair flow is gone?
6. Should `class_schedules` eventually reference `class_sections` instead of `(course_id, term_id, lecturer_id)` duplicates? Would be cleaner but changes the API.
7. `AdminClassSectionController::attach` validates `term_number` max 8, curriculum allows 12; the schema allows 1..12 everywhere.
