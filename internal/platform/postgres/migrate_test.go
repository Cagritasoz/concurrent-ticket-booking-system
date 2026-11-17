// These tests are in package postgres_test, outside package postgres, because they use pgtest,
// and pgtest will use package postgres in M1 step 3. Inside package postgres that would be an
// import cycle, which Go doesn't allow.
package postgres_test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres/pgtest"
)

const secretPassword = "s3cret-migrate-password"

// testMigrations is a small set of migrations kept in memory, so these tests check the Migrator
// itself, independent of the real migrations (those are tested in package migrations).
func testMigrations() fstest.MapFS {
	return fstest.MapFS{
		"000001_create_a.up.sql":   {Data: []byte("CREATE TABLE a (id int);")},
		"000001_create_a.down.sql": {Data: []byte("DROP TABLE a;")},
		"000002_create_b.up.sql":   {Data: []byte("CREATE TABLE b (id int);")},
		"000002_create_b.down.sql": {Data: []byte("DROP TABLE b;")},
	}
}

func newMigrator(t *testing.T, databaseURL string, files fstest.MapFS) *postgres.Migrator {
	t.Helper()
	migrator, err := postgres.NewMigrator(t.Context(), databaseURL, files)
	if err != nil {
		t.Fatalf("NewMigrator: %v", err)
	}
	t.Cleanup(func() {
		if err := migrator.Close(); err != nil {
			t.Errorf("close migrator: %v", err)
		}
	})
	return migrator
}

func requireVersion(t *testing.T, migrator *postgres.Migrator, wantVersion uint, wantDirty bool) {
	t.Helper()
	version, dirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != wantVersion || dirty != wantDirty {
		t.Fatalf("version %d, dirty %t; want version %d, dirty %t", version, dirty, wantVersion, wantDirty)
	}
}

// tableExists reports whether a table with this name exists in the database.
func tableExists(t *testing.T, databaseURL, table string) bool {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	var exists bool
	if err := conn.QueryRow(t.Context(), "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	return exists
}

func TestNewMigrator_InvalidURL(t *testing.T) {
	_, err := postgres.NewMigrator(t.Context(), "postgres://user:"+secretPassword+"@localhost:not-a-port/db", testMigrations())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if strings.Contains(err.Error(), secretPassword) {
		t.Errorf("error contains the password: %v", err)
	}
}

func TestNewMigrator_UnreachableServer(t *testing.T) {
	// Open a port to get a free number, then close it, so nothing is listening there.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	_, err = postgres.NewMigrator(t.Context(), "postgres://user:"+secretPassword+"@"+addr+"/db?sslmode=disable", testMigrations())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if strings.Contains(err.Error(), secretPassword) {
		t.Errorf("error contains the password: %v", err)
	}
}

func TestMigrator_UpAndDown(t *testing.T) {
	databaseURL := pgtest.NewDatabase(t)
	migrator := newMigrator(t, databaseURL, testMigrations())

	requireVersion(t, migrator, 0, false)

	if err := migrator.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	requireVersion(t, migrator, 2, false)
	if !tableExists(t, databaseURL, "a") || !tableExists(t, databaseURL, "b") {
		t.Fatal("tables a and b should exist after Up")
	}

	// Running Up again with nothing new is not an error.
	if err := migrator.Up(); err != nil {
		t.Fatalf("second Up: %v", err)
	}

	// Down rolls back one migration at a time.
	if err := migrator.Down(); err != nil {
		t.Fatalf("Down: %v", err)
	}
	requireVersion(t, migrator, 1, false)
	if !tableExists(t, databaseURL, "a") || tableExists(t, databaseURL, "b") {
		t.Fatal("after one Down, a should exist and b should not")
	}

	if err := migrator.Down(); err != nil {
		t.Fatalf("second Down: %v", err)
	}
	requireVersion(t, migrator, 0, false)

	if err := migrator.Down(); err == nil {
		t.Fatal("Down with no migration applied: expected an error, got nil")
	}
}

// A migration that fails partway is rolled back as a whole, but the database is marked dirty and
// every further migration is refused until the version is forced.
func TestMigrator_FailedMigrationMarksTheDatabaseDirty(t *testing.T) {
	databaseURL := pgtest.NewDatabase(t)
	files := testMigrations()
	// The first statement is fine, the second one has a typo.
	files["000002_create_b.up.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE b (id int);\nSELEC 1;")}
	migrator := newMigrator(t, databaseURL, files)

	if err := migrator.Up(); err == nil {
		t.Fatal("Up with a broken migration: expected an error, got nil")
	}
	requireVersion(t, migrator, 2, true)

	// The whole file ran as one transaction, so its first statement was rolled back too.
	if tableExists(t, databaseURL, "b") {
		t.Error("table b exists, but the failed migration should have been rolled back as a whole")
	}

	err := migrator.Up()
	if err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("Up on a dirty database: got %v, want an error that says dirty", err)
	}

	// Migration 2 left nothing behind, so version 1 is what is really applied.
	if err := migrator.Force(1); err != nil {
		t.Fatalf("Force: %v", err)
	}
	requireVersion(t, migrator, 1, false)
}

