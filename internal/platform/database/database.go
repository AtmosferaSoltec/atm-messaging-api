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

// migrateLockID is the PostgreSQL advisory lock that serializes migrations, so
// two processes starting together (or parallel tests) never apply the same one twice.
const migrateLockID = 7_272_001

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

	// The lock lives on its own connection for the whole run and is released with it.
	lockConn, err := conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open migration lock connection: %w", err)
	}
	defer func() { _ = lockConn.Close() }()
	if _, err := lockConn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrateLockID); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		_, _ = lockConn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrateLockID)
	}()

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
