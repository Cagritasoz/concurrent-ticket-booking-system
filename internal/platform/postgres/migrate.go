package postgres

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Migrator applies SQL migrations to a database, in version order. It plays the role of Flyway.
//
// Versions are the migration numbers: 0 means no migration is applied. The current version, and
// whether a migration failed halfway ("dirty"), are stored in the schema_migrations table, like
// Flyway's flyway_schema_history. A Postgres advisory lock makes sure only one Migrator changes a
// database at a time.
//
// The Migrator never uses the app's pgxpool: cmd/migrate is a separate program that runs before
// the app and opens its own connection. golang-migrate works through database/sql (Go's JDBC), with
// pgx underneath as the driver, so pgx is still the only code that talks to Postgres.
type Migrator struct {
	m *migrate.Migrate
}

// NewMigrator connects to the database and prepares the migrations in files, a folder of
// NNNNNN_name.up.sql and NNNNNN_name.down.sql pairs (normally migrations.Files). The caller must
// Close the Migrator.
func NewMigrator(ctx context.Context, databaseURL string, files fs.FS) (*Migrator, error) {
	// create migration source from a Go filesystem
	source, err := iofs.New(files, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	// We open the connection ourselves instead of giving golang-migrate the URL: pgx removes the
	// password from its errors, and the ping gets a time limit.
	connConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		_ = source.Close() // nothing useful to do if closing fails; the parse error matters more
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	// stdlib is pgx's database/sql driver. *sql.DB has a small pool of its own, but golang-migrate
	// takes one connection and keeps it, because the advisory lock belongs to one session.
	db := stdlib.OpenDB(*connConfig)

	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		_ = source.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	// Creates schema_migrations if it doesn't exist yet.
	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		_ = db.Close()
		_ = source.Close()
		return nil, fmt.Errorf("prepare schema_migrations: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		_ = driver.Close() // also closes db
		_ = source.Close()
		return nil, fmt.Errorf("create migrator: %w", err)
	}
	return &Migrator{m: m}, nil
}

// Up applies every migration that hasn't been applied yet. Nothing to apply is not an error.
//
// Each migration file runs as one transaction, so a failing statement undoes the whole file.
// golang-migrate takes no context: Ctrl+C ends the process, Postgres rolls back the migration that
// was running, and the database is left marked dirty (see Force).
func (mg *Migrator) Up() error {
	if err := mg.m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", explain(err))
	}
	return nil
}

// Down rolls back the last applied migration only, never all of them at once.
func (mg *Migrator) Down() error {
	version, _, err := mg.Version()
	if err != nil {
		return err
	}
	if version == 0 {
		return errors.New("migrate down: no migration is applied, nothing to roll back")
	}
	if err := mg.m.Steps(-1); err != nil {
		return fmt.Errorf("migrate down: %w", explain(err))
	}
	return nil
}

// Version returns the current version (0 if no migration is applied) and whether the last
// migration failed halfway.
func (mg *Migrator) Version() (version uint, dirty bool, err error) {
	version, dirty, err = mg.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read version: %w", err)
	}
	return version, dirty, nil
}

// Force sets the version and clears the dirty flag without running any SQL. It is the way out
// after a failed migration, once the database has been fixed by hand (like Flyway's repair).
// Version 0 means "no migration applied".
func (mg *Migrator) Force(version uint) error {
	v := int(version)
	if version == 0 {
		v = -1 // golang-migrate's "no version"; its version 0 would mean a migration numbered 0
	}
	if err := mg.m.Force(v); err != nil {
		return fmt.Errorf("force version %d: %w", version, err)
	}
	return nil
}

// Close closes the database connection.
func (mg *Migrator) Close() error {
	sourceErr, dbErr := mg.m.Close()
	return errors.Join(sourceErr, dbErr)
}

// explain adds what to do next to golang-migrate's "dirty" error.
func explain(err error) error {
	var dirty migrate.ErrDirty
	if errors.As(err, &dirty) {
		return fmt.Errorf("migration %d failed halfway and the database is marked dirty: "+
			"check and fix the database by hand, then force the version that is really applied "+
			"(make migrate-force version=N): %w", dirty.Version, err)
	}
	return err
}