// When the very first migration fails, nothing is applied, and Force(0) must bring the database
// back to "no version" so Up starts again at migration 1.
func TestMigrator_FailedFirstMigrationIsForcedToZero(t *testing.T) {
	databaseURL := pgtest.NewDatabase(t)
	files := testMigrations()
	files["000001_create_a.up.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE a (id int);\nSELEC 1;")}
	migrator := newMigrator(t, databaseURL, files)

	if err := migrator.Up(); err == nil {
		t.Fatal("Up with a broken first migration: expected an error, got nil")
	}
	requireVersion(t, migrator, 1, true)
	if tableExists(t, databaseURL, "a") {
		t.Error("table a exists, but the failed migration should have been rolled back as a whole")
	}

	if err := migrator.Force(0); err != nil {
		t.Fatalf("Force: %v", err)
	}
	requireVersion(t, migrator, 0, false)

	// Fix the file, as a person would, and run Up again with a new Migrator.
	files["000001_create_a.up.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE a (id int);")}
	fixed := newMigrator(t, databaseURL, files)
	if err := fixed.Up(); err != nil {
		t.Fatalf("Up after the fix: %v", err)
	}
	requireVersion(t, fixed, 2, false)
}

// Force only accepts 0 or a version that has a migration, and a rejected version changes nothing.
func TestMigrator_ForceRejectsAVersionWithoutAMigration(t *testing.T) {
	databaseURL := pgtest.NewDatabase(t)
	files := testMigrations()
	// A gap: versions 1, 2 and 5, so the check can't just compare against the latest version.
	files["000005_create_c.up.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE c (id int);")}
	files["000005_create_c.down.sql"] = &fstest.MapFile{Data: []byte("DROP TABLE c;")}
	migrator := newMigrator(t, databaseURL, files)

	if err := migrator.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	for _, version := range []uint{3, 4, 6, 1000} {
		err := migrator.Force(version)
		want := fmt.Sprintf("no migration has version %d; the latest is 5", version)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Force(%d): got %v, want an error that says %q", version, err, want)
		}
	}
	requireVersion(t, migrator, 5, false) // the rejected versions changed nothing

	for _, version := range []uint{2, 0} {
		if err := migrator.Force(version); err != nil {
			t.Fatalf("Force(%d): %v", version, err)
		}
		requireVersion(t, migrator, version, false)
	}
}

func TestMigrator_ForceWithoutMigrationsOnlyAcceptsZero(t *testing.T) {
	databaseURL := pgtest.NewDatabase(t)
	migrator := newMigrator(t, databaseURL, fstest.MapFS{})

	err := migrator.Force(1)
	if err == nil || !strings.Contains(err.Error(), "there are no migrations") {
		t.Fatalf("Force(1): got %v, want an error that says there are no migrations", err)
	}
	if err := migrator.Force(0); err != nil {
		t.Fatalf("Force(0): %v", err)
	}
	requireVersion(t, migrator, 0, false)
}

// A failed down is marked dirty at the version below the failed migration, while the rollback
// itself was undone, so the failed migration is still applied.
func TestMigrator_FailedDownMarksTheVersionBelowDirty(t *testing.T) {
	databaseURL := pgtest.NewDatabase(t)
	files := testMigrations()
	files["000002_create_b.down.sql"] = &fstest.MapFile{Data: []byte("DROP TABLE b;\nSELEC 1;")}
	migrator := newMigrator(t, databaseURL, files)

	if err := migrator.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := migrator.Down(); err == nil {
		t.Fatal("Down with a broken down file: expected an error, got nil")
	}
	requireVersion(t, migrator, 1, true)

	// DROP TABLE b was rolled back with the rest of the file, so migration 2 is still applied.
	if !tableExists(t, databaseURL, "b") {
		t.Error("table b is gone, but the failed down should have been rolled back as a whole")
	}

	err := migrator.Up()
	if err == nil || !strings.Contains(err.Error(), "dirty at version 1") {
		t.Fatalf("Up on a dirty database: got %v, want an error that says dirty at version 1", err)
	}

	if err := migrator.Force(2); err != nil {
		t.Fatalf("Force: %v", err)
	}
	requireVersion(t, migrator, 2, false)
}

// A failed down of migration 1 is marked dirty with golang-migrate's "no version". Version must
// still report it as dirty, and Down must say dirty, not "nothing to roll back".
func TestMigrator_FailedDownOfTheFirstMigrationStaysDirty(t *testing.T) {
	databaseURL := pgtest.NewDatabase(t)
	files := testMigrations()
	files["000001_create_a.down.sql"] = &fstest.MapFile{Data: []byte("DROP TABLE a;\nSELEC 1;")}
	migrator := newMigrator(t, databaseURL, files)

	if err := migrator.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := migrator.Down(); err != nil { // migration 2's down file is fine
		t.Fatalf("first Down: %v", err)
	}
	if err := migrator.Down(); err == nil {
		t.Fatal("Down with a broken down file: expected an error, got nil")
	}
	requireVersion(t, migrator, 0, true)

	if !tableExists(t, databaseURL, "a") {
		t.Error("table a is gone, but the failed down should have been rolled back as a whole")
	}

	err := migrator.Down()
	if err == nil || !strings.Contains(err.Error(), "dirty at version 0") {
		t.Fatalf("Down on a dirty database: got %v, want an error that says dirty at version 0", err)
	}

	if err := migrator.Force(1); err != nil {
		t.Fatalf("Force: %v", err)
	}
	requireVersion(t, migrator, 1, false)
}
