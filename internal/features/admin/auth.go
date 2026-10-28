// Package admin serves the administration API used by the React panel. There is
// a single operator: its username and password come from the environment, with
// no user table. Login exchanges them for a short-lived opaque token kept in
// memory (a restart signs everyone out). Tenant API keys never work here.
package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	tokenPrefix   = "adm_"
	tokenBytes    = 32
	maxCredential = 256
)

// Errors returned by the AuthService.
var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidToken       = errors.New("invalid or expired token")
)

// User is the operator of the panel.
type User struct {
	Username string
}

// LoginResult is what a successful login returns.
type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	User      User
}

// AuthService checks the operator's credentials and issues session tokens.
type AuthService struct {
	username       string
	usernameDigest [sha256.Size]byte
	passwordDigest [sha256.Size]byte
	tokenTTL       time.Duration
	now            func() time.Time

	mu     sync.Mutex
	tokens map[string]time.Time // SHA-256 of the token -> expiry
}

// NewAuthService creates an AuthService for the single operator.
func NewAuthService(username, password string, tokenTTL time.Duration) (*AuthService, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("ADMIN_USER is required")
	}
	// Any non-empty password is accepted: no length or character rules.
	if password == "" {
		return nil, errors.New("ADMIN_PASSWORD is required")
	}
	return &AuthService{
		username:       username,
		usernameDigest: sha256.Sum256([]byte(username)),
		passwordDigest: sha256.Sum256([]byte(password)),
		tokenTTL:       tokenTTL,
		now:            time.Now,
		tokens:         make(map[string]time.Time),
	}, nil
}

// Login checks the credentials and opens a session. Both values are always
// compared, in constant time, so the response does not say which one was wrong.
func (s *AuthService) Login(_ context.Context, username, password string) (LoginResult, error) {
	if len(username) > maxCredential || len(password) > maxCredential {
		return LoginResult{}, ErrInvalidCredentials
	}
	userDigest := sha256.Sum256([]byte(strings.TrimSpace(username)))
	passwordDigest := sha256.Sum256([]byte(password))
	userOK := subtle.ConstantTimeCompare(userDigest[:], s.usernameDigest[:])
	passwordOK := subtle.ConstantTimeCompare(passwordDigest[:], s.passwordDigest[:])
	if userOK&passwordOK != 1 {
		return LoginResult{}, ErrInvalidCredentials
	}

	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return LoginResult{}, fmt.Errorf("generate token: %w", err)
	}
	token := tokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	expiresAt := s.now().Add(s.tokenTTL)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	s.tokens[hashToken(token)] = expiresAt
	return LoginResult{Token: token, ExpiresAt: expiresAt, User: User{Username: s.username}}, nil
}

// Authenticate resolves the operator that owns a session token.
func (s *AuthService) Authenticate(_ context.Context, token string) (User, error) {
	if !strings.HasPrefix(token, tokenPrefix) {
		return User{}, ErrInvalidToken
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt, ok := s.tokens[hashToken(token)]
	if !ok || !expiresAt.After(s.now()) {
		return User{}, ErrInvalidToken
	}
	return User{Username: s.username}, nil
}

// Logout ends the session of a token.
func (s *AuthService) Logout(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, hashToken(token))
	return nil
}

func (s *AuthService) purgeExpiredLocked() {
	now := s.now()
	for hash, expiresAt := range s.tokens {
		if !expiresAt.After(now) {
			delete(s.tokens, hash)
		}
	}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
