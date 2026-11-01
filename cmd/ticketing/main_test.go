package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// These tests call run directly, the way main does. t.Setenv sets an environment variable for
// this test only and restores the old value afterwards. Port 0 asks the operating system for any
// free port, so the tests never clash with an app already running on 8080.
// The full shutdown test (an in-flight request still finishes) comes in M0 step 6.

func TestRun_ReturnsNilAfterShutdown(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("LOG_LEVEL", "error") // keep the test output quiet

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()

	cancel() // what Ctrl+C does in main

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned an error after shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s after ctx was cancelled")
	}
}

func TestRun_FailsWhenPortIsTaken(t *testing.T) {
	// Occupy a port first, then ask run to listen on the same one.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = taken.Close() })

	t.Setenv("HTTP_ADDR", taken.Addr().String())
	t.Setenv("LOG_LEVEL", "error")

	err = run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "listen on") {
		t.Fatalf("expected a listen error, got %v", err)
	}
}
