package email

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository is the persistence of emails and of the delivery queue.
type Repository interface {
	// Insert stores a new queued email. If the idempotency key already exists
	// for the tenant it returns the original email and created=false.
	Insert(ctx context.Context, e Email) (stored Email, created bool, err error)
	FindByKey(ctx context.Context, tenantID uuid.UUID, key string) (Email, bool, error)
	Get(ctx context.Context, tenantID, id uuid.UUID) (Email, error)
	CountToAddressSince(ctx context.Context, tenantID uuid.UUID, address string, since time.Time) (int, error)

	// Claim takes one due email (queued, or sending with an expired lease),
	// marks it sending, counts the attempt and leases it for lease.
	Claim(ctx context.Context, lease time.Duration) (Email, bool, error)
	MarkSent(ctx context.Context, id uuid.UUID) error
	Reschedule(ctx context.Context, id uuid.UUID, next time.Time, reason string) error
	MarkFailed(ctx context.Context, id uuid.UUID, reason string) error

	CountByTenantSince(ctx context.Context, since time.Time) ([]TenantCounts, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

// NewRepository creates the PostgreSQL repository.
func NewRepository(pool *pgxpool.Pool) Repository { return &pgRepository{pool: pool} }

const emailColumns = `id, tenant_id, to_address, template, data, coalesce(idempotency_key, ''), status,
	attempts, last_error, next_attempt_at, created_at, sent_at`

func scanEmail(row pgx.Row) (Email, error) {
	var e Email
	var raw []byte
	err := row.Scan(&e.ID, &e.TenantID, &e.To, &e.Template, &raw, &e.IdempotencyKey, &e.Status,
		&e.Attempts, &e.LastError, &e.NextAttemptAt, &e.CreatedAt, &e.SentAt)
	if err != nil {
		return Email{}, err
	}
	if err := unmarshalData(raw, &e.Data); err != nil {
		return Email{}, err
	}
	return e, nil
}

func (r *pgRepository) Insert(ctx context.Context, e Email) (Email, bool, error) {
	raw, err := marshalData(e.Data)
	if err != nil {
		return Email{}, false, err
	}
	var key *string
	if e.IdempotencyKey != "" {
		key = &e.IdempotencyKey
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO email_messages (id, tenant_id, to_address, template, data, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING`,
		e.ID, e.TenantID, e.To, e.Template, raw, key)
	if err != nil {
		return Email{}, false, fmt.Errorf("insert email: %w", err)
	}
	if tag.RowsAffected() == 0 {
		existing, found, err := r.FindByKey(ctx, e.TenantID, e.IdempotencyKey)
		if err != nil {
			return Email{}, false, err
		}
		if !found {
			return Email{}, false, errors.New("insert email: conflict without an existing row")
		}
		return existing, false, nil
	}
	stored, err := r.Get(ctx, e.TenantID, e.ID)
	return stored, true, err
}

func (r *pgRepository) FindByKey(ctx context.Context, tenantID uuid.UUID, key string) (Email, bool, error) {
	e, err := scanEmail(r.pool.QueryRow(ctx,
		`SELECT `+emailColumns+` FROM email_messages WHERE tenant_id = $1 AND idempotency_key = $2`, tenantID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return Email{}, false, nil
	}
	if err != nil {
		return Email{}, false, fmt.Errorf("find email by key: %w", err)
	}
	return e, true, nil
}

func (r *pgRepository) Get(ctx context.Context, tenantID, id uuid.UUID) (Email, error) {
	e, err := scanEmail(r.pool.QueryRow(ctx,
		`SELECT `+emailColumns+` FROM email_messages WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Email{}, ErrNotFound
	}
	if err != nil {
		return Email{}, fmt.Errorf("get email: %w", err)
	}
	return e, nil
}

func (r *pgRepository) CountToAddressSince(ctx context.Context, tenantID uuid.UUID, address string, since time.Time) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM email_messages WHERE tenant_id = $1 AND to_address = $2 AND created_at >= $3`,
		tenantID, address, since).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count emails to address: %w", err)
	}
	return count, nil
}

func (r *pgRepository) Claim(ctx context.Context, lease time.Duration) (Email, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Email{}, false, fmt.Errorf("begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	e, err := scanEmail(tx.QueryRow(ctx, `
		SELECT `+emailColumns+` FROM email_messages
		WHERE status IN ('queued', 'sending') AND next_attempt_at <= now()
		ORDER BY next_attempt_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED`))
	if errors.Is(err, pgx.ErrNoRows) {
		return Email{}, false, nil
	}
	if err != nil {
		return Email{}, false, fmt.Errorf("select due email: %w", err)
	}
	// The lease keeps other workers away while this one sends; if the process dies
	// mid-send the row becomes due again when the lease expires.
	leaseUntil := time.Now().Add(lease)
	if _, err := tx.Exec(ctx, `
		UPDATE email_messages SET status = 'sending', attempts = attempts + 1, next_attempt_at = $2
		WHERE id = $1`, e.ID, leaseUntil); err != nil {
		return Email{}, false, fmt.Errorf("claim email: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Email{}, false, fmt.Errorf("commit claim: %w", err)
	}
	e.Status = StatusSending
	e.Attempts++
	e.NextAttemptAt = leaseUntil
	return e, true, nil
}

// blankData empties the stored variables of password reset codes, so a code
// does not outlive its delivery.
const blankData = `data = CASE WHEN template = 'password_reset_code' THEN '{}'::jsonb ELSE data END`

func (r *pgRepository) MarkSent(ctx context.Context, id uuid.UUID) error {
	return r.exec(ctx, `UPDATE email_messages SET status = 'sent', sent_at = now(), last_error = '', `+blankData+` WHERE id = $1`, id)
}

func (r *pgRepository) Reschedule(ctx context.Context, id uuid.UUID, next time.Time, reason string) error {
	return r.exec(ctx, `UPDATE email_messages SET status = 'queued', next_attempt_at = $2, last_error = $3 WHERE id = $1`,
		id, next, truncate(reason, 500))
}

func (r *pgRepository) MarkFailed(ctx context.Context, id uuid.UUID, reason string) error {
	return r.exec(ctx, `UPDATE email_messages SET status = 'failed', last_error = $2, `+blankData+` WHERE id = $1`,
		id, truncate(reason, 500))
}

func (r *pgRepository) exec(ctx context.Context, sql string, args ...any) error {
	if _, err := r.pool.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("update email: %w", err)
	}
	return nil
}

func (r *pgRepository) CountByTenantSince(ctx context.Context, since time.Time) ([]TenantCounts, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT t.id, coalesce(t.system_code, ''),
		       count(*) FILTER (WHERE e.status = 'queued'),
		       count(*) FILTER (WHERE e.status = 'sending'),
		       count(*) FILTER (WHERE e.status = 'sent'),
		       count(*) FILTER (WHERE e.status = 'failed')
		FROM email_messages e JOIN tenants t ON t.id = e.tenant_id
		WHERE e.created_at >= $1
		GROUP BY t.id, t.system_code
		ORDER BY t.system_code`, since)
	if err != nil {
		return nil, fmt.Errorf("count emails by tenant: %w", err)
	}
	defer rows.Close()
	var out []TenantCounts
	for rows.Next() {
		var c TenantCounts
		if err := rows.Scan(&c.TenantID, &c.SystemCode, &c.Queued, &c.Sending, &c.Sent, &c.Failed); err != nil {
			return nil, fmt.Errorf("scan email count: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
