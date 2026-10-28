package tenant

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/apierror"
)

const contextKey = "tenant"

// AuthMiddleware authenticates the request with `Authorization: Bearer <api key>`
// and stores the tenant in the context. The tenant always comes from the key,
// never from a request parameter.
func AuthMiddleware(service *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		key, found := strings.CutPrefix(header, "Bearer ")
		if !found {
			apierror.Write(c, apierror.Unauthorized("missing or malformed Authorization header"))
			return
		}

		t, err := service.Authenticate(c.Request.Context(), strings.TrimSpace(key))
		if errors.Is(err, ErrInvalidAPIKey) {
			apierror.Write(c, apierror.Unauthorized("invalid api key"))
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
