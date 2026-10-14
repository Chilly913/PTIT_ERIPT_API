# RBAC (Phase 1)

Go port of the legacy Spatie laravel-permission setup. Code: `internal/rbac`.
Reference data: `migrations/000092_rbac_reference_data` (mirror of legacy `RoleSeeder`).
Schema: `roles`, `permissions`, `role_permissions`, `user_roles`, `user_permissions`,
`user_assignments` (see `docs/db/02_identity.md`).

## Role -> permission matrix (seeded)

| permission | admin | academic_manager | instructor | advisor | finance | student |
|---|:-:|:-:|:-:|:-:|:-:|:-:|
| view_dashboard     | x | x | x | x | x |   |
| manage_users       | x |   |   |   |   |   |
| manage_roles       | x |   |   |   |   |   |
| manage_courses     | x |   | x |   |   |   |
| manage_lessons     | x |   | x |   |   |   |
| manage_exams       | x |   | x |   |   |   |
| manage_finances    | x |   |   |   | x |   |
| manage_academic    | x | x | x |   |   |   |
| manage_enrollments | x | x |   |   |   |   |
| manage_grades      | x | x | x |   |   |   |
| view_grades        | x | x | x | x |   | x |
| advise_students    | x |   |   | x |   |   |
| take_exams         | x |   |   |   |   | x |
| view_reports       | x | x | x | x | x |   |
| submit_reviews     | x |   |   |   |   | x |

Admins can change a role's permissions at runtime (`SyncRolePermissions`), so code must
authorise from the database (`Service`), never from `rbac.DefaultMatrix`.

## Semantics (kept from legacy)

- Effective permissions = direct `user_permissions` ∪ permissions of the user's roles.
- **Super-admin**: legacy `AppServiceProvider` has `Gate::before(admin => true)`. It applies to
  every *permission* check (`can`, `canAny`, `permission:` and `role_or_permission:` middleware,
  policies) but **not** to role checks (`hasRole`, `role:` middleware). Go: `CanAny`/`Can`/
  `RequirePermission`/`RequireRoleOrPermission` pass for admin; `HasRole`/`RequireRole` do not.
- Middleware arguments are any-of; `"a|b"` and `("a", "b")` are equivalent.
- Unauthenticated -> `401 {"message":"Unauthenticated."}` (legacy `auth:sanctum` always runs first).
- Forbidden -> 403 with the Spatie `UnauthorizedException` message (exception details disabled
  in legacy `config/permission.php`):

| middleware | 403 body |
|---|---|
| `RequireRole` | `{"message":"User does not have the right roles."}` |
| `RequirePermission` | `{"message":"User does not have the right permissions."}` |
| `RequireRoleOrPermission` | `{"message":"User does not have any of the necessary access rights."}` |

- API `role` field of multi-role users: `rbac.PrimaryRole` with priority
  admin > academic_manager > finance > advisor > instructor > student.

## Caching

`Service.UserRolesPermissions` caches a per-user snapshot in Redis
(`<REDIS_PREFIX>rbac:user:<id>`, TTL 5 min as a safety net). Entries carry a version stamp
(`rbac:gen` global generation + `rbac:uver:<id>` per-user version) read in the same MGET,
so invalidation is race-free: user writes bump the user version, `SyncRolePermissions`
bumps the generation. Middleware memoises the snapshot per request (`Service.Caller`).
Redis errors fall back to the database (logged).

Write helpers take an optional `*gorm.DB` tx. With `nil` they run in their own transaction and
invalidate after commit; with a caller tx they invalidate immediately — call
`InvalidateUser`/`InvalidateAll` again after your commit.

## Usage

