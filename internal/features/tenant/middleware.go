package tenant

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/apierror"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/authtoken"
)

const contextKey = "tenant"

// AuthMiddleware authenticates the request with `Authorization: Bearer <token>`,
// the service token of ATM-Auth-API, and stores the tenant of its system in the
// context. The tenant always comes from the token, never from a request parameter.
func AuthMiddleware(service *Service, verifier *authtoken.Verifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, found := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		if !found {
			apierror.Write(c, apierror.Unauthorized("missing or malformed Authorization header"))
			return
		}
		code, err := verifier.SystemCode(strings.TrimSpace(raw))
		if err != nil {
			apierror.Write(c, apierror.Unauthorized("invalid credentials"))
			return
		}
		t, err := service.Authenticate(c.Request.Context(), code)
		if errors.Is(err, ErrInvalidCredentials) {
			apierror.Write(c, apierror.Unauthorized("invalid credentials"))
			return
		}
		if err != nil {
			apierror.Write(c, err)
			return
		}

		c.Set(contextKey, t)
		c.Next()
	}
}

// FromContext returns the authenticated tenant set by AuthMiddleware.
func FromContext(c *gin.Context) Tenant {
	value, _ := c.Get(contextKey)
	t, _ := value.(Tenant)
	return t
}
