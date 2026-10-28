package tenant

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/db"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/database"
)

// Runs against a real PostgreSQL database (see the message package's integration
// test for how to set TEST_DATABASE_URL).
func TestTenantNamesAreUniqueInPostgres(t *testing.T) {
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
	// Cleanups run last-in first-out: the delete below must run before the pool closes.
	t.Cleanup(pool.Close)
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE name ILIKE 'itest-unique%'`) })

	service := NewService(NewRepository(pool))
	first, err := service.Create(ctx, "itest-unique Name")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"itest-unique Name", "ITEST-UNIQUE name", "  itest-unique name "} {
		if _, err := service.Create(ctx, name); err != ErrNameTaken {
			t.Errorf("%q: the database must reject a duplicate, got %v", name, err)
		}
	}

	other, err := service.Create(ctx, "itest-unique Other")
	if err != nil {
		t.Fatal(err)
	}
	taken := "ITEST-UNIQUE NAME"
	if _, err := service.Update(ctx, other.Tenant.ID, &taken, nil); err != ErrNameTaken {
		t.Errorf("renaming onto an existing name must fail, got %v", err)
	}
	if _, err := service.Update(ctx, first.Tenant.ID, &taken, nil); err != nil {
		t.Errorf("a tenant may change the case of its own name: %v", err)
	}
	_ = uuid.Nil
}
