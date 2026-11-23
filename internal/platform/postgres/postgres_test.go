package postgres_test

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres/pgtest"
)

const testPassword = "s3cret-test-password"

func TestNewPool_InvalidURL(t *testing.T) {
	_, err := postgres.NewPool(context.Background(), "postgres://user:"+testPassword+"@localhost:not-a-port/db")
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

	_, err = postgres.NewPool(context.Background(), "postgres://user:"+testPassword+"@"+addr+"/db?sslmode=disable")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Errorf("error contains the password: %v", err)
	}
}

func TestNewPool_ConnectsToPostgres(t *testing.T) {
	databaseURL := pgtest.NewDatabase(t)

	pool, err := postgres.NewPool(context.Background(), databaseURL)
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
