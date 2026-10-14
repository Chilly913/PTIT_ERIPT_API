# Decisions & open questions

## Decided (2026-10-14)

| Topic | Decision |
|---|---|
| Database | Fresh PostgreSQL 17 schema (no MySQL, no MongoDB). Data/seed migration later. |
| Cutover | No shared DB with Laravel, so Go replaces Laravel per release once all modules are ported. |
| Auth tokens | Opaque bearer tokens (SHA-256 at rest), 30-day sliding expiry, revoked on logout/password reset. No refresh flow exposed yet. |
| Deleting users | Soft delete (`users.deleted_at`). Deleted users cannot log in; history rows are kept. |
| `user_type` | Adds `staff` for finance/advisor employees. |
| Primary role | admin > academic_manager > finance > advisor > instructor > student. |
| RBAC reference data | Roles/permissions matrix from `RoleSeeder.php` (migration 000092). |
| Disabling users | `users.disabled_at`: login answers 403 "Tài khoản của bạn đã bị khóa..." after the password check; existing tokens stop working. |
| Exam deletion | Blocked when the exam has attempts (DB trigger + 422 in the API); archive instead. |
| Quiz grading | `quiz_questions.points`, falling back to `questions.default_score` when NULL. `ordering` questions are graded (whole order must match). |
| Gradebook | Component weights must sum to 100 per course (API validation); `max_score > 0`. |
| Question groups | A question's group must be in the same bank (composite FK). |
| Legacy data | The new site starts with clean data, so legacy-only concerns (virtual-class fields of old rows, cascade vs RESTRICT on old master data) are closed: the schema stays as designed (RESTRICT on in-use master data). |

## Kept from Laravel on purpose (API compatibility)

- Login checks e-mail verification before the password, so an unverified account answers 403 even with a wrong password (reveals that the account exists and re-sends the verification mail). Candidate for change together with the frontend.

## Deferred (decide later)

- **RBAC**: extra `instructor` permissions found in the legacy dev DB (migration follows RoleSeeder for now); date window on `user_assignments` for unit scope.
- **Academic**: cohort code scope (per program for now); `btree_gist` overlap constraint for schedules (checked in code for now).
- **Commerce**: tuition through orders + PayOS; `vouchers.starts_at` in the admin UI; point `action` CHECK list (kept fixed for now).
