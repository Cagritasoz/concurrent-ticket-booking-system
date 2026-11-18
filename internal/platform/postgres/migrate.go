package postgres

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source"
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
// Dirty means a migration started and was never marked as finished. Before running a file,
// golang-migrate writes the version the database will be at afterwards, with dirty = true, and
// only after the file succeeds does it set dirty = false. For the up file of migration N that
// version is N; for the down file of migration N it is the version below (N-1, or 0 for
// migration 1). If the file fails or the process is killed, the flag stays true. It is written
// outside the file's transaction, so it survives even though Postgres rolls the file's changes
// back. golang-migrate can't know that (some databases can't roll back DDL), so Up and Down refuse
// to run until a person checks the database and Force clears the flag.
//
// The Migrator never uses the app's pgxpool: cmd/migrate is a separate program that runs before
// the app and opens its own connection. golang-migrate works through database/sql (Go's JDBC), with
// pgx underneath as the driver, so pgx is still the only code that talks to Postgres.
type Migrator struct {
	m      *migrate.Migrate
	driver database.Driver // the same driver m uses, kept to read schema_migrations directly
	source source.Driver   // the same migration files m uses, kept to check versions in Force
}

// NewMigrator connects to the database and prepares the migrations in files, a folder of
// NNNNNN_name.up.sql and NNNNNN_name.down.sql pairs (normally migrations.Files). The caller must
// Close the Migrator.
func NewMigrator(ctx context.Context, databaseURL string, files fs.FS) (*Migrator, error) {
	// create migration source from a Go filesystem
	src, err := iofs.New(files, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	// We open the connection ourselves instead of giving golang-migrate the URL: pgx removes the
	// password from its errors, and the ping gets a time limit.
	connConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		_ = src.Close() // nothing useful to do if closing fails; the parse error matters more
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	// stdlib is pgx's database/sql driver. *sql.DB has a small pool of its own, but golang-migrate
	// takes one connection and keeps it, because the advisory lock belongs to one session.
	db := stdlib.OpenDB(*connConfig)

	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		_ = src.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	// Creates schema_migrations if it doesn't exist yet.
	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		_ = db.Close()
		_ = src.Close()
		return nil, fmt.Errorf("prepare schema_migrations: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		_ = driver.Close() // also closes db
		_ = src.Close()
		return nil, fmt.Errorf("create migrator: %w", err)
	}
	return &Migrator{m: m, driver: driver, source: src}, nil
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
	version, dirty, err := mg.Version()
	if err != nil {
		return err
	}
	// Version 0 and dirty means the down of migration 1 failed. Steps then reports ErrDirty,
	// which is the useful error here, not "nothing to roll back".
	if version == 0 && !dirty {
		return errors.New("migrate down: no migration is applied, nothing to roll back")
	}
	if err := mg.m.Steps(-1); err != nil {
		return fmt.Errorf("migrate down: %w", explain(err))
	}
	return nil
}

// Version returns the current version (0 if no migration is applied) and whether the last
// migration failed halfway.
//
// It reads schema_migrations through the driver, not through mg.m.Version: that one turns
// golang-migrate's "no version" (-1) into ErrNilVersion and drops the dirty flag with it, so a
// failed down of migration 1 would look like a clean version 0, which is misleading.
func (mg *Migrator) Version() (version uint, dirty bool, err error) {
	v, dirty, err := mg.driver.Version()
	if err != nil {
		return 0, false, fmt.Errorf("read version: %w", err)
	}
	// -1 is golang-migrate's "no version": an empty schema_migrations, or the dirty -1 row a failed
	// down of migration 1 leaves. Our versions are uint, so it is reported as 0.
	if v == database.NilVersion {
		return 0, dirty, nil
	}
	return uint(v), dirty, nil
}

// Force sets the version and clears the dirty flag without running any SQL. It is the way out
// after a failed migration, once the database has been fixed by hand (like Flyway's repair).
//
// Version 0 means "no migration applied". golang-migrate writes that as -1, its "no version", so
// Force turns 0 into -1 before passing it on: golang-migrate would read a 0 as "migration 0 is
// applied". Version does the opposite and reports -1 as 0.
//
// A version other than 0 must have a migration file. golang-migrate's own Force accepts any
// number, and the next Up or Down would then fail with "no migration found".
func (mg *Migrator) Force(version uint) error {
	if version != 0 {
		if err := mg.requireMigration(version); err != nil {
			return fmt.Errorf("force version %d: %w", version, err)
		}
	}
	v := int(version)
	if version == 0 {
		v = -1 // golang-migrate's "no version"; its version 0 would mean a migration numbered 0
	}
	if err := mg.m.Force(v); err != nil {
		return fmt.Errorf("force version %d: %w", version, err)
	}
	return nil
}

// requireMigration returns an error if no migration has this version. It walks the versions in
// order with First and Next, which only read the list of file names, not the files.
func (mg *Migrator) requireMigration(version uint) error {
	var latest uint
	found := false // whether there is any migration at all
	v, err := mg.source.First()
	for err == nil {
		if v == version {
			return nil
		}
		latest, found = v, true
		v, err = mg.source.Next(v)
	}
	if !errors.Is(err, fs.ErrNotExist) { // ErrNotExist only means there is no next version
		return fmt.Errorf("read migrations: %w", err)
	}
	if !found {
		return errors.New("there are no migrations, so 0 (no migration applied) is the only valid version")
	}
	return fmt.Errorf("no migration has version %d; the latest is %d, and 0 means no migration applied",
		version, latest)
}

// Close closes the database connection.
func (mg *Migrator) Close() error {
	sourceErr, dbErr := mg.m.Close()
	return errors.Join(sourceErr, dbErr)
}

// explain adds what to do next to golang-migrate's ErrDirty, whose own message is only "Dirty
// database version N. Fix and force version." It uses errors.As, not errors.Is, because ErrDirty
// is a struct type that carries the version, not one fixed value like ErrNoChange: errors.As finds
// an ErrDirty anywhere in err's chain and copies it into dirty. The original error is wrapped with
// %w, so its text stays at the end and callers can still find it with errors.As. Any other error is
// returned unchanged.
//
// Go has two common kinds of errors. Any type with an Error() string method is an error (Go has
// no "implements": having the method is enough), so a struct like ErrDirty can carry data such as
// the version. A fixed error that carries nothing is a package variable made once with
// errors.New, like ErrNoChange, and is compared with errors.Is.
//
// ErrDirty comes on the run after a failure: the failing run itself returns the SQL error, and the
// next Up or Down finds the flag still set.
//
// The message doesn't name the migration that failed, because the dirty version doesn't say it:
// after a failed up it is the failed migration, after a failed down the one below it (see
// Migrator). golang-migrate writes "no version" as -1; the message shows it as 0, like Version.
func explain(err error) error {
	var dirty migrate.ErrDirty
	if errors.As(err, &dirty) {
		return fmt.Errorf("the database is marked dirty at version %d because an earlier migration "+
			"failed halfway (a failed up leaves the version of the migration that failed, a failed "+
			"down the version below it): check the database by hand, then force the version that "+
			"is really applied (make migrate-force version=N): %w", max(dirty.Version, 0), err)
	}
	return err
}
