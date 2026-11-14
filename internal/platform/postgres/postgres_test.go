package postgres

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
)

const testPassword = "s3cret-test-password"

func TestNewPool_InvalidURL(t *testing.T) {
	_, err := NewPool(context.Background(), "postgres://user:"+testPassword+"@localhost:not-a-port/db")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Errorf("error contains the password: %v", err)
	}
}

func TestNewPool_UnreachableServer(t *testing.T) {
	// Open a port to get a free number, then close it, so nothing is listening there.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	_, err = NewPool(context.Background(), "postgres://user:"+testPassword+"@"+addr+"/db?sslmode=disable")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Errorf("error contains the password: %v", err)
	}
}

// This test needs a real Postgres, so it is skipped unless TEST_DATABASE_URL is set. CI sets it.
// M1 step 3 replaces this with a Postgres that the tests start themselves.
func TestNewPool_ConnectsToPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	pool, err := NewPool(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	var got int
	if err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != 1 {
		t.Errorf("SELECT 1: got %d", got)
	}
}
