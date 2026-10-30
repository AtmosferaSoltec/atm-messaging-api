package message

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtmosferaSoltec/atm-messaging-api/db"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/database"
	"io"
	"log/slog"
)

// These tests run against a real PostgreSQL database, because fakes cannot catch
// SQL mistakes (a missing column once broke every message query). Set
// TEST_DATABASE_URL to the messaging_test database to run them:
//
//	TEST_DATABASE_URL='postgresql://messaging_app:...@localhost:5432/messaging_test?search_path=messaging' go test ./internal/features/message
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping database tests")
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(ctx, url, db.Migrations, database.MigrateUp, log); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedSession creates a tenant, account and session, and removes them afterwards.
func seedSession(t *testing.T, pool *pgxpool.Pool) (tenantID, sessionID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tenantID, accountID, sessionID := uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO tenants (id, name, system_code, webhook_secret) VALUES ($1, 'itest', $2, 's')`, tenantID, "itest-"+tenantID.String())
	exec(`INSERT INTO accounts (id, tenant_id, external_ref) VALUES ($1, $2, 'itest')`, accountID, tenantID)
	exec(`INSERT INTO sessions (id, tenant_id, account_id, channel, status) VALUES ($1, $2, $3, 'whatsapp', 'connected')`, sessionID, tenantID, accountID)
	t.Cleanup(func() {
		exec(`DELETE FROM sessions WHERE id = $1`, sessionID) // cascades to messages
		exec(`DELETE FROM accounts WHERE id = $1`, accountID)
		exec(`DELETE FROM tenants WHERE id = $1`, tenantID)
	})
	return tenantID, sessionID
}

func newMessage(tenantID, sessionID uuid.UUID, priority Priority, key string) Message {
	return Message{
		ID: uuid.New(), TenantID: tenantID, SessionID: sessionID, Channel: channel.WhatsApp,
		Recipient: "51936416623", Kind: channel.KindText, Body: "hello", Priority: priority,
		IdempotencyKey: key, Status: StatusQueued,
	}
}

func TestMessageRepositoryAgainstPostgres(t *testing.T) {
	pool := integrationPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	tenantID, sessionID := seedSession(t, pool)

	// Insert, idempotency and lookup.
	first := newMessage(tenantID, sessionID, PriorityTransactional, "key-1")
	stored, created, err := repo.Insert(ctx, first, []byte("%PDF-bytes"))
	if err != nil || !created || stored.ID != first.ID || stored.Status != StatusQueued {
		t.Fatalf("insert: %+v created=%v err=%v", stored, created, err)
	}
	again, created, err := repo.Insert(ctx, newMessage(tenantID, sessionID, PriorityTransactional, "key-1"), nil)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("the same idempotency key must return the original: %+v created=%v err=%v", again, created, err)
	}
	if got, err := repo.FindByID(ctx, tenantID, first.ID); err != nil || got.ID != first.ID {
		t.Fatalf("find: %+v %v", got, err)
	}
	if _, err := repo.FindByID(ctx, uuid.New(), first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant must not see the message, got %v", err)
	}

	if got, err := repo.FindByIDUnscoped(ctx, first.ID); err != nil || got.ID != first.ID {
		t.Fatalf("unscoped find (admin): %+v %v", got, err)
	}

	// Video and audio are valid message types in the database too.
	for _, kind := range []channel.MessageKind{channel.KindVideo, channel.KindAudio} {
		media := newMessage(tenantID, sessionID, PriorityTransactional, "")
		media.Kind = kind
		if _, _, err := repo.Insert(ctx, media, []byte("bytes")); err != nil {
			t.Fatalf("%s messages must be accepted by the schema: %v", kind, err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM messages WHERE id = $1`, media.ID); err != nil {
			t.Fatal(err)
		}
	}

	// Queue order: transactional before promotional, even if older.
	promo := newMessage(tenantID, sessionID, PriorityPromotional, "")
	if _, _, err := repo.Insert(ctx, promo, nil); err != nil {
		t.Fatal(err)
	}
	head, found, _, err := repo.PeekNext(ctx, sessionID)
	if err != nil || !found || head.ID != first.ID {
		t.Fatalf("peek must return the transactional message first: %+v found=%v err=%v", head, found, err)
	}

	// Claim, requeue with a future due time, then settle.
	attachment, err := repo.MarkSending(ctx, first.ID)
	if err != nil || string(attachment) != "%PDF-bytes" {
		t.Fatalf("mark sending must return the stored attachment: %q %v", attachment, err)
	}
	if _, err := repo.MarkSending(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a message can be claimed once, got %v", err)
	}
	if err := repo.Requeue(ctx, first.ID, time.Now().Add(time.Hour), 1); err != nil {
		t.Fatal(err)
	}
	head, _, _, _ = repo.PeekNext(ctx, sessionID)
	if head.ID != promo.ID {
		t.Fatal("a message due in the future must not be peeked")
	}

	now := time.Now()
	if err := repo.Finish(ctx, first.ID, StatusUpdate{Status: StatusSent, ProviderMessageID: "WA-1", SentAt: &now, Attempts: 2}); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.CountSent(ctx, sessionID, PriorityTransactional, now.Add(-time.Minute)); err != nil || n != 1 {
		t.Fatalf("count sent: %d %v", n, err)
	}

	// Receipts move a message forward and never back.
	updated, err := repo.ApplyReceipt(ctx, sessionID, []string{"WA-1"}, StatusRead, now)
	if err != nil || len(updated) != 1 || updated[0].Status != StatusRead || updated[0].ReadAt == nil || updated[0].DeliveredAt == nil {
		t.Fatalf("read receipt: %+v %v", updated, err)
	}
	updated, err = repo.ApplyReceipt(ctx, sessionID, []string{"WA-1"}, StatusDelivered, now)
	if err != nil || len(updated) != 0 {
		t.Fatalf("a late delivered receipt must not move a read message back: %+v %v", updated, err)
	}

	// Admin listing, filters and statistics.
	rows, err := repo.ListFiltered(ctx, Filter{TenantID: &tenantID, Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("list: %d %v", len(rows), err)
	}
	rows, _ = repo.ListFiltered(ctx, Filter{TenantID: &tenantID, Priority: PriorityPromotional, Limit: 10})
	if len(rows) != 1 || rows[0].ID != promo.ID {
		t.Fatalf("priority filter: %+v", rows)
	}
	rows, _ = repo.ListFiltered(ctx, Filter{TenantID: &tenantID, Recipient: "93641", Limit: 10})
	if len(rows) != 2 {
		t.Fatalf("recipient search should match by digits: %d", len(rows))
	}
	rows, _ = repo.ListFiltered(ctx, Filter{TenantID: &tenantID, Recipient: "999", Limit: 10})
	if len(rows) != 0 {
		t.Fatalf("recipient search must exclude non-matches: %d", len(rows))
	}
	counts, err := repo.CountByStatusSince(ctx, &tenantID, now.Add(-time.Hour))
	if err != nil || counts[StatusRead] != 1 || counts[StatusQueued] != 1 {
		t.Fatalf("counts: %v %v", counts, err)
	}
	if depth, err := repo.QueueDepth(ctx, &tenantID); err != nil || depth != 1 {
		t.Fatalf("queue depth: %d %v", depth, err)
	}
	hourly, err := repo.HourlyActivity(ctx, &tenantID, now.Add(-time.Hour))
	if err != nil || len(hourly) == 0 {
		t.Fatalf("hourly activity: %v %v", hourly, err)
	}
	total := 0
	for _, h := range hourly {
		total += h.Sent
	}
	if total != 1 {
		t.Fatalf("exactly one message was delivered/read: %+v", hourly)
	}

	// Recovery after a crash.
	other := newMessage(tenantID, sessionID, PriorityTransactional, "")
	_, _, _ = repo.Insert(ctx, other, nil)
	_, _ = repo.MarkSending(ctx, other.ID)
	if n, err := repo.RecoverSending(ctx); err != nil || n < 1 {
		t.Fatalf("recover sending: %d %v", n, err)
	}
	ids, err := repo.SessionsWithQueued(ctx)
	if err != nil || len(ids) == 0 {
		t.Fatalf("sessions with queued messages: %v %v", ids, err)
	}
}
