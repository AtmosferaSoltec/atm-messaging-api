package tenant

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/apierror"
)

const contextKey = "tenant"

// AuthMiddleware authenticates the request with `Authorization: Bearer <credential>`
// and stores the tenant in the context. The credential is the service token of
// ATM-Auth-API (preferred) or, while systems migrate, a legacy API key (`atm_...`).
// The tenant always comes from the credential, never from a request parameter.
// verifier may be nil, which disables service tokens.
func AuthMiddleware(service *Service, verifier *TokenVerifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		credential, found := strings.CutPrefix(header, "Bearer ")
		if !found {
			apierror.Write(c, apierror.Unauthorized("missing or malformed Authorization header"))
			return
		}
		credential = strings.TrimSpace(credential)

		var t Tenant
		var err error
		if verifier != nil && !strings.HasPrefix(credential, apiKeyPrefix) {
			t, err = authenticateToken(c, service, verifier, credential)
		} else {
			t, err = service.Authenticate(c.Request.Context(), credential)
		}
		if errors.Is(err, ErrInvalidAPIKey) || errors.Is(err, ErrInvalidCredentials) {
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

func authenticateToken(c *gin.Context, service *Service, verifier *TokenVerifier, raw string) (Tenant, error) {
	code, err := verifier.SystemCode(raw)
	if err != nil {
		return Tenant{}, ErrInvalidCredentials
	}
	return service.AuthenticateSystem(c.Request.Context(), code)
}

// FromContext returns the authenticated tenant set by AuthMiddleware.
func FromContext(c *gin.Context) Tenant {
	value, _ := c.Get(contextKey)
	t, _ := value.(Tenant)
	return t
}
