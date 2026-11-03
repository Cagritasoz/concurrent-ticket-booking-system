package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/httpapi"
)

// These tests call run directly, the way main does. t.Setenv sets an environment variable for
// this test only and restores the old value afterwards. Port 0 asks the operating system for any
// free port, so the tests never clash with an app already running on 8080.

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

// The tests below call serve with a slow handler, so a request is still running when shutdown
// starts. The handler blocks until the test closes the release channel, which lets the test
// decide exactly when the request finishes instead of guessing with sleeps.

// slowServer is a server whose only handler signals started, then waits for release.
type slowServer struct {
	listener net.Listener
	server   *http.Server
	started  chan struct{} // closed when the handler has received the request
	release  chan struct{} // the test closes it to let the handler answer
	shutdown chan struct{} // closed when server.Shutdown has been called
}

func newSlowServer(t *testing.T) *slowServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	s := &slowServer{
		listener: listener,
		started:  make(chan struct{}), // empty struct to mean that event matters not the type.
		release:  make(chan struct{}),
		shutdown: make(chan struct{}),
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(s.started) // close started channel
		<-s.release      // Wait for "release" channel to be closed.
		_, _ = io.WriteString(w, "done")
	})
	s.server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	// RegisterOnShutdown functions are called when Shutdown starts, so the test knows for sure
	// that shutdown is in progress.
	s.server.RegisterOnShutdown(func() { close(s.shutdown) }) // close shutdown channel on server shutdown
	// The test created the server, so the test closes it (run does the same with its server).
	t.Cleanup(func() { _ = s.server.Close() })
	return s
}

// response is what the test client got back.
type response struct {
	status int
	body   string
	err    error
}

// sendRequest sends a GET in the background and delivers the result on the returned channel.
func (s *slowServer) sendRequest() <-chan response {
	result := make(chan response, 1) // channel that uses the response struct.
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		// The address decides which server is reached: the listener knows the real port the
		// operating system picked. "/slow" is only a label: there is no router here, http.Server
		// calls its one Handler for every request, and that handler never reads the path.
		// In the real app, Gin is the Handler, and Gin is what matches paths to routes.
		resp, err := client.Get("http://" + s.listener.Addr().String() + "/slow")
		if err != nil {
			result <- response{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		result <- response{status: resp.StatusCode, body: string(body), err: err}
	}()
	return result // return the channel
}

// waitFor fails the test if ch is not closed within 5 seconds.
func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestServe_InFlightRequestFinishesDuringShutdown(t *testing.T) {
	s := newSlowServer(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, s.listener, s.server, &httpapi.Readiness{}, 5*time.Second, logger) }()

	result := s.sendRequest()
	waitFor(t, s.started, "the request to reach the handler")

	cancel() // Ctrl+C while the request is still running
	waitFor(t, s.shutdown, "shutdown to start")

	// Shutdown waits for the running request, so serve must not have returned yet.
	// select with a default does not block, if the cases are not ready default is picked, not blocking anymore.
	select {
	case err := <-done:
		t.Fatalf("serve returned while a request was still running: %v", err)
	default:
	}

	close(s.release) // let the request finish by closing "release" channel

	res := <-result // get the result from channel.
	if res.err != nil {
		t.Fatalf("in-flight request failed: %v", res.err)
	}
	if res.status != http.StatusOK || res.body != "done" {
		t.Errorf("in-flight request: got %d %q, want 200 \"done\"", res.status, res.body)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned an error after a clean shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the in-flight request finished")
	}
}

func TestServe_FailsWhenShutdownTimeoutPasses(t *testing.T) {
	s := newSlowServer(t)
	t.Cleanup(func() { close(s.release) }) // unblock the handler goroutine when the test ends
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, s.listener, s.server, &httpapi.Readiness{}, 100*time.Millisecond, logger) }()

	result := s.sendRequest()
	waitFor(t, s.started, "the request to reach the handler")

	cancel() // the request never finishes, so the 100ms deadline passes

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected a deadline error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the shutdown timeout")
	}

	// What run's deferred Close does next: force-close the connection that is still open. The
	// client then gets an error instead of a response.
	_ = s.server.Close()
	if res := <-result; res.err == nil {
		t.Errorf("expected the force-closed request to fail, got %d %q", res.status, res.body)
	}
}
