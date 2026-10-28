package message

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when no message matches.
var ErrNotFound = errors.New("message not found")

// Repository stores messages and is also the persistent queue the dispatcher
// works from. Lookups for API clients are always scoped to a tenant.
type Repository interface {
	// Insert stores the message with its attachment bytes. If the tenant already
	// used the idempotency key, it returns the existing message and created=false.
	Insert(ctx context.Context, m Message, attachment []byte) (stored Message, created bool, err error)
	FindByID(ctx context.Context, tenantID, id uuid.UUID) (Message, error)

	// PeekNext returns the next queued message of a session that is due, without
	// claiming it: transactional first, then oldest first. When none is due,
	// nextDue holds when the earliest queued message becomes due, if any.
	PeekNext(ctx context.Context, sessionID uuid.UUID) (head Message, found bool, nextDue *time.Time, err error)
	// MarkSending claims a queued message and returns its attachment bytes.
	MarkSending(ctx context.Context, id uuid.UUID) ([]byte, error)
	// Requeue puts a message back in the queue to be tried again at dueAt.
	Requeue(ctx context.Context, id uuid.UUID, dueAt time.Time, attempts int) error
	// Finish records a final state (sent or failed) and discards the attachment bytes.
	Finish(ctx context.Context, id uuid.UUID, update StatusUpdate) error
	// CountSent counts the messages of a priority sent by a session since a time.
	CountSent(ctx context.Context, sessionID uuid.UUID, priority Priority, since time.Time) (int, error)

	// ApplyReceipt advances the status of the session's messages with the given
	// provider IDs to delivered or read, never moving a message backwards. It
	// returns the messages that changed.
	ApplyReceipt(ctx context.Context, sessionID uuid.UUID, providerIDs []string, status Status, at time.Time) ([]Message, error)

	// ListFiltered, CountByStatusSince and QueueDepth serve the administration panel.
	ListFiltered(ctx context.Context, filter Filter) ([]Message, error)
	CountByStatusSince(ctx context.Context, tenantID *uuid.UUID, since time.Time) (map[Status]int, error)
	QueueDepth(ctx context.Context, tenantID *uuid.UUID) (int, error)
	// HourlyActivity groups messages created since a time by hour. Hours without
	// messages are absent; "sent" counts every message that left the queue successfully.
	HourlyActivity(ctx context.Context, tenantID *uuid.UUID, since time.Time) ([]HourlyCount, error)

	// RecoverSending requeues messages left in "sending" by a crash or shutdown.
	RecoverSending(ctx context.Context) (int, error)
	// SessionsWithQueued lists the sessions that have queued messages.
	SessionsWithQueued(ctx context.Context) ([]uuid.UUID, error)
}

// Filter narrows an administrative message listing. Zero values mean "any".
type Filter struct {
	TenantID  *uuid.UUID
	SessionID *uuid.UUID
	Status    Status
	Priority  Priority
	// Recipient matches messages whose destination contains these digits.
	Recipient string
	Limit     int
	Offset    int
}

// HourlyCount is how many messages were sent or failed during one hour.
type HourlyCount struct {
	Hour   time.Time
	Sent   int
	Failed int
}

// StatusUpdate is the final state of a message.
type StatusUpdate struct {
	Status            Status
	Error             string
	ProviderMessageID string
	SentAt            *time.Time
	Attempts          int
}

type pgRepository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a PostgreSQL-backed Repository.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

const selectMessage = `
	SELECT id, tenant_id, session_id, channel, recipient, type, body, attachment_filename,
	       attachment_mime, attachment_size, priority, COALESCE(idempotency_key, ''), status,
	       attempts, error, provider_message_id, sent_at, delivered_at, read_at, created_at
	FROM messages`

func scanMessage(row pgx.Row) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.TenantID, &m.SessionID, &m.Channel, &m.Recipient, &m.Kind, &m.Body,
		&m.AttachmentFilename, &m.AttachmentMime, &m.AttachmentSize, &m.Priority, &m.IdempotencyKey,
		&m.Status, &m.Attempts, &m.Error, &m.ProviderMessageID, &m.SentAt, &m.DeliveredAt, &m.ReadAt, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("scan message: %w", err)
	}
	return m, nil
}

