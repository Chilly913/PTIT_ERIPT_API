// Package auth holds the authenticated-principal context shared by the auth
// middleware (which sets it) and authorization code such as rbac (which reads it).
package auth

import "github.com/gin-gonic/gin"

const principalKey = "auth.principal"

// Principal is the authenticated caller of the current request.
type Principal struct {
	UserID  int64
	TokenID int64
}

// SetPrincipal stores the caller on the request context.
func SetPrincipal(c *gin.Context, p Principal) { c.Set(principalKey, p) }

// PrincipalFrom returns the caller, ok=false when the request is unauthenticated.
func PrincipalFrom(c *gin.Context) (Principal, bool) {
	v, ok := c.Get(principalKey)
	if !ok {
		return Principal{}, false
	}
	p, ok := v.(Principal)
	return p, ok
}

// UserID returns the caller's user id, or 0 when unauthenticated.
func UserID(c *gin.Context) int64 {
	p, _ := PrincipalFrom(c)
	return p.UserID
}
