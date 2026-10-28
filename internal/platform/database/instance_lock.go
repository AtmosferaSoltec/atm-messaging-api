package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// instanceLockID is the PostgreSQL advisory lock held by the running server.
const instanceLockID = 7_272_002

// ErrAnotherInstance is returned when another server already runs on this database.
var ErrAnotherInstance = errors.New("another instance of the service is already running on this database")

// AcquireInstanceLock makes sure only one server runs per database. A WhatsApp
// device can be connected from a single place at a time, so two servers would
// keep kicking each other off. The lock belongs to one connection that is kept
// for the life of the process, so PostgreSQL releases it if the process dies.
// The returned function releases it on a clean shutdown.
func AcquireInstanceLock(ctx context.Context, pool *pgxpool.Pool) (func(), error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire instance lock connection: %w", err)
	}

	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", instanceLockID).Scan(&acquired); err != nil {
		conn.Release()
		return nil, fmt.Errorf("take instance lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, ErrAnotherInstance
	}

	return func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", instanceLockID)
		conn.Release()
	}, nil
}