func (r *pgRepository) Insert(ctx context.Context, m Message, attachment []byte) (Message, bool, error) {
	var key *string
	if m.IdempotencyKey != "" {
		key = &m.IdempotencyKey
	}

	tag, err := r.pool.Exec(ctx, `
		INSERT INTO messages (id, tenant_id, session_id, channel, recipient, type, body,
			attachment_filename, attachment_mime, attachment_size, attachment_data, priority,
			idempotency_key, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING`,
		m.ID, m.TenantID, m.SessionID, m.Channel, m.Recipient, m.Kind, m.Body,
		m.AttachmentFilename, m.AttachmentMime, m.AttachmentSize, attachment, m.Priority, key, m.Status)
	if err != nil {
		return Message{}, false, fmt.Errorf("insert message: %w", err)
	}

	if tag.RowsAffected() == 0 {
		existing, err := scanMessage(r.pool.QueryRow(ctx,
			selectMessage+` WHERE tenant_id = $1 AND idempotency_key = $2`, m.TenantID, m.IdempotencyKey))
		if err != nil {
			return Message{}, false, err
		}
		return existing, false, nil
	}

	stored, err := scanMessage(r.pool.QueryRow(ctx, selectMessage+` WHERE id = $1`, m.ID))
	if err != nil {
		return Message{}, false, err
	}
	return stored, true, nil
}

func (r *pgRepository) FindByID(ctx context.Context, tenantID, id uuid.UUID) (Message, error) {
	return scanMessage(r.pool.QueryRow(ctx, selectMessage+` WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *pgRepository) PeekNext(ctx context.Context, sessionID uuid.UUID) (Message, bool, *time.Time, error) {
	head, err := scanMessage(r.pool.QueryRow(ctx, selectMessage+`
		WHERE session_id = $1 AND status = 'queued' AND next_attempt_at <= now()
		ORDER BY (priority = 'promotional'), created_at
		LIMIT 1`, sessionID))
	if err == nil {
		return head, true, nil, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Message{}, false, nil, err
	}

	var nextDue *time.Time
	if err := r.pool.QueryRow(ctx,
		`SELECT min(next_attempt_at) FROM messages WHERE session_id = $1 AND status = 'queued'`, sessionID).
		Scan(&nextDue); err != nil {
		return Message{}, false, nil, fmt.Errorf("find next due message: %w", err)
	}
	return Message{}, false, nextDue, nil
}

func (r *pgRepository) MarkSending(ctx context.Context, id uuid.UUID) ([]byte, error) {
	var attachment []byte
	err := r.pool.QueryRow(ctx, `
		UPDATE messages SET status = 'sending', updated_at = now()
		WHERE id = $1 AND status = 'queued'
		RETURNING attachment_data`, id).Scan(&attachment)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mark message sending: %w", err)
	}
	return attachment, nil
}

func (r *pgRepository) Requeue(ctx context.Context, id uuid.UUID, dueAt time.Time, attempts int) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE messages SET status = 'queued', next_attempt_at = $2, attempts = $3, updated_at = now()
		WHERE id = $1`, id, dueAt, attempts)
	if err != nil {
		return fmt.Errorf("requeue message: %w", err)
	}
	return nil
}

func (r *pgRepository) Finish(ctx context.Context, id uuid.UUID, u StatusUpdate) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE messages SET status = $2, error = $3, provider_message_id = $4, sent_at = $5,
			attempts = $6, attachment_data = NULL, updated_at = now()
		WHERE id = $1`, id, u.Status, u.Error, u.ProviderMessageID, u.SentAt, u.Attempts)
	if err != nil {
		return fmt.Errorf("finish message: %w", err)
	}
	return nil
}

func (r *pgRepository) CountSent(ctx context.Context, sessionID uuid.UUID, priority Priority, since time.Time) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM messages
		WHERE session_id = $1 AND priority = $2 AND sent_at >= $3`, sessionID, priority, since).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count sent messages: %w", err)
	}
	return count, nil
}

func (r *pgRepository) RecoverSending(ctx context.Context) (int, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE messages SET status = 'queued', updated_at = now() WHERE status = 'sending'`)
	if err != nil {
		return 0, fmt.Errorf("recover sending messages: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *pgRepository) SessionsWithQueued(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT session_id FROM messages WHERE status = 'queued'`)
	if err != nil {
		return nil, fmt.Errorf("list sessions with queued messages: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan session id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read session ids: %w", err)
	}
	return ids, nil
}

