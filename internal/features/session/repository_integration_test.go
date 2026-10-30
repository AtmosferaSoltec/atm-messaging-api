package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtmosferaSoltec/atm-messaging-api/db"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/database"
)

// Runs against a real PostgreSQL database (see the message package's integration
// test for how to set TEST_DATABASE_URL). The connection lifecycle only happens
// for real after a phone scans a QR, so this is where its SQL gets exercised.
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping database tests")
	}
	ctx := context.Background()
	if err := database.Migrate(ctx, url, db.Migrations, database.MigrateUp, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestSessionRepositoryAgainstPostgres(t *testing.T) {
	pool := integrationPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	tenantID, accountID := uuid.New(), uuid.New()
	for _, sql := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO tenants (id, name, system_code, webhook_secret) VALUES ($1, 'itest', $2, 's')`, []any{tenantID, "itest-" + tenantID.String()}},
		{`INSERT INTO accounts (id, tenant_id, external_ref) VALUES ($1, $2, 'itest-account')`, []any{accountID, tenantID}},
	} {
		if _, err := pool.Exec(ctx, sql.query, sql.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM sessions WHERE tenant_id = $1`, tenantID)
		_, _ = pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID)
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	sess := Session{ID: uuid.New(), TenantID: tenantID, AccountID: accountID, Channel: channel.WhatsApp, Status: StatusPendingQR}
	if err := repo.Insert(ctx, sess); err != nil {
		t.Fatal(err)
	}
	dup := sess
	dup.ID = uuid.New()
	if err := repo.Insert(ctx, dup); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("one session per account and channel is enforced by the database, got %v", err)
	}

	// Pairing, then the first connection: the warm-up start is set once and never moves.
	ref := "51936416623:7@s.whatsapp.net"
	first := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := repo.UpdateState(ctx, sess.ID, StateUpdate{Status: StatusPendingQR, ChannelRef: &ref}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateState(ctx, sess.ID, StateUpdate{Status: StatusConnected, ConnectedAt: &first}); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Truncate(time.Second)
	if err := repo.UpdateState(ctx, sess.ID, StateUpdate{Status: StatusConnected, ConnectedAt: &later}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.FindByID(ctx, tenantID, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusConnected || got.ChannelRef != ref || got.AccountRef != "itest-account" {
		t.Fatalf("unexpected session %+v", got)
	}
	if got.ConnectedAt == nil || !got.ConnectedAt.Equal(later) {
		t.Errorf("connected_at follows the latest connection, got %v", got.ConnectedAt)
	}
	if got.FirstConnectedAt == nil || !got.FirstConnectedAt.Equal(first) {
		t.Errorf("first_connected_at (warm-up start) must keep the first connection, got %v", got.FirstConnectedAt)
	}

	// A ban survives the disconnect events that follow it.
	if err := repo.UpdateState(ctx, sess.ID, StateUpdate{Status: StatusBanned}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateState(ctx, sess.ID, StateUpdate{Status: StatusDisconnected}); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.FindByID(ctx, tenantID, sess.ID)
	if got.Status != StatusBanned {
		t.Errorf("a banned session must stay banned after a disconnect, got %s", got.Status)
	}

	// Logging out clears the credentials reference.
	empty := ""
	if err := repo.UpdateState(ctx, sess.ID, StateUpdate{Status: StatusDisconnected, ChannelRef: &empty}); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.FindByID(ctx, tenantID, sess.ID)
	if got.ChannelRef != "" {
		t.Errorf("logout must clear channel_ref, got %q", got.ChannelRef)
	}

	// Admin listing and counters.
	list, err := repo.ListFiltered(ctx, Filter{TenantID: &tenantID, Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %d %v", len(list), err)
	}
	counts, err := repo.CountByStatus(ctx, &tenantID)
	if err != nil || counts[StatusBanned] != 1 {
		t.Fatalf("counts: %v %v", counts, err)
	}
	if restorable, err := repo.ListRestorable(ctx); err != nil {
		t.Fatal(err)
	} else {
		for _, r := range restorable {
			if r.ID == sess.ID {
				t.Error("a banned or logged-out session must not be restored on startup")
			}
		}
	}
}
