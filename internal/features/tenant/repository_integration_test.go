package tenant

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/AtmosferaSoltec/atm-messaging-api/db"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/database"
)

// Runs against a real PostgreSQL database (see the message package's integration
// test for how to set TEST_DATABASE_URL).
func TestProvisionAgainstPostgres(t *testing.T) {
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
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE system_code LIKE 'itest-unique%'`) })

	service := NewService(NewRepository(pool))
	first, err := service.Provision(ctx, "itest-unique")
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.Provision(ctx, "itest-unique")
	if err != nil || again.ID != first.ID {
		t.Fatalf("provisioning twice must return the same tenant: %v / %v", again, err)
	}
	if _, err := service.Authenticate(ctx, "itest-unique"); err != nil {
		t.Fatalf("a provisioned tenant authenticates: %v", err)
	}

	other, err := service.Provision(ctx, "itest-unique-other")
	if err != nil {
		t.Fatal(err)
	}
	taken := "ITEST-UNIQUE"
	if _, err := service.Update(ctx, other.ID, &taken, nil); !errors.Is(err, ErrNameTaken) {
		t.Errorf("renaming onto an existing name must fail, got %v", err)
	}
	if _, err := service.Update(ctx, first.ID, &taken, nil); err != nil {
		t.Errorf("a tenant may change the case of its own name: %v", err)
	}
}