// statusRankSQL orders the statuses a receipt can move a message through.
const statusRankSQL = `CASE status WHEN 'sent' THEN 1 WHEN 'delivered' THEN 2 WHEN 'read' THEN 3 ELSE 0 END`

func (r *pgRepository) ApplyReceipt(ctx context.Context, sessionID uuid.UUID, providerIDs []string, status Status, at time.Time) ([]Message, error) {
	targetRank := 2
	if status == StatusRead {
		targetRank = 3
	}

	// A read receipt also implies delivery, so delivered_at is filled when missing.
	rows, err := r.pool.Query(ctx, `
		UPDATE messages SET
			status = $3::varchar,
			delivered_at = COALESCE(delivered_at, $4),
			read_at = CASE WHEN $3::text = 'read' THEN COALESCE(read_at, $4) ELSE read_at END,
			updated_at = now()
		WHERE session_id = $1 AND provider_message_id = ANY($2)
		  AND `+statusRankSQL+` BETWEEN 1 AND $5 - 1
		RETURNING id`, sessionID, providerIDs, status, at, targetRank)
	if err != nil {
		return nil, fmt.Errorf("apply receipt: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan receipt id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read receipt ids: %w", err)
	}

	updated := make([]Message, 0, len(ids))
	for _, id := range ids {
		m, err := scanMessage(r.pool.QueryRow(ctx, selectMessage+` WHERE id = $1`, id))
		if err != nil {
			return nil, err
		}
		updated = append(updated, m)
	}
	return updated, nil
}

func (r *pgRepository) ListFiltered(ctx context.Context, f Filter) ([]Message, error) {
	rows, err := r.pool.Query(ctx, selectMessage+` WHERE ($1::uuid IS NULL OR tenant_id = $1)
		AND ($2::uuid IS NULL OR session_id = $2) AND ($3 = '' OR status = $3)
		AND ($4 = '' OR priority = $4) AND ($5 = '' OR position($5 in recipient) > 0)
		ORDER BY created_at DESC LIMIT $6 OFFSET $7`,
		f.TenantID, f.SessionID, string(f.Status), string(f.Priority), f.Recipient, f.Limit, f.Offset)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()

	messages := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read messages: %w", err)
	}
	return messages, nil
}

func (r *pgRepository) CountByStatusSince(ctx context.Context, tenantID *uuid.UUID, since time.Time) (map[Status]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT status, count(*) FROM messages
		WHERE created_at >= $2 AND ($1::uuid IS NULL OR tenant_id = $1) GROUP BY status`, tenantID, since)
	if err != nil {
		return nil, fmt.Errorf("count messages: %w", err)
	}
	defer rows.Close()

	counts := map[Status]int{}
	for rows.Next() {
		var status Status
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan message count: %w", err)
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (r *pgRepository) QueueDepth(ctx context.Context, tenantID *uuid.UUID) (int, error) {
	var depth int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM messages
		WHERE status IN ('queued', 'sending') AND ($1::uuid IS NULL OR tenant_id = $1)`, tenantID).Scan(&depth)
	if err != nil {
		return 0, fmt.Errorf("count queue depth: %w", err)
	}
	return depth, nil
}

func (r *pgRepository) HourlyActivity(ctx context.Context, tenantID *uuid.UUID, since time.Time) ([]HourlyCount, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT date_trunc('hour', created_at) AS hour,
		       count(*) FILTER (WHERE status IN ('sent', 'delivered', 'read')),
		       count(*) FILTER (WHERE status = 'failed')
		FROM messages
		WHERE created_at >= $2 AND ($1::uuid IS NULL OR tenant_id = $1)
		GROUP BY 1 ORDER BY 1`, tenantID, since)
	if err != nil {
		return nil, fmt.Errorf("hourly activity: %w", err)
	}
	defer rows.Close()

	var counts []HourlyCount
	for rows.Next() {
		var c HourlyCount
		if err := rows.Scan(&c.Hour, &c.Sent, &c.Failed); err != nil {
			return nil, fmt.Errorf("scan hourly activity: %w", err)
		}
		counts = append(counts, c)
	}
	return counts, rows.Err()
}