```go
rb := rbac.FromApp(a) // or rbac.New(db, rbac.NewRedisCache(rdb, prefix, 0), log)

admin := api.Group("/admin", authMW, rb.RequireRole(rbac.RoleAdmin))
api.POST("/courses", authMW, rb.RequirePermission(rbac.PermManageCourses), h.store)

// in handlers
roles, perms, _, err := rb.Caller(c)
ok, err := rb.Can(ctx, userID, rbac.PermManageCourses)       // $user->can()
isAdmin, err := rb.HasRole(ctx, userID, rbac.RoleAdmin)       // $user->hasRole('admin')
err = rb.SyncRoles(ctx, tx, userID, payload.Role)             // $user->syncRoles([$role])

// unit scoping (App\Concerns\ScopesByUnit::scopeByUnitColumn)
scope, err := rb.UnitScopeFor(ctx, userID)
db.Scopes(scope.ScopeByUnit("unit_id")).Find(&rows)
```

`ActiveUnitIDs` returns `(nil, true)` for admins; otherwise the units of assignments with
`status = 'active'` and today inside `[start_date, end_date]` (NULL = open). `ScopeByUnit`
adds no filter when unrestricted, `1 = 0` when the user has no active unit.
Legacy only checked `status`; the date window is a deliberate tightening.

## Legacy route middleware -> Go

| Legacy (routes/api) | Routes | Go |
|---|---|---|
| `role:admin` (ai.php:30) | `/admin/ai/*` | `rb.RequireRole(rbac.RoleAdmin)` |
| `role:admin` (career.php:15) | `/admin/career-paths*` ... | `rb.RequireRole(rbac.RoleAdmin)` |
| `role:admin` (user.php:150) | `/admin/*` (stats, users, roles, ...) | `rb.RequireRole(rbac.RoleAdmin)` |
| `role:admin` (user.php:294) | `/admin/vouchers*` ... | `rb.RequireRole(rbac.RoleAdmin)` |
| `permission:manage_courses` (course.php:38) | course create/update/delete | `rb.RequirePermission(rbac.PermManageCourses)` |
| `permission:manage_lessons\|manage_courses` (course.php:52) | lesson CRUD | `rb.RequirePermission(rbac.PermManageLessons, rbac.PermManageCourses)` |
| `permission:submit_reviews` (course.php:97) | course reviews | `rb.RequirePermission(rbac.PermSubmitReviews)` |
| `permission:manage_exams` (quiz.php:15) | quiz authoring, question banks | `rb.RequirePermission(rbac.PermManageExams)` |
| `permission:take_exams` (quiz.php:66) | exam pre-check/start/submit | `rb.RequirePermission(rbac.PermTakeExams)` |
| `permission:manage_courses\|view_dashboard` (user.php:80) | `/instructor/stats`, students, revenue | `rb.RequirePermission(rbac.PermManageCourses, rbac.PermViewDashboard)` |
| `permission:view_grades\|manage_grades` (user.php:87) | gradebook reads | `rb.RequirePermission(rbac.PermViewGrades, rbac.PermManageGrades)` |
| `permission:manage_grades` (user.php:93) | gradebook writes | `rb.RequirePermission(rbac.PermManageGrades)` |
| `permission:manage_grades\|manage_courses` (user.php:100) | offline sessions | `rb.RequirePermission(rbac.PermManageGrades, rbac.PermManageCourses)` |
| `permission:manage_academic\|manage_courses` (user.php:115) | `/instructor/academic/*` | `rb.RequirePermission(rbac.PermManageAcademic, rbac.PermManageCourses)` |
| `permission:advise_students` (user.php:123) | `/advisor/*` | `rb.RequirePermission(rbac.PermAdviseStudents)` |
| `permission:view_grades` (user.php:131) | `/me/transcript` | `rb.RequirePermission(rbac.PermViewGrades)` |

`role_or_permission:` is registered in legacy but not used by any route; Go provides
`RequireRoleOrPermission` for parity.

Controller-level checks keep their own bodies: `Authorize::forbidden` renders
`{"message":"Forbidden"}` (or a custom message) and `AdminController::ensureAdmin` its own
message — port those with `httpx.Forbidden(c, msg)` after `rb.CanAny`/`rb.HasRole`.
