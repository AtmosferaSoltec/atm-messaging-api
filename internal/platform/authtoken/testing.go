package authtoken

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Issuer is the issuer of the tokens NewForTest signs.
const Issuer = "atm-auth-api"

// Signer mints tokens that the Verifier returned next to it accepts.
type Signer struct {
	t   testing.TB
	key *rsa.PrivateKey
}

// NewForTest returns a Verifier and a Signer sharing a fresh key pair.
func NewForTest(t testing.TB) (*Verifier, *Signer) {
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
	verifier, err := New(base64.StdEncoding.EncodeToString(pemBytes), Issuer)
	if err != nil {
		t.Fatal(err)
	}
	return verifier, &Signer{t: t, key: key}
}

// Token signs a token with the given subject, role and lifetime (negative: already expired).
func (s *Signer) Token(subject, role string, ttl time.Duration) string {
	return s.sign(Issuer, s.key, subject, role, ttl)
}

// TokenFrom signs with another issuer, to test that foreign tokens are refused.
func (s *Signer) TokenFrom(issuer, subject, role string, ttl time.Duration) string {
	return s.sign(issuer, s.key, subject, role, ttl)
}

// ForeignToken signs with a different key, to test that forged tokens are refused.
func (s *Signer) ForeignToken(subject, role string, ttl time.Duration) string {
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		s.t.Fatal(err)
	}
	return s.sign(Issuer, other, subject, role, ttl)
}

func (s *Signer) sign(issuer string, key *rsa.PrivateKey, subject, role string, ttl time.Duration) string {
	s.t.Helper()
	raw, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
		},
		Role: role,
		Name: "Test Operator",
	}).SignedString(key)
	if err != nil {
		s.t.Fatal(err)
	}
	return raw
}
