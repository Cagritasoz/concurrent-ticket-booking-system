package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WithTx runs fn inside one database transaction. If fn returns nil the transaction is committed;
// if fn returns an error or panics it is rolled back, and the error (or panic) is passed on
// unchanged. It plays the role of Spring's TransactionTemplate, or of an @Transactional method,
// but explicit: everything that must be in the transaction uses the tx that fn receives.
//
//	err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
//		q := eventdb.New(tx) // the generated queries, running in this transaction
//		...
//	})
//
// The transaction uses Postgres's default isolation level, READ COMMITTED, like Spring's default
// with Postgres. Rows that must not change underneath fn are locked with SELECT ... FOR UPDATE.
// Both the pgxpool.Pool and pgx.Tx have functions Exec, Query and QueryRow
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	// Runs on every way out of this function, a panic included. After a successful Commit,
	// Rollback does nothing and returns pgx.ErrTxClosed, so its error is ignored. If ctx is
	// already canceled, pgx closes the connection instead, which also rolls back.
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err // unchanged, so callers can still check it with errors.Is and errors.As
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
