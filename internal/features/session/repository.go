package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
)

// Errors returned by the Repository.
var (
	ErrNotFound      = errors.New("session not found")
	ErrAlreadyExists = errors.New("account already has a session for this channel")
)

const uniqueViolation = "23505"

// Repository stores sessions. Methods taking a tenant ID are always scoped to it.
type Repository interface {
	Insert(ctx context.Context, s Session) error
	FindByID(ctx context.Context, tenantID, id uuid.UUID) (Session, error)
	FindByAccountChannel(ctx context.Context, tenantID uuid.UUID, accountRef string, ch channel.Type) (Session, error)
	List(ctx context.Context, tenantID uuid.UUID, accountRef string) ([]Session, error)
	Delete(ctx context.Context, tenantID, id uuid.UUID) error

	// ListFiltered and CountByStatus serve the administration panel across tenants.
	ListFiltered(ctx context.Context, filter Filter) ([]Session, error)
	CountByStatus(ctx context.Context, tenantID *uuid.UUID) (map[Status]int, error)

	// The methods below are used by the event handler and startup, which have no tenant in scope.
	FindByIDUnscoped(ctx context.Context, id uuid.UUID) (Session, error)
	UpdateState(ctx context.Context, id uuid.UUID, update StateUpdate) error
	ListRestorable(ctx context.Context) ([]Session, error)
}

// Filter narrows an administrative session listing. Zero values mean "any".
type Filter struct {
	TenantID   *uuid.UUID
	Status     Status
	AccountRef string
	Limit      int
	Offset     int
}

// StateUpdate changes the connection state of a session. A nil ChannelRef keeps
// the stored one; a pointer to "" clears it.
type StateUpdate struct {
	Status      Status
	ChannelRef  *string
	ConnectedAt *time.Time
}

type pgRepository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a PostgreSQL-backed Repository.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

const selectSession = `
	SELECT s.id, s.tenant_id, s.account_id, a.external_ref, s.channel, s.status,
	       s.channel_ref, s.connected_at, s.first_connected_at, s.created_at, s.updated_at
	FROM sessions s JOIN accounts a ON a.id = s.account_id`

func scanSession(row pgx.Row) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.TenantID, &s.AccountID, &s.AccountRef, &s.Channel, &s.Status,
		&s.ChannelRef, &s.ConnectedAt, &s.FirstConnectedAt, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("scan session: %w", err)
	}
	return s, nil
}

func (r *pgRepository) Insert(ctx context.Context, s Session) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO sessions (id, tenant_id, account_id, channel, status)
		VALUES ($1, $2, $3, $4, $5)`,
		s.ID, s.TenantID, s.AccountID, s.Channel, s.Status)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return ErrAlreadyExists
	}
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (r *pgRepository) FindByID(ctx context.Context, tenantID, id uuid.UUID) (Session, error) {
	return scanSession(r.pool.QueryRow(ctx, selectSession+` WHERE s.tenant_id = $1 AND s.id = $2`, tenantID, id))
}

func (r *pgRepository) FindByIDUnscoped(ctx context.Context, id uuid.UUID) (Session, error) {
	return scanSession(r.pool.QueryRow(ctx, selectSession+` WHERE s.id = $1`, id))
}

func (r *pgRepository) FindByAccountChannel(ctx context.Context, tenantID uuid.UUID, accountRef string, ch channel.Type) (Session, error) {
	return scanSession(r.pool.QueryRow(ctx,
		selectSession+` WHERE s.tenant_id = $1 AND a.external_ref = $2 AND s.channel = $3`,
		tenantID, accountRef, ch))
}

func (r *pgRepository) List(ctx context.Context, tenantID uuid.UUID, accountRef string) ([]Session, error) {
	query := selectSession + ` WHERE s.tenant_id = $1`
	args := []any{tenantID}
	if accountRef != "" {
		query += ` AND a.external_ref = $2`
		args = append(args, accountRef)
	}
	return r.collect(ctx, query+` ORDER BY s.created_at`, args...)
}

func (r *pgRepository) ListRestorable(ctx context.Context) ([]Session, error) {
	return r.collect(ctx, selectSession+` WHERE s.channel_ref <> '' AND s.status <> 'banned' ORDER BY s.created_at`)
}

func (r *pgRepository) collect(ctx context.Context, query string, args ...any) ([]Session, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	sessions := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}
	return sessions, nil
}

func (r *pgRepository) UpdateState(ctx context.Context, id uuid.UUID, u StateUpdate) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE sessions SET
			-- A banned session stays banned until it is explicitly paired again.
			status       = CASE WHEN status = 'banned' AND $2 = 'disconnected' THEN status ELSE $2 END,
			channel_ref  = COALESCE($3, channel_ref),
			connected_at = COALESCE($4, connected_at),
			first_connected_at = COALESCE(first_connected_at, $4),
			updated_at   = now()
		WHERE id = $1`, id, u.Status, u.ChannelRef, u.ConnectedAt)
	if err != nil {
		return fmt.Errorf("update session state: %w", err)
	}
	return nil
}

func (r *pgRepository) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM sessions WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *pgRepository) ListFiltered(ctx context.Context, f Filter) ([]Session, error) {
	query := selectSession + ` WHERE ($1::uuid IS NULL OR s.tenant_id = $1)
		AND ($2 = '' OR s.status = $2) AND ($3 = '' OR a.external_ref = $3)
		ORDER BY s.created_at DESC LIMIT $4 OFFSET $5`
	return r.collect(ctx, query, f.TenantID, string(f.Status), f.AccountRef, f.Limit, f.Offset)
}

func (r *pgRepository) CountByStatus(ctx context.Context, tenantID *uuid.UUID) (map[Status]int, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT status, count(*) FROM sessions WHERE ($1::uuid IS NULL OR tenant_id = $1) GROUP BY status`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("count sessions: %w", err)
	}
	defer rows.Close()

	counts := map[Status]int{}
	for rows.Next() {
		var status Status
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan session count: %w", err)
		}
		counts[status] = count
	}
	return counts, rows.Err()
}
