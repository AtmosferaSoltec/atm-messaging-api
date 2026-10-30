package admin

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/apierror"
)

const userContextKey = "admin"

// RequireAdmin authenticates `Authorization: Bearer adm_...` with an admin
// session token. Service tokens are not accepted here, and admin tokens are
// not accepted on /v1.
func RequireAdmin(auth *AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c)
		if token == "" {
			apierror.Write(c, apierror.Unauthorized("missing or malformed Authorization header"))
			return
		}
		user, err := auth.Authenticate(c.Request.Context(), token)
		if errors.Is(err, ErrInvalidToken) {
			apierror.Write(c, apierror.Unauthorized("invalid or expired token"))
			return
		}
		if err != nil {
			apierror.Write(c, err)
			return
		}
		c.Set(userContextKey, user)
		c.Next()
	}
}

// FromContext returns the admin set by RequireAdmin.
func FromContext(c *gin.Context) User {
	value, _ := c.Get(userContextKey)
	user, _ := value.(User)
	return user
}

func bearerToken(c *gin.Context) string {
	token, found := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	if !found {
		return ""
	}
	return strings.TrimSpace(token)
}
