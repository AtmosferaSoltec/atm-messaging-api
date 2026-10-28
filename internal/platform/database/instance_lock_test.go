package database

import (
	"context"
	"errors"
	"os"
	"testing"
)

// Runs against a real PostgreSQL database (see the message package's integration
// test for how to set TEST_DATABASE_URL).
func TestOnlyOneInstanceHoldsTheLock(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping database tests")
	}
	ctx := context.Background()
	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	release, err := AcquireInstanceLock(ctx, pool)
	if err != nil {
		t.Fatalf("the first instance must get the lock: %v", err)
	}

	// A second "instance" (its own pool, like another process) must be refused.
	other, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	if _, err := AcquireInstanceLock(ctx, other); !errors.Is(err, ErrAnotherInstance) {
		t.Fatalf("a second instance must be refused, got %v", err)
	}

	release()
	releaseAgain, err := AcquireInstanceLock(ctx, other)
	if err != nil {
		t.Fatalf("the lock must be free after a clean release: %v", err)
	}
	releaseAgain()
}
