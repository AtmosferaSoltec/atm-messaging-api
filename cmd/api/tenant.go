package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/config"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/tenant"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/database"
)

func tenantCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("usage: api tenant create -name NAME")
	}

	flags := flag.NewFlagSet("tenant create", flag.ContinueOnError)
	name := flags.String("name", "", "tenant name (e.g. the system that will consume the API)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	created, err := tenant.NewService(tenant.NewRepository(pool)).Create(ctx, *name)
	if err != nil {
		return err
	}

	fmt.Printf("Tenant created: %s (%s)\n", created.Tenant.Name, created.Tenant.ID)
	fmt.Printf("Webhook secret: %s\n", created.Tenant.WebhookSecret)
	fmt.Printf("API key (shown only once, store it now): %s\n", created.APIKey)
	return nil
}
