// Package pgtest gives tests a Postgres database of their own, like Spring's @DataJpaTest gives
// each test class a database.
//
// For now NewDatabase creates a fresh database on the server in TEST_DATABASE_URL and skips the
// test when that variable isn't set. M1 step 3 changes it to start Postgres with Testcontainers;
// tests that call NewDatabase stay the same.
package pgtest

import (
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// setupTimeout limits how long creating the database may take.
const setupTimeout = 10 * time.Second

// NewDatabase creates an empty database with a random name and returns its URL. The database is
// dropped when the test ends, so tests never share data, and the database named in
// TEST_DATABASE_URL itself is never changed.
func NewDatabase(t *testing.T) string {
	t.Helper()

	serverURL := os.Getenv("TEST_DATABASE_URL")
	if serverURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
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
