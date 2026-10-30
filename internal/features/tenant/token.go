package tenant

import (
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

const systemSubjectPrefix = "system:"

// serviceClaims are the claims of a service token issued by ATM-Auth-API
// (POST /v1/token): role "service" and subject "system:<code>".
type serviceClaims struct {
	jwt.RegisteredClaims
	Role string `json:"role"`
}

// TokenVerifier validates service tokens locally with the public key of
// ATM-Auth-API. It never calls auth on a request.
type TokenVerifier struct {
	key    *rsa.PublicKey
	issuer string
}

// NewTokenVerifier parses the base64 PEM public key of ATM-Auth-API.
func NewTokenVerifier(publicKeyBase64, issuer string) (*TokenVerifier, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyBase64))
	if err != nil {
		return nil, errors.New("invalid AUTH_PUBLIC_KEY_BASE64")
	}
	key, err := jwt.ParseRSAPublicKeyFromPEM(data)
	if err != nil || key.N.BitLen() < 2048 {
		return nil, errors.New("invalid RSA public key (minimum 2048 bits)")
	}
	return &TokenVerifier{key: key, issuer: issuer}, nil
}

// SystemCode checks the token and returns the code of the system it was issued to.
func (v *TokenVerifier) SystemCode(raw string) (string, error) {
	var claims serviceClaims
	_, err := jwt.ParseWithClaims(raw, &claims,
		func(*jwt.Token) (any, error) { return v.key, nil },
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	code, isSystem := strings.CutPrefix(claims.Subject, systemSubjectPrefix)
	if err != nil || claims.Role != "service" || !isSystem || code == "" {
		return "", errors.New("invalid service token")
	}
	return code, nil
}
