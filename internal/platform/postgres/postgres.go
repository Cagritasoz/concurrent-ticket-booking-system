// Package postgres sets up the connection to PostgreSQL.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// connectTimeout limits how long startup waits for Postgres to answer.
const connectTimeout = 5 * time.Second

// NewPool creates a connection pool and checks that Postgres can be reached. The caller owns the
// pool and must Close it.
//
// pgxpool plays the role of HikariCP in Spring, but nothing creates it automatically: this function
// replaces Spring Boot's DataSource auto-configuration. The pool uses pgx's default settings; they
// can be changed in the URL without code, e.g. ?pool_max_conns=10.
//
// pgx removes the password from its error messages, so these errors are safe to log.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	// The pool connects lazily, on the first query. Ping now, so a wrong password or an unreachable
	// server stops the app at startup instead of failing the first request.
	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}
