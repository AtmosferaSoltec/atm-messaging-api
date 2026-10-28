// Package connect creates the public links a customer opens to scan a QR code
// and link their own WhatsApp, without having any account. A link belongs to
// one session, expires, and only lets its holder watch that session's QR and
// status: it cannot disconnect, send or see anything else.
package connect

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/session"
)

const (
	tokenPrefix = "cl_"
	tokenBytes  = 32
	maxLabel    = 100
)

// Errors returned by the Service.
var (
	// ErrInvalidLink covers unknown, replaced and expired links alike, so a
	// holder of a dead link learns nothing.
	ErrInvalidLink  = errors.New("connect link is invalid or expired")
	ErrInvalidLabel = errors.New("label must be at most 100 characters")
)

// Created is a new link. Token is the only time it exists in clear.
type Created struct {
	Token     string
	Label     string
	ExpiresAt time.Time
}

// Repository stores the links.
type Repository interface {
	// Replace stores the session's link, invalidating any previous one.
	Replace(ctx context.Context, sessionID uuid.UUID, tokenHash, label string, expiresAt time.Time) error
	// FindByTokenHash returns the session, label and expiry of an unexpired link.
	FindByTokenHash(ctx context.Context, tokenHash string, now time.Time) (sessionID uuid.UUID, label string, expiresAt time.Time, err error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a PostgreSQL-backed Repository.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) Replace(ctx context.Context, sessionID uuid.UUID, tokenHash, label string, expiresAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO connect_links (session_id, token_hash, label, expires_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (session_id) DO UPDATE
		SET token_hash = EXCLUDED.token_hash, label = EXCLUDED.label,
		    expires_at = EXCLUDED.expires_at, created_at = now()`,
		sessionID, tokenHash, label, expiresAt)
	if err != nil {
		return fmt.Errorf("store connect link: %w", err)
	}
	return nil
}

func (r *pgRepository) FindByTokenHash(ctx context.Context, tokenHash string, now time.Time) (uuid.UUID, string, time.Time, error) {
	var sessionID uuid.UUID
	var label string
	var expiresAt time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT session_id, label, expires_at FROM connect_links WHERE token_hash = $1 AND expires_at > $2`,
		tokenHash, now).Scan(&sessionID, &label, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", time.Time{}, ErrInvalidLink
	}
	if err != nil {
		return uuid.Nil, "", time.Time{}, fmt.Errorf("find connect link: %w", err)
	}
	return sessionID, label, expiresAt, nil
}

// Sessions is what the links need from the session feature.
type Sessions interface {
	Lookup(ctx context.Context, id uuid.UUID) (session.Session, error)
	Watch(ctx context.Context, tenantID, id uuid.UUID) (session.Session, <-chan channel.Event, func(), error)
}

// Service holds the connect-link rules.
type Service struct {
	repo     Repository
	sessions Sessions
	ttl      time.Duration
	now      func() time.Time
}

// NewService creates a Service whose links last ttl.
func NewService(repo Repository, sessions Sessions, ttl time.Duration) *Service {
	return &Service{repo: repo, sessions: sessions, ttl: ttl, now: time.Now}
}

// Create issues a new link for a session, replacing the previous one. An empty
// label falls back to the account reference.
func (s *Service) Create(ctx context.Context, sessionID uuid.UUID, label string) (Created, error) {
	label = strings.TrimSpace(label)
	if len(label) > maxLabel {
		return Created{}, ErrInvalidLabel
	}
	sess, err := s.sessions.Lookup(ctx, sessionID)
	if err != nil {
		return Created{}, err
	}
	if label == "" {
		label = sess.AccountRef
	}

	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return Created{}, fmt.Errorf("generate connect token: %w", err)
	}
	token := tokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	expiresAt := s.now().Add(s.ttl)

	if err := s.repo.Replace(ctx, sessionID, hashToken(token), label, expiresAt); err != nil {
		return Created{}, err
	}
	return Created{Token: token, Label: label, ExpiresAt: expiresAt}, nil
}

// Info is what the public page may show about a link.
type Info struct {
	Label     string
	Channel   channel.Type
	Status    session.Status
	ExpiresAt time.Time
}

// Resolve returns the public information behind a link.
func (s *Service) Resolve(ctx context.Context, token string) (Info, session.Session, error) {
	sess, label, expiresAt, err := s.find(ctx, token)
	if err != nil {
		return Info{}, session.Session{}, err
	}
	return Info{Label: label, Channel: sess.Channel, Status: sess.Status, ExpiresAt: expiresAt}, sess, nil
}

// Watch follows the QR/connection events of the session behind a link. If the
// session was unlinked, opening the stream starts a new pairing.
func (s *Service) Watch(ctx context.Context, token string) (session.Session, <-chan channel.Event, func(), error) {
	sess, _, _, err := s.find(ctx, token)
	if err != nil {
		return session.Session{}, nil, nil, err
	}
	return s.sessions.Watch(ctx, sess.TenantID, sess.ID)
}

func (s *Service) find(ctx context.Context, token string) (session.Session, string, time.Time, error) {
	if !strings.HasPrefix(token, tokenPrefix) {
		return session.Session{}, "", time.Time{}, ErrInvalidLink
	}
	sessionID, label, expiresAt, err := s.repo.FindByTokenHash(ctx, hashToken(token), s.now())
	if err != nil {
		return session.Session{}, "", time.Time{}, err
	}
	sess, err := s.sessions.Lookup(ctx, sessionID)
	if errors.Is(err, session.ErrNotFound) {
		return session.Session{}, "", time.Time{}, ErrInvalidLink
	}
	if err != nil {
		return session.Session{}, "", time.Time{}, err
	}
	return sess, label, expiresAt, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
