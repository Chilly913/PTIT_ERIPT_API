# 02 — Identity (`000002_identity`, `000090_deferred_fks_identity`)

## 1. Tables

| Table | Purpose |
|---|---|
| `institutions` | Top-level organisation (PTIT). Master data. |
| `units` | Org tree inside an institution (board / office / faculty ...), self-referencing `parent_id`. |
| `positions` | Staff position catalogue (SYSTEM_ADMIN, FACULTY_ADMIN, LECTURER ...). |
| `users` | Account + profile + org/academic placement of every person (student, instructor, staff, admin). |
| `user_stats` | **New (split from users).** Hot gamification counters: points balance, login streak. |
| `user_assignments` | User holds a position in a unit; active rows drive unit-scoped data access (`ScopesByUnit`). |
| `user_identities` | **New.** External OAuth identities (Google `sub`), replaces `users.google_id`. |
| `access_tokens` | Opaque bearer tokens (SHA-256 hash), replaces Sanctum `personal_access_tokens`. |
| `refresh_tokens` | **New.** Rotating refresh tokens with family-based reuse detection. |
| `user_action_tokens` | **New.** One-time e-mailed tokens: `password_reset`, `email_verification` (replaces `password_reset_tokens` and Laravel's signed verification URLs). |
| `roles`, `permissions` | RBAC catalogue (Spatie without `guard_name`). |
| `role_permissions` | Role -> permission grants (was `role_has_permissions`). |
| `user_roles` | User -> role (was `model_has_roles`). |
| `user_permissions` | Direct user -> permission grants (was `model_has_permissions`). |

Deferred FKs (`000090_deferred_fks_identity`): `users.program_id -> programs`,
`users.major_id -> majors`, `users.specialization_id -> specializations`,
`users.cohort_id -> cohorts`, `users.administrative_class_id -> administrative_classes`
(all `ON DELETE SET NULL`, tables owned by `000004_academic`; names assumed identical to legacy).

## 2. Changes vs legacy

### users (35 columns analysed)

Usage evidence (grep of `backend/app`):

| Concern | Columns | Evidence / decision |
|---|---|---|
| Account | `name, email, password, email_verified_at, avatar, google_id, remember_token, user_type` | `AuthController`, `serializeUser`. `remember_token` only rotated on password reset (session "remember me" — no web sessions in Go) -> **dropped**. `google_id` -> `user_identities`. `password` -> `password_hash`, now **nullable** (OAuth-only accounts; legacy stored a random hash). |
| Gamification | `points_balance, streak_days, last_login_date, streak_last_updated` | Updated on every daily login and every point award (`PointService`), read by leaderboard (`ORDER BY points_balance DESC`). -> **split to `user_stats`**: avoids rewriting the wide users row (and its 15 indexes, so updates stay HOT) on every award, and gives the leaderboard a narrow `(points_balance DESC)` index. |
| Org | `institution_id, unit_id, staff_code` | `serializeUser.organization`, admin filters. Kept. |
| Student academic | `student_code, program_id, major_id, specialization_id, cohort_id, administrative_class_id, advisor_id, study_status` | Admin user list filters/sorts on all of them together with name search and role (`AdminController::usersQuery`), `serializeUser.academic`, advisor/enrollment controllers. **Kept on users** (see below). |
| Personal | `phone, id_card_number, gender, date_of_birth, nationality, hometown, permanent_address, bio, face_url` | Admin CRUD + import/export, `LearnerProfileService`, proctoring (`face_url`). Kept. |

**Why no `student_profiles` / `staff_profiles` split:** every admin user listing filters on
academic columns and name/email/role in one query, `serializeUser` always returns them, and
`study_status` is shared by students and staff (`dang_hoc` vs `dang_cong_tac`). A 1:1 split
would add a join to every user query without removing any nullable column problem worth it
(92% of users are students). The only split that pays for itself is the hot counters (`user_stats`).

Other users changes:
- `email` -> `citext` unique (case-insensitive login/uniqueness). `student_code`, `staff_code` -> `citext` unique.
- `avatar` -> `avatar_path`, `face_url` -> `face_image_path`: store object key (legacy stored a public URL via `PublicMediaUrl::toPublic`; data migration must strip the media base URL). Google avatars stay absolute `https://` URLs; Go resolves either form when building the API's `avatar` / `face_url`.
- CHECKs: `user_type IN (student, instructor, academic_manager, admin)` (only values written, see `OrgAcademicSeeder`; `role` is what authorises), `gender IN (male, female, other)`, `study_status IN` the 9 `App\Support\Enums\StudyStatus` values, lengths from the admin validators (bio 1000, phone 32, codes 50, id card 20, nationality 64).
- `search_text` generated stored column (`name email student_code staff_code phone`) + GIN trigram index: implements the admin `q`/`search` LIKE over 5 columns with one index. Go must query `search_text ILIKE '%' || $1 || '%'`.
- Indexes on every FK; partial indexes for sparse `specialization_id`, `advisor_id`; `created_at` (default admin sort). Dropped legacy composite `(institution_id, unit_id)`, `(program_id, major_id, cohort_id)` (filters are applied independently) and `study_status` (9 values, low selectivity).

### Auth tokens
- `personal_access_tokens` -> `access_tokens`: polymorphic `tokenable_*` -> `user_id` FK CASCADE; `token` (hex sha256) -> `token_hash bytea` (32 bytes, CHECK); `abilities` JSON text (always `["*"]` in data) -> `text[]`, NULL = all; `expires_at` now **NOT NULL** (legacy Sanctum `expiration = null` meant tokens never expired); `revoked_at` added (logout keeps an audit row; purge job deletes by `expires_at`); `updated_at` dropped (only `last_used_at` changes — Go should throttle that write, e.g. once per minute).
- `refresh_tokens` (new): short-lived access tokens + long-lived refresh tokens for web/mobile. Each refresh rotates: old row gets `rotated_at`; presenting a rotated token => revoke every row of its `family_id` (stolen-token detection). `access_token_id` lets logout revoke the pair.
- `password_reset_tokens` (PK email, bcrypt token) -> `user_action_tokens` with `purpose='password_reset'`: keyed by `user_id`, sha256 hash, explicit `expires_at` (legacy `auth.passwords.users.expire = 60` min), `consumed_at` for single use. `email_verification` uses the same table (Laravel used stateless signed URLs; Go stores a token instead). `email` column voids a token if the user's e-mail changed since issuance. `users.email_verified_at` is kept.

### OAuth: `user_identities` vs `users.google_id`
Chosen: `user_identities (provider, provider_subject)` unique, `(user_id, provider)` unique.
Reasons: keeps OAuth state off the users row; unlinking = delete row; adding another provider
(e.g. Microsoft 365 for university SSO) needs only a CHECK change; stores the provider e-mail
for audit. Legacy data had 0 users with `google_id`, so migration cost is nil.

### RBAC
- Spatie `guard_name` dropped: single value `web` in all 21 rows, no consumer reads it (`grep guard_name` in frontend/admin-ui/mobile: none).
- `model_type` dropped (only `App\Models\User`).
- Usage: route middleware `role:admin` and `permission:a|b` (15 permission names), `hasRole('admin')`, `can('manage_courses')`, `getAllPermissions()` (roles ∪ direct), `syncRoles`, `syncPermissions` on roles (admin "roles" screen). Permission check in Go = `user_permissions ∪ role_permissions via user_roles`.
- Users can have several roles (4 users hold two roles in dev data); API `role` = "first role" — Go must pick deterministically (see open questions).
- `user_permissions` kept although empty in data (cheap, matches `getAllPermissions` semantics).
- Join tables have no timestamps (Spatie had none).

### Org units
- `institutions.code`, `units.code`, `positions.code` -> `citext`.
- `units.institution_id`: CASCADE -> **RESTRICT** (master data; deleting an institution must not silently delete the org tree). `parent_id` stays SET NULL; CHECK `parent_id <> id`.
- `units.unit_type`, `institutions.institution_type` stay free text (API accepts any string ≤ 100) with a length CHECK.
- `positions.scope_level` CHECK `IN (institution, unit, cohort)`.
- `user_assignments` (not in the original task list but it is the user↔unit↔position link and references only identity tables, so it lives here): `unit_id`/`position_id` CASCADE -> RESTRICT; `status` CHECK `IN (active, inactive)`; dates CHECK; partial unique `(user_id, unit_id, position_id) WHERE active` (also serves the hot "active unit ids" lookup as index-only scan); partial unique "one primary assignment per user".

### Dropped
| Item | Reason |
|---|---|
| `users.remember_token` | Laravel session "remember me"; Go uses bearer tokens only. |
| `users.google_id` | Moved to `user_identities`. |
| `roles/permissions.guard_name` | Single value, unused. |
| `personal_access_tokens.tokenable_type`, `updated_at` | Polymorphism removed; only `last_used_at` changes. |
| `password_reset_tokens` (table) | Replaced by `user_action_tokens`. |

## 3. Old -> new column mapping

| Legacy | New | Transform |
|---|---|---|
| `users.id` | `users.id` | — |
| `users.name` | `users.name` | — |
| `users.email` | `users.email` | citext |
| `users.email_verified_at` | `users.email_verified_at` | timestamptz (legacy app tz Asia/Ho_Chi_Minh) |
| `users.password` | `users.password_hash` | bcrypt hash kept as-is (Go verifies `$2y$` bcrypt) |
| `users.avatar` | `users.avatar_path` | strip media base URL -> object key; keep external https URLs |
| `users.face_url` | `users.face_image_path` | strip media base URL -> object key |
| `users.google_id` | `user_identities(provider='google', provider_subject=google_id, email=users.email)` | only where not null |
| `users.bio` | `users.bio` | — |
| `users.institution_id`, `unit_id`, `program_id`, `major_id`, `specialization_id`, `cohort_id`, `administrative_class_id`, `advisor_id` | same names | — |
| `users.user_type` | `users.user_type` | — |
| `users.student_code`, `staff_code` | same | citext |
| `users.phone`, `id_card_number`, `gender`, `date_of_birth`, `nationality`, `hometown`, `permanent_address`, `study_status` | same | — |
| `users.points_balance` | `user_stats.points_balance` | one row per user |
| `users.streak_days` | `user_stats.streak_days` | |
| `users.last_login_date` | `user_stats.last_login_date` | |
| `users.streak_last_updated` | `user_stats.streak_last_updated` | |
| `users.remember_token` | — | dropped |
| `users.created_at`, `updated_at` | same | NULL -> now() |
| `roles.id, name` | `roles.id, name` | drop `guard_name` |
| `permissions.id, name` | `permissions.id, name` | drop `guard_name` |
| `role_has_permissions(permission_id, role_id)` | `role_permissions(role_id, permission_id)` | |
| `model_has_roles(role_id, model_id)` | `user_roles(user_id=model_id, role_id)` | `model_type = 'App\Models\User'` only |
| `model_has_permissions(permission_id, model_id)` | `user_permissions(user_id=model_id, permission_id)` | |
| `personal_access_tokens.*` | — | **not migrated**: Sanctum tokens are `id|plaintext` with sha256 of the plaintext; Go uses its own format — users log in again. |
| `password_reset_tokens.*` | — | not migrated (short-lived) |
| `institutions.*` | `institutions.*` | `is_active` tinyint -> boolean |
| `units.*` | `units.*` | `level` smallint, `is_active` boolean |
| `positions.*` | `positions.*` | |
| `user_assignments.*` | `user_assignments.*` | `is_primary` boolean |

## 4. Open questions

1. `user_type` vs roles: `user_type` only has 4 values and admin-created users (e.g. `finance`, `advisor` role holders) default to `student`. Should Go derive `user_type` from roles, or add `staff` to the CHECK?
2. API field `role` = "first role" of a multi-role user is non-deterministic in legacy. Proposed priority: admin > academic_manager > instructor > advisor > finance > student. Confirm.
3. Access/refresh lifetimes (proposed: access 1 h, refresh 30 days sliding). If the team prefers no refresh flow, drop `refresh_tokens` and use long `expires_at` on `access_tokens`.
4. ~~No account lock/disable column exists in legacy.~~ Decided: `users.disabled_at` added. A disabled user cannot log in (403 after the password check) and existing tokens stop working.
5. `user_assignments` was not in the identity table list given to me — confirm no other domain also creates it.
6. `000006_commerce_career` comments refer to `users.points_balance`; it is now `user_stats.points_balance`.
