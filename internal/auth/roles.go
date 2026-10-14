package auth

import (
	"context"
	"slices"

	"gorm.io/gorm"
)

// RoleService is the RBAC dependency of authentication (implemented by
// internal/rbac). Role and permission names are returned in a stable order.
type RoleService interface {
	// UserRolesPermissions returns the user's role names and all permission
	// names (direct grants ∪ grants via roles).
	UserRolesPermissions(ctx context.Context, userID int64) (roles, permissions []string, err error)
	// AssignRole adds role to the user (no-op if already assigned) using tx
	// (a transaction or plain *gorm.DB).
	AssignRole(ctx context.Context, tx *gorm.DB, userID int64, role string) error
}

// Role names with special meaning to auth.
const (
	RoleAdmin           = "admin"
	RoleAcademicManager = "academic_manager"
	RoleFinance         = "finance"
	RoleAdvisor         = "advisor"
	RoleInstructor      = "instructor"
	RoleStudent         = "student"
)

// rolePriority orders roles for the single "role" field clients receive.
var rolePriority = []string{RoleAdmin, RoleAcademicManager, RoleFinance, RoleAdvisor, RoleInstructor, RoleStudent}

// PrimaryRole picks the role shown to clients: admin > academic_manager >
// finance > advisor > instructor > student; unknown roles rank after those, in
// input order. Returns "" when roles is empty.
func PrimaryRole(roles []string) string {
	best, bestRank := "", len(rolePriority)+1
	for _, r := range roles {
		rank := slices.Index(rolePriority, r)
		if rank < 0 {
			rank = len(rolePriority)
		}
		if rank < bestRank {
			best, bestRank = r, rank
		}
	}
	return best
}

// HasRole reports whether roles contains any of want.
func HasRole(roles []string, want ...string) bool {
	for _, w := range want {
		if slices.Contains(roles, w) {
			return true
		}
	}
	return false
}
