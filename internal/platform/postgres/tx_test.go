package postgres_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres/pgtest"
)

// newPool returns a pool on a fresh, empty database that runs schema first.
func newPool(t *testing.T, schema string) *pgxpool.Pool {
	t.Helper()
	pool, err := postgres.NewPool(t.Context(), pgtest.NewDatabase(t))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	// Registered after pgtest's cleanup, so it runs before it: the pool closes, then the
	// database is dropped.
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return pool
}

// countRows returns how many rows the table items has, read outside any transaction.
func countRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM items").Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

const itemsTable = "CREATE TABLE items (name text PRIMARY KEY)"

func TestWithTx_CommitsWhenFnReturnsNil(t *testing.T) {
	pool := newPool(t, itemsTable)

	err := postgres.WithTx(t.Context(), pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), "INSERT INTO items VALUES ('a'), ('b')")
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	if got := countRows(t, pool); got != 2 {
		t.Errorf("rows after commit: got %d, want 2", got)
	}
}

func TestWithTx_RollsBackAndReturnsFnsError(t *testing.T) {
	pool := newPool(t, itemsTable)
	errStop := errors.New("stop")

	err := postgres.WithTx(t.Context(), pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), "INSERT INTO items VALUES ('a')"); err != nil {
			return err
		}
		return errStop // the insert above worked, but the whole transaction must be undone
	})
	// The same message too: WithTx adds nothing to fn's error, the caller knows best what it means.
	if !errors.Is(err, errStop) || err.Error() != errStop.Error() {
		t.Fatalf("WithTx: got %v, want fn's error unchanged", err)
	}
	if got := countRows(t, pool); got != 0 {
		t.Errorf("rows after rollback: got %d, want 0", got)
	}
}

func TestWithTx_RollsBackAndPassesOnAPanic(t *testing.T) {
	pool := newPool(t, itemsTable)

	// recover only works in a deferred function, so the call that panics runs in its own
	// function, and recovered is the value the panic carried.
	recovered := func() (recovered any) {
		defer func() { recovered = recover() }()
		_ = postgres.WithTx(t.Context(), pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), "INSERT INTO items VALUES ('a')"); err != nil {
				return err
			}
			panic("boom")
		})
		return nil
	}()

	if recovered != "boom" {
		t.Fatalf("recovered %v, want the panic to reach the caller", recovered)
	}
	if got := countRows(t, pool); got != 0 {
		t.Errorf("rows after the panic: got %d, want 0", got)
	}
	// The connection went back to the pool instead of leaking.
	if got := pool.Stat().AcquiredConns(); got != 0 {
		t.Errorf("connections still in use: got %d, want 0", got)
	}
}

func TestWithTx_ReturnsACommitError(t *testing.T) {
	// A DEFERRABLE INITIALLY DEFERRED constraint is only checked at COMMIT, so here the inserts
	// succeed and the commit is what fails.
	pool := newPool(t, `CREATE TABLE items (
		name text CONSTRAINT items_name_key UNIQUE DEFERRABLE INITIALLY DEFERRED
	)`)

	err := postgres.WithTx(t.Context(), pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), "INSERT INTO items VALUES ('a'), ('a')")
		return err
	})
	if !postgres.IsUniqueViolation(err, "items_name_key") {
		t.Fatalf("WithTx: got %v, want the unique violation from COMMIT", err)
	}
	if got := countRows(t, pool); got != 0 {
		t.Errorf("rows after the failed commit: got %d, want 0", got)
	}
}
