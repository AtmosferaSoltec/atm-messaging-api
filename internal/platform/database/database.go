// Package database opens the PostgreSQL pool and applies migrations.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	// pgx driver for database/sql, used by goose and whatsmeow.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Migration commands accepted by Migrate.
const (
	MigrateUp     = "up"
	MigrateDown   = "down"
	MigrateStatus = "status"
)

const statementTimeout = "10s"

// Open creates the connection pool. The schema (search_path) travels in the URL
// (?search_path=messaging) and is also fixed on the role.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// err is not wrapped: its text could include the URL with the password.
		return nil, fmt.Errorf("parse database url: invalid format")
	}
	cfg.MaxConns = 10
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = statementTimeout
	cfg.ConnConfig.RuntimeParams["application_name"] = "atm-messaging-api"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrate applies (up), reverts the last (down) or lists (status) the
// migrations in migrations. The goose_db_version table lives in the role's schema.
func Migrate(ctx context.Context, databaseURL string, migrations fs.FS, command string, log *slog.Logger) error {
	conn, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database for migrations: invalid url")
	}
	defer func() { _ = conn.Close() }()

	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}

	switch command {
	case MigrateUp:
		err = goose.UpContext(ctx, conn, "migrations")
	case MigrateDown:
		err = goose.DownContext(ctx, conn, "migrations")
	case MigrateStatus:
		err = goose.StatusContext(ctx, conn, "migrations")
	default:
		return fmt.Errorf("unknown migrate command %q (use up, down or status)", command)
	}
	if err != nil {
		return fmt.Errorf("migrate %s: %w", command, err)
	}
	log.Info("migrations done", "command", command)
	return nil
}
