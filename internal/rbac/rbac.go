// Package rbac ports the legacy Spatie laravel-permission behaviour: roles,
// permissions, the role/permission route middleware, the admin super-user rule
// (Gate::before) and unit-based data scoping (App\Concerns\ScopesByUnit).
//
// The canonical catalogue is seeded by migrations/000092_rbac_reference_data and
// mirrored here (AllRoles, AllPermissions, DefaultMatrix). See docs/RBAC.md.
package rbac

import (
	"errors"
	"slices"
	"strings"
)

// Role names (legacy RoleSeeder).
const (
	RoleAdmin           = "admin"
	RoleAcademicManager = "academic_manager"
	RoleInstructor      = "instructor"
	RoleAdvisor         = "advisor"
	RoleFinance         = "finance"
	RoleStudent         = "student"
)

// Permission names (legacy RoleSeeder).
const (
	PermViewDashboard     = "view_dashboard"
	PermManageUsers       = "manage_users"
	PermManageRoles       = "manage_roles"
	PermManageCourses     = "manage_courses"
	PermManageLessons     = "manage_lessons"
	PermManageExams       = "manage_exams"
	PermManageFinances    = "manage_finances"
	PermManageAcademic    = "manage_academic"
	PermManageEnrollments = "manage_enrollments"
	PermManageGrades      = "manage_grades"
	PermViewGrades        = "view_grades"
	PermAdviseStudents    = "advise_students"
	PermTakeExams         = "take_exams"
	PermViewReports       = "view_reports"
	PermSubmitReviews     = "submit_reviews"
)

// AllRoles lists the seeded roles in seeder order.
var AllRoles = []string{RoleAdmin, RoleAcademicManager, RoleInstructor, RoleAdvisor, RoleFinance, RoleStudent}

// AllPermissions lists the seeded permissions in seeder order.
var AllPermissions = []string{
	PermViewDashboard, PermManageUsers, PermManageRoles, PermManageCourses, PermManageLessons,
	PermManageExams, PermManageFinances, PermManageAcademic, PermManageEnrollments, PermManageGrades,
	PermViewGrades, PermAdviseStudents, PermTakeExams, PermViewReports, PermSubmitReviews,
}

// DefaultMatrix is the role -> permissions grant seeded by migration 000092.
// Admins may later change it at runtime (SyncRolePermissions), so authorization
// always reads the database, never this map.
var DefaultMatrix = map[string][]string{
	RoleAdmin: AllPermissions,
	RoleAcademicManager: {
		PermViewDashboard, PermManageAcademic, PermManageEnrollments,
		PermManageGrades, PermViewGrades, PermViewReports,
	},
	RoleInstructor: {
		PermViewDashboard, PermManageCourses, PermManageLessons, PermManageExams,
		PermManageGrades, PermViewGrades, PermViewReports, PermManageAcademic,
	},
	RoleAdvisor: {PermViewDashboard, PermAdviseStudents, PermViewGrades, PermViewReports},
	RoleFinance: {PermViewDashboard, PermManageFinances, PermViewReports},
	RoleStudent: {PermViewGrades, PermTakeExams, PermSubmitReviews},
}

// SuperAdminRole bypasses every permission check (legacy AppServiceProvider
// Gate::before: admin => true). Role checks are NOT bypassed (Spatie hasRole).
const SuperAdminRole = RoleAdmin

// rolePriority decides the API "role" field of multi-role users.
var rolePriority = []string{RoleAdmin, RoleAcademicManager, RoleFinance, RoleAdvisor, RoleInstructor, RoleStudent}

// PrimaryRole returns the highest-priority role
// (admin > academic_manager > finance > advisor > instructor > student).
// Unknown roles rank after the known ones (first in input order); "" when empty.
func PrimaryRole(roles []string) string {
	for _, r := range rolePriority {
		if slices.Contains(roles, r) {
			return r
		}
	}
	if len(roles) > 0 {
		return roles[0]
	}
	return ""
}

// Errors returned by the write helpers.
var (
	ErrRoleNotFound       = errors.New("rbac: role does not exist")
	ErrPermissionNotFound = errors.New("rbac: permission does not exist")
)

// HasAnyOf reports whether have contains at least one of want.
func HasAnyOf(have []string, want ...string) bool {
	for _, w := range want {
		if slices.Contains(have, w) {
			return true
		}
	}
	return false
}

// CanAny applies Spatie canAny + Gate::before: true when roles contain admin or
// perms contain any of want.
func CanAny(roles, perms []string, want ...string) bool {
	return slices.Contains(roles, SuperAdminRole) || HasAnyOf(perms, want...)
}

// splitNames flattens "a|b" style arguments (Spatie middleware syntax) and drops blanks.
func splitNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		for _, p := range strings.Split(n, "|") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// normalize returns a sorted, deduplicated, non-nil copy.
func normalize(in []string) []string {
	out := make([]string, 0, len(in))
	out = append(out, in...)
	slices.Sort(out)
	return slices.Compact(out)
}
