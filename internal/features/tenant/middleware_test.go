package tenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/authtoken"
)

func TestAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := NewService(newFakeRepository())
	verifier, signer := authtoken.NewForTest(t)

	router := gin.New()
	router.GET("/", AuthMiddleware(service, verifier), func(c *gin.Context) {
		c.String(http.StatusOK, FromContext(c).SystemCode)
	})

	for name, tt := range map[string]struct {
		header string
		status int
	}{
		"service token": {"Bearer " + signer.Token("system:lavendimia", "service", time.Hour), http.StatusOK},
		"user token":    {"Bearer " + signer.Token("user-1", "user", time.Hour), http.StatusUnauthorized},
		"expired token": {"Bearer " + signer.Token("system:lavendimia", "service", -time.Second), http.StatusUnauthorized},
		"old api key":   {"Bearer atm_oldkey", http.StatusUnauthorized},
		"wrong scheme":  {"Basic " + signer.Token("system:lavendimia", "service", time.Hour), http.StatusUnauthorized},
		"no header":     {"", http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body.String())
			}
			if tt.status == http.StatusOK && rec.Body.String() != "lavendimia" {
				t.Fatalf("wrong tenant: %s", rec.Body.String())
			}
		})
	}
}

func TestAuthMiddlewareRefusesAnInactiveTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	service := NewService(newFakeRepository())
	verifier, signer := authtoken.NewForTest(t)
	created, err := service.Provision(ctx, "lavendimia")
	if err != nil {
		t.Fatal(err)
	}
	inactive := false
	if _, err := service.Update(ctx, created.ID, nil, &inactive); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.GET("/", AuthMiddleware(service, verifier), func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+signer.Token("system:lavendimia", "service", time.Hour))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
