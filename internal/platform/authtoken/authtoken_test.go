package authtoken

import (
	"errors"
	"testing"
	"time"
)

func TestSystemCode(t *testing.T) {
	verifier, signer := NewForTest(t)

	tests := []struct {
		name  string
		token string
		want  string
	}{
		{"service token", signer.Token("system:hotel", "service", time.Hour), "hotel"},
		{"user token", signer.Token("user-1", "user", time.Hour), ""},
		{"superadmin token", signer.Token("admin-1", "superadmin", time.Hour), ""},
		{"service role without system subject", signer.Token("user-1", "service", time.Hour), ""},
		{"expired", signer.Token("system:hotel", "service", -time.Second), ""},
		{"other issuer", signer.TokenFrom("someone-else", "system:hotel", "service", time.Hour), ""},
		{"other signing key", signer.ForeignToken("system:hotel", "service", time.Hour), ""},
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

func TestSuperadmin(t *testing.T) {
	verifier, signer := NewForTest(t)

	op, err := verifier.Superadmin(signer.Token("admin-1", "superadmin", time.Hour))
	if err != nil || op.Subject != "admin-1" || op.Name != "Test Operator" {
		t.Fatalf("superadmin token: %+v, %v", op, err)
	}
	for name, token := range map[string]string{
		"user":    signer.Token("user-1", "user", time.Hour),
		"service": signer.Token("system:hotel", "service", time.Hour),
	} {
		if _, err := verifier.Superadmin(token); !errors.Is(err, ErrNotSuperadmin) {
			t.Errorf("%s token: expected ErrNotSuperadmin, got %v", name, err)
		}
	}
	for name, token := range map[string]string{
		"expired": signer.Token("admin-1", "superadmin", -time.Second),
		"forged":  signer.ForeignToken("admin-1", "superadmin", time.Hour),
		"garbage": "nope",
	} {
		if _, err := verifier.Superadmin(token); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s token: expected ErrInvalidToken, got %v", name, err)
		}
	}
}
