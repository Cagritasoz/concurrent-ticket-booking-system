// Package pgtest gives tests a Postgres database of their own, like Spring's @Testcontainers with
// @DataJpaTest gives each test class a database.
//
// Where the server comes from:
//
//   - If TEST_DATABASE_URL is set, tests use that server. Useful when Docker is slow or missing.
//   - Otherwise the first test that needs Postgres starts a container with Testcontainers. The
//     other tests of the same package (one test binary) share it, and Main removes it at the end.
//
// Either way each test gets its own fresh database on that server, dropped when the test ends, so
// tests never see each other's data. Under go test -short these tests are skipped.
//
// Every test package that uses pgtest needs this TestMain:
//
//	func TestMain(m *testing.M) { pgtest.Main(m) }
package pgtest

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/migrations"
)

const (
	// image is the same Postgres as in docker-compose.yaml, so tests run against what the app uses.
	image = "postgres:17.11-alpine"

	// startTimeout limits starting the container. The first run also downloads the image.
	startTimeout = 3 * time.Minute

	// setupTimeout limits how long creating one test database may take.
	setupTimeout = 10 * time.Second
)

// The shared server of this test binary. Tests may run in parallel (t.Parallel), so the container
// is started inside sync.Once: the first test to get here starts it, any others wait for it.
var (
	mainCalled bool
	startOnce  sync.Once
	started    *tcpostgres.PostgresContainer // nil until a test needed the container
	startedURL string
	startErr   error
)

// Main runs the package's tests and then removes the Postgres container, if one was started. Call
// it from TestMain. When TestMain returns, Go exits with the result of the tests.
//
// If the test process is killed before Main can remove the container, Testcontainers' helper
// container (Ryuk) notices and removes it.
func Main(m *testing.M) {
	mainCalled = true
	m.Run()

	if started != nil {
		if err := testcontainers.TerminateContainer(started); err != nil { // terminate the shared Postgres container.
			// Not a test failure: Ryuk removes the container once this process exits.
			_, _ = fmt.Fprintf(os.Stderr, "pgtest: remove the Postgres container: %v\n", err)
		}
	}
}

// NewDatabase creates an empty database with a random name and returns its URL. The database is
// dropped when the test ends, and the database named in TEST_DATABASE_URL itself is never changed.
func NewDatabase(t *testing.T) string {
	t.Helper()

	// testing.Short reports whether go test ran with -short. It only works once m.Run has parsed
	// the flags, which is why the check is here, during a test, and not in Main.
	if testing.Short() {
		// Skip ends the calling test here and reports it as skipped, not failed, like JUnit's
		// assumeTrue. Every test that needs a database calls NewDatabase, so all of them skip.
		t.Skip("needs Postgres; skipped under -short")
	}
	if !mainCalled {
		t.Fatal("pgtest: this test package needs func TestMain(m *testing.M) { pgtest.Main(m) }")
	}

	serverURL := server(t)
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal("TEST_DATABASE_URL is not a valid URL") // the error would contain the password
	}

	ctx, cancel := context.WithTimeout(t.Context(), setupTimeout)
	defer cancel()

	admin, err := pgx.Connect(ctx, serverURL)
	if err != nil {
		t.Fatalf("connect to the test server: %v", err) // pgx removes the password from errors
	}
	// Cleanups run in reverse order: the database is dropped first, then this connection closes.
	// t.Context() is already canceled when cleanups run, so they use their own context.
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	// rand.Text returns 26 random letters and digits; lowercase keeps the name easy to type in psql.
	name := "test_" + strings.ToLower(rand.Text())
	quoted := pgx.Identifier{name}.Sanitize()

	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		// WITH (FORCE) closes connections the test left open.
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database %s: %v", name, err)
		}
	})

	u.Path = "/" + name
	return u.String()
}

// NewMigratedDatabase is NewDatabase with every migration in migrations/ applied, the starting
// point for tests of code that uses the tables.
func NewMigratedDatabase(t *testing.T) string {
	t.Helper()
	databaseURL := NewDatabase(t)

	migrator, err := postgres.NewMigrator(t.Context(), databaseURL, migrations.Files)
	if err != nil {
		t.Fatalf("prepare migrations: %v", err)
	}
	defer func() { _ = migrator.Close() }()
	if err := migrator.Up(); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return databaseURL
}

// server returns the URL of the server to create test databases on: TEST_DATABASE_URL if it is
// set, otherwise the container, started on the first call.
func server(t *testing.T) string {
	t.Helper()
	if serverURL := os.Getenv("TEST_DATABASE_URL"); serverURL != "" {
		return serverURL
	}
	// runs the function exactly once per process, which is one process per package that contains all the tests.
	startOnce.Do(func() { started, startedURL, startErr = start() })
	if startErr != nil {
		t.Fatalf("start Postgres with Testcontainers (is Docker running? Or set TEST_DATABASE_URL, "+
			"or run go test -short to skip these tests): %v", startErr)
	}
	return startedURL
}

// start runs the Postgres container and returns it with its URL. It doesn't use a test's context:
// the container outlives the test that started it.
func start() (*tcpostgres.PostgresContainer, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()

	// The password only protects a container on a random local port that lives as long as the
	// tests, so a fixed one is fine. The module also turns fsync off: faster, and nothing here
	// needs to survive a crash.
	container, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase("pgtest"),
		tcpostgres.WithUsername("pgtest"),
		tcpostgres.WithPassword("pgtest"),
		tcpostgres.BasicWaitStrategies(), // wait until Postgres accepts connections
	)
	if err != nil {
		_ = testcontainers.TerminateContainer(container) // a half-started container, if any
		return nil, "", err
	}

	serverURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = testcontainers.TerminateContainer(container)
		return nil, "", fmt.Errorf("read connection string: %w", err)
	}
	return container, serverURL, nil
}
