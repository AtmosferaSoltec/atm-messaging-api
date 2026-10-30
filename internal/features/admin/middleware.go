package admin

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/apierror"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/authtoken"
)

const operatorContextKey = "admin"

// RequireAdmin accepts only the access token of a superadmin of ATM-Auth-API.
// Service tokens and the tokens of ordinary users are refused.
func RequireAdmin(verifier *authtoken.Verifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, found := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		if !found || strings.TrimSpace(token) == "" {
			apierror.Write(c, apierror.Unauthorized("missing or malformed Authorization header"))
			return
		}
		operator, err := verifier.Superadmin(strings.TrimSpace(token))
		if errors.Is(err, authtoken.ErrNotSuperadmin) {
			apierror.Write(c, apierror.New(403, "forbidden", "superadmin access is required"))
			return
		}
		if err != nil {
			apierror.Write(c, apierror.Unauthorized("invalid or expired token"))
			return
		}
		c.Set(operatorContextKey, operator)
		c.Next()
	}
}
