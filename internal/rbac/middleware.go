package rbac

import (
	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// 403 messages of Spatie\Permission\Exceptions\UnauthorizedException
// (display_role_in_exception / display_permission_in_exception are false in legacy
// config/permission.php, app locale "en"), rendered by Laravel as {"message": ...}.
const (
	MsgMissingRoles       = "User does not have the right roles."
	MsgMissingPermissions = "User does not have the right permissions."
	MsgMissingAccess      = "User does not have any of the necessary access rights."
)

const ctxKey = "rbac.snapshot"

type snapshot struct{ roles, perms []string }

// Caller returns the authenticated caller's roles and permissions, memoized on the
// request. ok=false when the request is unauthenticated.
func (s *Service) Caller(c *gin.Context) (roles, perms []string, ok bool, err error) {
	uid := auth.UserID(c)
	if uid == 0 {
		return nil, nil, false, nil
	}
	if v, exists := c.Get(ctxKey); exists {
		if sn, isSnap := v.(snapshot); isSnap {
			return sn.roles, sn.perms, true, nil
		}
	}
	roles, perms, err = s.UserRolesPermissions(c.Request.Context(), uid)
	if err != nil {
		return nil, nil, true, err
	}
	c.Set(ctxKey, snapshot{roles: roles, perms: perms})
	return roles, perms, true, nil
}

// RequireRole is Spatie `role:a|b`: passes when the caller has ANY of roles.
// No admin bypass (Spatie RoleMiddleware uses hasAnyRole, not the Gate).
// Arguments may be variadic or pipe-separated.
func (s *Service) RequireRole(roles ...string) gin.HandlerFunc {
	want := splitNames(roles)
	return s.guard(MsgMissingRoles, func(r, _ []string) bool { return HasAnyOf(r, want...) })
}

// RequirePermission is Spatie `permission:a|b`: passes when the caller is admin
// (Gate::before) or holds ANY of perms.
func (s *Service) RequirePermission(perms ...string) gin.HandlerFunc {
	want := splitNames(perms)
	return s.guard(MsgMissingPermissions, func(r, p []string) bool { return CanAny(r, p, want...) })
}

// RequireRoleOrPermission is Spatie `role_or_permission:a|b`: passes when the caller
// is admin, holds ANY of names as a permission, or ANY of names as a role.
func (s *Service) RequireRoleOrPermission(names ...string) gin.HandlerFunc {
	want := splitNames(names)
	return s.guard(MsgMissingAccess, func(r, p []string) bool {
		return CanAny(r, p, want...) || HasAnyOf(r, want...)
	})
}

func (s *Service) guard(msg string, allow func(roles, perms []string) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		roles, perms, ok, err := s.Caller(c)
		switch {
		case !ok:
			httpx.Unauthenticated(c)
		case err != nil:
			httpx.Fail(c, err)
		case !allow(roles, perms):
			httpx.Forbidden(c, msg)
		default:
			c.Next()
		}
	}
}
