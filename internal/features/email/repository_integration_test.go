package email

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
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/database"
)

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

func seedTenant(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants (id, name, system_code, webhook_secret) VALUES ($1, 'itest-email', $2, 's')`, id, "itest-"+id.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM email_messages WHERE tenant_id = $1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, id)
	})
	return id
}

func TestEmailRepositoryAgainstPostgres(t *testing.T) {
	pool := integrationPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	tenantID := seedTenant(t, pool)
	// Only this tenant's rows may be due while the test runs.
	if _, err := pool.Exec(ctx, `UPDATE email_messages SET next_attempt_at = now() + interval '1 day' WHERE status = 'queued'`); err != nil {
		t.Fatal(err)
	}

	first := Email{ID: uuid.New(), TenantID: tenantID, To: "ana@example.com", Template: TemplatePasswordResetCode,
		Data: Data{Name: "Ana", Code: "123456", ExpiresMinutes: intPtr(15)}, IdempotencyKey: "k1"}
	stored, created, err := repo.Insert(ctx, first)
	if err != nil || !created || stored.Status != StatusQueued || stored.Data.Code != "123456" {
		t.Fatalf("insert: %+v %v %v", stored, created, err)
	}
	again, created, err := repo.Insert(ctx, Email{ID: uuid.New(), TenantID: tenantID, To: "ana@example.com",
		Template: TemplatePasswordResetCode, Data: first.Data, IdempotencyKey: "k1"})
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("idempotent insert: %+v %v %v", again, created, err)
	}
	if _, err := repo.Get(ctx, uuid.New(), first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant must get not found, got %v", err)
	}
	if n, err := repo.CountToAddressSince(ctx, tenantID, "ana@example.com", time.Now().Add(-time.Hour)); err != nil || n != 1 {
		t.Fatalf("count: %d %v", n, err)
	}

	claimed, found, err := repo.Claim(ctx, time.Minute)
	if err != nil || !found || claimed.ID != first.ID || claimed.Attempts != 1 || claimed.Status != StatusSending {
		t.Fatalf("claim: %+v %v %v", claimed, found, err)
	}
	if _, found, _ := repo.Claim(ctx, time.Minute); found {
		t.Fatal("a leased email must not be claimed again")
	}

	if err := repo.Reschedule(ctx, first.ID, time.Now().Add(-time.Second), "boom"); err != nil {
		t.Fatal(err)
	}
	claimed, found, _ = repo.Claim(ctx, time.Minute)
	if !found || claimed.Attempts != 2 || claimed.LastError != "boom" {
		t.Fatalf("second claim: %+v", claimed)
	}

	if err := repo.MarkSent(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	sent, _ := repo.Get(ctx, tenantID, first.ID)
	if sent.Status != StatusSent || sent.SentAt == nil || sent.Data.Code != "" || sent.Data.Name != "" {
		t.Fatalf("sent reset email must be blanked: %+v", sent)
	}

	// A failed reset email is blanked too; other templates keep their data.
	failing := Email{ID: uuid.New(), TenantID: tenantID, To: "b@example.com", Template: TemplatePasswordResetCode, Data: first.Data}
	if _, _, err := repo.Insert(ctx, failing); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkFailed(ctx, failing.ID, "permanent"); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Get(ctx, tenantID, failing.ID); got.Status != StatusFailed || got.Data.Code != "" || got.LastError != "permanent" {
		t.Fatalf("failed: %+v", got)
	}
	changed := Email{ID: uuid.New(), TenantID: tenantID, To: "c@example.com", Template: TemplatePasswordChanged, Data: Data{Name: "Cris"}}
	if _, _, err := repo.Insert(ctx, changed); err != nil {
		t.Fatal(err)
	}
	_ = repo.MarkSent(ctx, changed.ID)
	if got, _ := repo.Get(ctx, tenantID, changed.ID); got.Data.Name != "Cris" {
		t.Fatalf("password_changed data is kept: %+v", got)
	}

	counts, err := repo.CountByTenantSince(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var mine *TenantCounts
	for i := range counts {
		if counts[i].TenantID == tenantID {
			mine = &counts[i]
		}
	}
	if mine == nil || mine.Sent != 2 || mine.Failed != 1 {
		t.Fatalf("metrics: %+v", mine)
	}
}
