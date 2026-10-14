package auth

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Required authenticates the bearer token (Laravel auth:sanctum): missing or
// invalid tokens get 401 {"message":"Unauthenticated."}.
func (s *TokenService) Required() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, err := s.Verify(c.Request.Context(), BearerToken(c.Request))
		if err != nil {
			if errors.Is(err, ErrInvalidToken) {
				httpx.Unauthenticated(c)
				return
			}
			httpx.Fail(c, err)
			return
		}
		SetPrincipal(c, p)
		c.Next()
	}
}

// Optional sets the principal when a valid bearer token is present and
// otherwise continues as a guest (auth('sanctum')->user() semantics).
func (s *TokenService) Optional() gin.HandlerFunc {
	return func(c *gin.Context) {
		if tok := BearerToken(c.Request); tok != "" {
			p, err := s.Verify(c.Request.Context(), tok)
			switch {
			case err == nil:
				SetPrincipal(c, p)
			case !errors.Is(err, ErrInvalidToken):
				httpx.Fail(c, err)
				return
			}
		}
		c.Next()
	}
}
