package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/AtmosferaSoltec/atm-messaging-api/db"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/config"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/database"
)

func migrate(ctx context.Context, log *slog.Logger, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: api migrate up|down|status")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	return database.Migrate(ctx, cfg.DatabaseURL, db.Migrations, args[0], log)
}
