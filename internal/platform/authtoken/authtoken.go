// Package authtoken validates the access tokens of ATM-Auth-API locally, with its
// public key. It never calls auth on a request.
package authtoken

import (
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

const (
	systemSubjectPrefix = "system:"
	roleService         = "service"
	roleSuperadmin      = "superadmin"
)

// ErrInvalidToken: the token is malformed, expired, from another issuer, or not
// of the kind the caller asked for.
var ErrInvalidToken = errors.New("invalid token")

type claims struct {
	jwt.RegisteredClaims
	Role string `json:"role"`
	Name string `json:"name"`
}

// Verifier checks tokens signed by ATM-Auth-API.
type Verifier struct {
	key    *rsa.PublicKey
	issuer string
}

// New parses the base64 PEM public key of ATM-Auth-API.
func New(publicKeyBase64, issuer string) (*Verifier, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyBase64))
	if err != nil {
		return nil, errors.New("invalid AUTH_PUBLIC_KEY_BASE64")
	}
	key, err := jwt.ParseRSAPublicKeyFromPEM(data)
	if err != nil || key.N.BitLen() < 2048 {
		return nil, errors.New("invalid RSA public key (minimum 2048 bits)")
	}
	return &Verifier{key: key, issuer: issuer}, nil
}

func (v *Verifier) verify(raw string) (claims, error) {
	var c claims
	_, err := jwt.ParseWithClaims(raw, &c,
		func(*jwt.Token) (any, error) { return v.key, nil },
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || c.Subject == "" {
		return claims{}, ErrInvalidToken
	}
	return c, nil
}

// SystemCode checks a service token and returns the code of the system it was issued to.
func (v *Verifier) SystemCode(raw string) (string, error) {
	c, err := v.verify(raw)
	code, isSystem := strings.CutPrefix(c.Subject, systemSubjectPrefix)
	if err != nil || c.Role != roleService || !isSystem || code == "" {
		return "", ErrInvalidToken
	}
	return code, nil
}

// Operator is the superadmin a user token belongs to.
type Operator struct {
	Subject string
	Name    string
}

// Superadmin checks a user token and returns the superadmin it belongs to. A valid
// token of any other role fails with ErrNotSuperadmin.
func (v *Verifier) Superadmin(raw string) (Operator, error) {
	c, err := v.verify(raw)
	if err != nil {
		return Operator{}, err
	}
	if c.Role != roleSuperadmin {
		return Operator{}, ErrNotSuperadmin
	}
	return Operator{Subject: c.Subject, Name: c.Name}, nil
}

// ErrNotSuperadmin: the token is valid but its owner is not a superadmin.
var ErrNotSuperadmin = errors.New("not a superadmin")
