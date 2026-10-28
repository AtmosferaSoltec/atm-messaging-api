// Package optout keeps the per-session list of recipients that unsubscribed from
// promotional messages. Promotional messages are never sent to them.
package optout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/shared/phone"
)

// Errors returned by the Service.
var (
	ErrInvalidRecipient = phone.ErrInvalid
	ErrSessionNotFound  = errors.New("session not found")
)

// Entry is a recipient that opted out.
type Entry struct {
	Recipient string
	CreatedAt time.Time
}

// SessionOwnership checks that a session belongs to a tenant. It returns
// ErrSessionNotFound otherwise.
type SessionOwnership interface {
	Owns(ctx context.Context, tenantID, sessionID uuid.UUID) error
}

// Repository stores opt-outs.
type Repository interface {
	Add(ctx context.Context, sessionID uuid.UUID, recipient string) error
	Remove(ctx context.Context, sessionID uuid.UUID, recipient string) error
	Exists(ctx context.Context, sessionID uuid.UUID, recipient string) (bool, error)
	List(ctx context.Context, sessionID uuid.UUID) ([]Entry, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a PostgreSQL-backed Repository.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) Add(ctx context.Context, sessionID uuid.UUID, recipient string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO opt_outs (session_id, recipient) VALUES ($1, $2) ON CONFLICT DO NOTHING`, sessionID, recipient)
	if err != nil {
		return fmt.Errorf("add opt-out: %w", err)
	}
	return nil
}

func (r *pgRepository) Remove(ctx context.Context, sessionID uuid.UUID, recipient string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM opt_outs WHERE session_id = $1 AND recipient = $2`, sessionID, recipient)
	if err != nil {
		return fmt.Errorf("remove opt-out: %w", err)
	}
	return nil
}

func (r *pgRepository) Exists(ctx context.Context, sessionID uuid.UUID, recipient string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM opt_outs WHERE session_id = $1 AND recipient = $2)`, sessionID, recipient).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check opt-out: %w", err)
	}
	return exists, nil
}

func (r *pgRepository) List(ctx context.Context, sessionID uuid.UUID) ([]Entry, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT recipient, created_at FROM opt_outs WHERE session_id = $1 ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list opt-outs: %w", err)
	}
	defer rows.Close()

	entries := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Recipient, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan opt-out: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read opt-outs: %w", err)
	}
	return entries, nil
}

// Service holds the opt-out business rules.
type Service struct {
	repo     Repository
	sessions SessionOwnership
}

// NewService creates a Service.
func NewService(repo Repository, sessions SessionOwnership) *Service {
	return &Service{repo: repo, sessions: sessions}
}

// Add unsubscribes a recipient from the promotional messages of a session.
func (s *Service) Add(ctx context.Context, tenantID, sessionID uuid.UUID, recipient string) error {
	normalized, err := s.authorize(ctx, tenantID, sessionID, recipient)
	if err != nil {
		return err
	}
	return s.repo.Add(ctx, sessionID, normalized)
}

// Remove subscribes a recipient again.
func (s *Service) Remove(ctx context.Context, tenantID, sessionID uuid.UUID, recipient string) error {
	normalized, err := s.authorize(ctx, tenantID, sessionID, recipient)
	if err != nil {
		return err
	}
	return s.repo.Remove(ctx, sessionID, normalized)
}

// List returns the recipients that opted out of a session.
func (s *Service) List(ctx context.Context, tenantID, sessionID uuid.UUID) ([]Entry, error) {
	if err := s.sessions.Owns(ctx, tenantID, sessionID); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, sessionID)
}

// IsOptedOut reports whether a recipient unsubscribed. The session ID is trusted
// (internal use by the message feature); the recipient is normalized here.
func (s *Service) IsOptedOut(ctx context.Context, sessionID uuid.UUID, recipient string) (bool, error) {
	normalized, err := phone.Normalize(recipient)
	if err != nil {
		return false, nil
	}
	return s.repo.Exists(ctx, sessionID, normalized)
}

func (s *Service) authorize(ctx context.Context, tenantID, sessionID uuid.UUID, recipient string) (string, error) {
	if err := s.sessions.Owns(ctx, tenantID, sessionID); err != nil {
		return "", err
	}
	return phone.Normalize(recipient)
}
