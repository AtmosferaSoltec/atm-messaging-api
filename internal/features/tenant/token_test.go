package tenant

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const testIssuer = "atm-auth"

func newTestVerifier(t *testing.T) (*TokenVerifier, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	verifier, err := NewTokenVerifier(base64.StdEncoding.EncodeToString(pemBytes), testIssuer)
	if err != nil {
		t.Fatal(err)
	}
	return verifier, key
}

func signToken(t *testing.T, key *rsa.PrivateKey, subject, role string, expires time.Duration) string {
	t.Helper()
	claims := serviceClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    testIssuer,
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expires)),
		},
		Role: role,
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSystemCode(t *testing.T) {
	verifier, key := newTestVerifier(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)

	tests := []struct {
		name  string
		token string
		want  string
	}{
		{"service token", signToken(t, key, "system:hotel", "service", time.Hour), "hotel"},
		{"user token", signToken(t, key, "user-1", "user", time.Hour), ""},
		{"expired", signToken(t, key, "system:hotel", "service", -time.Second), ""},
		{"other signing key", signToken(t, other, "system:hotel", "service", time.Hour), ""},
		{"garbage", "not-a-token", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := verifier.SystemCode(tt.token)
			if (err != nil) != (tt.want == "") || got != tt.want {
				t.Fatalf("SystemCode() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestAuthenticateSystem(t *testing.T) {
	ctx := context.Background()
	service := NewService(newFakeRepository())
	created, err := service.Create(ctx, "La Vendimia")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.AuthenticateSystem(ctx, "lavendimia"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unlinked system: err = %v", err)
	}
	if err := service.SetSystemCode(ctx, created.Tenant.ID, "Bad Code!"); !errors.Is(err, ErrInvalidSystemCode) {
		t.Fatalf("invalid code: err = %v", err)
	}
	if err := service.SetSystemCode(ctx, created.Tenant.ID, "lavendimia"); err != nil {
		t.Fatal(err)
	}
	got, err := service.AuthenticateSystem(ctx, "lavendimia")
	if err != nil || got.ID != created.Tenant.ID {
		t.Fatalf("linked system: %v, %v", got, err)
	}

	inactive := false
	if _, err := service.Update(ctx, created.Tenant.ID, nil, &inactive); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateSystem(ctx, "lavendimia"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("inactive tenant: err = %v", err)
	}
}

func TestAuthMiddlewareAcceptsTokenAndLegacyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	service := NewService(newFakeRepository())
	created, err := service.Create(ctx, "La Vendimia")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetSystemCode(ctx, created.Tenant.ID, "lavendimia"); err != nil {
		t.Fatal(err)
	}
	verifier, key := newTestVerifier(t)

	router := gin.New()
	router.GET("/", AuthMiddleware(service, verifier), func(c *gin.Context) {
		c.String(http.StatusOK, FromContext(c).ID.String())
	})

	for name, tt := range map[string]struct {
		credential string
		status     int
	}{
		"service token":   {signToken(t, key, "system:lavendimia", "service", time.Hour), http.StatusOK},
		"legacy api key":  {created.APIKey, http.StatusOK},
		"unlinked system": {signToken(t, key, "system:other", "service", time.Hour), http.StatusUnauthorized},
		"user token":      {signToken(t, key, "user-1", "user", time.Hour), http.StatusUnauthorized},
		"unknown api key": {"atm_nope", http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer "+tt.credential)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body.String())
			}
			if tt.status == http.StatusOK && rec.Body.String() != created.Tenant.ID.String() {
				t.Fatalf("wrong tenant: %s", rec.Body.String())
			}
		})
	}
}
