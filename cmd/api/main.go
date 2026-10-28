// Command api is the messaging service. Subcommands:
//
//	api serve                      start the HTTP server (default)
//	api migrate up|down|status     manage database migrations
//	api tenant create -name NAME   register a tenant and print its API key once
//	api healthcheck                used by the image's HEALTHCHECK (the image has no curl)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	// Embeds the time zone database, so LIMITS_TIMEZONE works in minimal images.
	_ "time/tzdata"
)

func main() {
	// A missing .env is normal in production, where variables come from the environment.
	_ = godotenv.Load()

	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}

	// Docker stops containers with SIGTERM: handling it lets the server drain
	// requests, finish the send in progress, close WhatsApp and release its lock.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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

// healthcheck asks the local server for /health and returns the process exit code.
func healthcheck() int {
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "8080"
	}
	client := http.Client{Timeout: 3 * time.Second}
	res, err := client.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		return 1
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
