// Command api is the messaging service. Subcommands:
//
//	api serve                      start the HTTP server (default)
//	api migrate up|down|status     manage database migrations
//	api tenant create -name NAME   register a tenant and print its API key once
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/joho/godotenv"

	// Embeds the time zone database, so LIMITS_TIMEZONE works in minimal images.
	_ "time/tzdata"
)

func main() {
	// A missing .env is normal in production, where variables come from the environment.
	_ = godotenv.Load()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	command, args := "serve", []string{}
	if len(os.Args) > 1 {
		command, args = os.Args[1], os.Args[2:]
	}

	var err error
	switch command {
	case "serve":
		err = serve(ctx, log)
	case "migrate":
		err = migrate(ctx, log, args)
	case "tenant":
		err = tenantCommand(ctx, args)
	default:
		err = fmt.Errorf("unknown command %q (use serve, migrate or tenant)", command)
	}
	if err != nil {
		log.Error("command failed", "command", command, "error", err)
		os.Exit(1)
	}
}
