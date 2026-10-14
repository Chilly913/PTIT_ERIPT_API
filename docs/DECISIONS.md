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

## Kept from Laravel on purpose (API compatibility)

- Login checks e-mail verification before the password, so an unverified account answers 403 even with a wrong password (reveals that the account exists and re-sends the verification mail). Candidate for change together with the frontend.

## Open questions

### Identity / RBAC
1. The legacy dev DB grants `instructor` 6 extra permissions not in RoleSeeder (`manage_users`, `manage_finances`, `manage_enrollments`, `advise_students`, `submit_reviews`, `take_exams`), probably edited through the admin UI. Migration 000092 follows RoleSeeder. Confirm.
2. Unit scoping now also requires today to be within `user_assignments.start_date..end_date` (legacy only checked `status`).
3. Should users get an explicit "disabled" flag in addition to soft delete?

### Learning
4. Should virtual-class `meeting_password` / `start_url` be hidden from learners? Legacy returns them.

### Academic
5. RESTRICT instead of silent cascade when deleting institutions, program types and terms that are in use — acceptable?
6. Cohort codes unique per program (current) or per institution?
7. Add `btree_gist` to block overlapping schedule slots?

### Assessment
8. Deleting an exam cascades to its quiz, attempts and violations. Block deletion when results exist instead?
9. Grade with `quiz_questions.points` instead of `questions.default_score`?
10. Enforce grade component weights summing to 100 per course?
11. `max_score > 0` (legacy allowed 0) — acceptable?
12. Force a question's group to belong to the same bank?
13. Implement grading for `ordering` questions?

### Commerce / career
14. Should tuition payments go through orders + PayOS?
15. The admin UI does not send `vouchers.starts_at` yet.
16. Point `action` values are a fixed CHECK list. Keep it, or make it free text?
