package webhook

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository is the webhook outbox.
type Repository interface {
	Insert(ctx context.Context, e Event) error
	// PeekDue returns the oldest pending event of a tenant that is due. When none
	// is due, nextDue holds when the earliest pending one will be.
	PeekDue(ctx context.Context, tenantID uuid.UUID) (event Event, found bool, nextDue *time.Time, err error)
	MarkDelivered(ctx context.Context, id uuid.UUID, attempts int) error
	Reschedule(ctx context.Context, id uuid.UUID, dueAt time.Time, attempts int, reason string) error
	MarkFailed(ctx context.Context, id uuid.UUID, attempts int, reason string) error
	ListRecent(ctx context.Context, tenantID uuid.UUID, limit int) ([]Event, error)
	TenantsWithPending(ctx context.Context) ([]uuid.UUID, error)
	CountByStatus(ctx context.Context, tenantID *uuid.UUID) (map[Status]int, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a PostgreSQL-backed Repository.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

const selectEvent = `
	SELECT id, tenant_id, event_type, payload, status, attempts, next_attempt_at, last_error, created_at, delivered_at
	FROM webhook_events`

func scanEvent(row pgx.Row) (Event, error) {
	var e Event
	err := row.Scan(&e.ID, &e.TenantID, &e.Type, &e.Payload, &e.Status, &e.Attempts, &e.NextAttemptAt,
		&e.LastError, &e.CreatedAt, &e.DeliveredAt)
	if err != nil {
		return Event{}, err
	}
	return e, nil
}

func (r *pgRepository) Insert(ctx context.Context, e Event) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO webhook_events (id, tenant_id, event_type, payload) VALUES ($1, $2, $3, $4)`,
		e.ID, e.TenantID, e.Type, e.Payload)
	if err != nil {
		return fmt.Errorf("insert webhook event: %w", err)
	}
	return nil
}

func (r *pgRepository) PeekDue(ctx context.Context, tenantID uuid.UUID) (Event, bool, *time.Time, error) {
	e, err := scanEvent(r.pool.QueryRow(ctx, selectEvent+`
		WHERE tenant_id = $1 AND status = 'pending' AND next_attempt_at <= now()
		ORDER BY created_at LIMIT 1`, tenantID))
	if err == nil {
		return e, true, nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Event{}, false, nil, fmt.Errorf("peek webhook event: %w", err)
	}

	var nextDue *time.Time
	if err := r.pool.QueryRow(ctx,
		`SELECT min(next_attempt_at) FROM webhook_events WHERE tenant_id = $1 AND status = 'pending'`, tenantID).
		Scan(&nextDue); err != nil {
		return Event{}, false, nil, fmt.Errorf("find next webhook event: %w", err)
	}
	return Event{}, false, nextDue, nil
}

func (r *pgRepository) MarkDelivered(ctx context.Context, id uuid.UUID, attempts int) error {
	return r.exec(ctx, `UPDATE webhook_events SET status = 'delivered', attempts = $2, last_error = '', delivered_at = now() WHERE id = $1`, id, attempts)
}

func (r *pgRepository) Reschedule(ctx context.Context, id uuid.UUID, dueAt time.Time, attempts int, reason string) error {
	return r.exec(ctx, `UPDATE webhook_events SET next_attempt_at = $2, attempts = $3, last_error = $4 WHERE id = $1`, id, dueAt, attempts, reason)
}

func (r *pgRepository) MarkFailed(ctx context.Context, id uuid.UUID, attempts int, reason string) error {
	return r.exec(ctx, `UPDATE webhook_events SET status = 'failed', attempts = $2, last_error = $3 WHERE id = $1`, id, attempts, reason)
}

func (r *pgRepository) exec(ctx context.Context, query string, args ...any) error {
	if _, err := r.pool.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("update webhook event: %w", err)
	}
	return nil
}

func (r *pgRepository) ListRecent(ctx context.Context, tenantID uuid.UUID, limit int) ([]Event, error) {
	rows, err := r.pool.Query(ctx, selectEvent+` WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list webhook events: %w", err)
	}
	defer rows.Close()

	events := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan webhook event: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read webhook events: %w", err)
	}
	return events, nil
}

func (r *pgRepository) TenantsWithPending(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT tenant_id FROM webhook_events WHERE status = 'pending'`)
	if err != nil {
		return nil, fmt.Errorf("list tenants with pending webhooks: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan tenant id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *pgRepository) CountByStatus(ctx context.Context, tenantID *uuid.UUID) (map[Status]int, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT status, count(*) FROM webhook_events WHERE ($1::uuid IS NULL OR tenant_id = $1) GROUP BY status`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("count webhook events: %w", err)
	}
	defer rows.Close()

	counts := map[Status]int{}
	for rows.Next() {
		var status Status
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan webhook count: %w", err)
		}
		counts[status] = count
	}
	return counts, rows.Err()
}
