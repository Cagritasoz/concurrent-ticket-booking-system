// Command ticketing is the starting point of the ticket booking service.
//
// This is the composition root: the one place where all parts of the application are created
// and connected by hand (config, logger, clients, modules, HTTP server). It plays the role that
// @SpringBootApplication and the ApplicationContext play in Spring Boot.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// The time zone database, embedded into the binary (about 450 KB). event checks venue time
	// zones with time.LoadLocation, which otherwise needs the operating system's copy: Windows
	// and minimal container images have none. The _ imports the package only for this side
	// effect; nothing in it is called.
	_ "time/tzdata"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/config"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/httpapi"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/telemetry"
)

// main stays tiny on purpose. It only calls run and decides how the process exits.
// It is the only function in the whole application allowed to call os.Exit.
func main() {
	// ctx is canceled when the process receives Ctrl+C (os.Interrupt) or SIGTERM, which is what
	// Docker and Kubernetes send to stop a container. Instead of the process being killed, run
	// sees the canceled ctx and shuts down gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	// After the first signal, stop listening for signals, so a second Ctrl+C kills the process
	// right away if a graceful shutdown takes too long.
	go func() {
		<-ctx.Done() // Block the goroutine until the context is canceled by a signal or by stop().
		stop()       // Unregister the signal behavior, restore defaults.
	}()

	err := run(ctx)
	stop() // os.Exit skips deferred calls, so release the signal handling explicitly first.
	if err != nil {
		// If writing to stderr fails there is nowhere left to report it, and we exit anyway,
		// so both results (bytes written, error) are ignored on purpose with "_".
		_, _ = fmt.Fprintf(os.Stderr, "ticketing: %v\n", err)
		os.Exit(1)
	}
}

// run builds the application and serves it until ctx is canceled, then shuts it down gracefully.
// It returns an error instead of exiting, so it can be called from tests and so deferred cleanup
// code always runs.
func run(ctx context.Context) error {
	// os.LookupEnv is passed in as a function value: config reads through it, and tests pass a fake.
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Logs go to stdout. In production the platform (Docker, Kubernetes) collects stdout and ships
	// it to a log store, so the app never writes log files itself.
	logger, err := telemetry.NewLogger(cfg.Log.Level, cfg.Log.Format, os.Stdout)
	if err != nil {
		return fmt.Errorf("create logger: %w", err)
	}

	logger.Info("starting ticketing",
		"http_addr", cfg.HTTP.Addr,
		"log_level", cfg.Log.Level,
		"log_format", cfg.Log.Format,
	)

	// Connect before opening the port, so the app never accepts requests it can't serve.
	// Deferred calls run in reverse order, so the pool closes after the HTTP server further down:
	// requests that are still finishing can use it until the end.
	pool, err := postgres.NewPool(ctx, cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pool.Close()
	logger.Info("connected to postgres",
		"host", pool.Config().ConnConfig.Host,
		"database", pool.Config().ConnConfig.Database,
	)

	// The services, created once and shared by every request. This is the hand-written version of
	// what Spring's ApplicationContext does with @Service beans and @Autowired.
	venues := event.NewVenueService(pool)
	halls := event.NewHallService(pool)

	readiness := &httpapi.Readiness{} // Set to false as its zero value.
	router := httpapi.NewRouter(logger, readiness, pool, venues, halls)

	// Open the port first, then mark the app ready: /readyz only says "ready" once we can
	// actually accept connections.
	// Listen asks the operating system to bind a listening socket to the configured address.
	// The server will accept connections from this listener when Serve starts.
	listener, err := net.Listen("tcp", cfg.HTTP.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTP.Addr, err)
	}

	// The server is created only after the port is open, so there is nothing to clean up if
	// Listen fails. From here on, the deferred Close runs on every way out of run (like a
	// try-with-resources block in Java): it closes the listener and any connections still open.
	// run created the server, so run closes it.
	server := newHTTPServer(cfg.HTTP, router, logger)
	defer func() {
		if err := server.Close(); err != nil {
			logger.Error("close http server", "error", err)
		}
	}()

	return serve(ctx, listener, server, readiness, cfg.ShutdownTimeout, logger)
}

// serve runs server on listener until ctx is canceled, then shuts it down gracefully.
// It is separate from run so tests can call it with their own handler (for example a slow one)
// and a listener whose address they know. The caller owns server and must Close it afterwards.
func serve(
	ctx context.Context,
	listener net.Listener,
	server *http.Server,
	readiness *httpapi.Readiness,
	shutdownTimeout time.Duration,
	logger *slog.Logger,
) error {

	// Serve blocks until the server stops, so it runs in its own goroutine. That leaves run free
	// to wait for whichever comes first: the server failing, or a shutdown request.
	//
	// A channel is a typed pipe between goroutines; this one carries error values. The arrow
	// shows which way a value moves: ch <- v sends v into ch, and v := <-ch takes a value out.
	// The 1 is the buffer size: the channel can hold one value nobody has received yet, so the
	// goroutine can always send and exit, even if nobody is receiving anymore (with an unbuffered
	// channel the send would wait for a receiver, forever).
	serveErr := make(chan error, 1)
	go func() {
		// Serve always returns an error when it stops: http.ErrServerClosed after Shutdown,
		// anything else if it failed. Either way it is sent to serveErr.
		serveErr <- server.Serve(listener)
	}()

	readiness.SetReady(true)
	logger.Info("http server listening", "addr", listener.Addr().String())

	// select waits on several channels at once and runs the case that is ready first.
	// Block until either the server sends a result into serveErr or the application context is canceled.
	select {
	case err := <-serveErr: // receives Serve's error
		// Serve stopped on its own, without a shutdown request: something is wrong.
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done(): // Done is closed on cancel; the received value carries nothing and is dropped
		logger.Info("shutdown started")
	}

	return shutdown(ctx, server, serveErr, readiness, shutdownTimeout, logger)
}

// shutdown stops the server gracefully: it marks the app not ready, stops accepting new
// connections, and waits up to timeout for in-flight requests to finish.
// Spring equivalent: server.shutdown=graceful + spring.lifecycle.timeout-per-shutdown-phase.
func shutdown(
	ctx context.Context,
	server *http.Server,
	serveErr <-chan error, // receive-only: shutdown can take values out of it, never send
	readiness *httpapi.Readiness,
	timeout time.Duration,
	logger *slog.Logger,
) error {
	// 1. Not ready: /readyz answers 503, so a load balancer stops sending new requests here.
	readiness.SetReady(false)

	// 2. Give in-flight requests at most `timeout` to finish. ctx is already canceled, so the
	// deadline is built on context.WithoutCancel(ctx): same values, but not canceled.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel() // releases the timer behind the deadline, like closing a resource

	// Shutdown closes the listener (no new connections), closes idle connections, and waits for
	// active requests to finish. If the deadline passes first it returns an error, and the
	// deferred server.Close() in run then force-closes whatever is still open.
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown did not finish within %s: %w", timeout, err)
	}

	// 3. After Shutdown, Serve returns http.ErrServerClosed. That is the normal way for it to end;
	// any other error is a real problem.
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}

	logger.Info("shutdown complete")
	return nil
}

// newHTTPServer wraps the router in a standard http.Server with timeouts. The server accepts any
// http.Handler as the router: Gin here, or the standard library's http.ServeMux without Gin.
//
// Gin's own router.Run() starts a server with no timeouts at all, so one slow or malicious
// client could hold a connection (and its goroutine) open forever. Each timeout below limits
// one phase of a request; the values come from config (see docs/open/07-configuration.md).
func newHTTPServer(cfg config.HTTPConfig, handler http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler, // the *gin.Engine: it has a ServeHTTP method, so it is an http.Handler
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		// The server's own errors (e.g. a TLS handshake failure) go through slog too.
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
}
