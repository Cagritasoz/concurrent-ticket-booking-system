// Command ticketing is the starting point of the ticket booking service.
//
// This is the composition root: the one place where all parts of the application are created
// and connected by hand (config, logger, clients, modules, HTTP server). It plays the role that
// @SpringBootApplication and the ApplicationContext play in Spring Boot.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/config"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/httpapi"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/telemetry"
)

// main stays tiny on purpose. It only calls run and decides how the process exits.
// It is the only function in the whole application allowed to call os.Exit.
func main() {
	ctx := context.Background()

	if err := run(ctx); err != nil {
		// If writing to stderr fails there is nowhere left to report it, and we exit anyway,
		// so both results (bytes written, error) are ignored on purpose with "_".
		_, _ = fmt.Fprintf(os.Stderr, "ticketing: %v\n", err)
		os.Exit(1)
	}
}

// run builds and starts the application. It returns an error instead of exiting, so it can be
// called from tests and so deferred cleanup code always runs.
//
// The following steps fill it in: config (step 2), logger (step 3), HTTP server (step 4) and
// graceful shutdown (step 5).
func run(ctx context.Context) error {
	_ = ctx // Not used yet. From step 5 on, cancelling ctx (Ctrl+C) tells the app to shut down.

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

	// Log selected fields only, never the whole config: later it will contain secrets.
	logger.Info("starting ticketing",
		"http_addr", cfg.HTTP.Addr,
		"log_level", cfg.Log.Level,
		"log_format", cfg.Log.Format,
	)
	readiness := &httpapi.Readiness{} // Set to false as its zero value.
	router := httpapi.NewRouter(logger, readiness)

	// Open the port first, then mark the app ready: /readyz only says "ready" once we can
	// actually accept connections.
	// Listen asks the operating system to open port 8080 and start accepting connections.
	listener, err := net.Listen("tcp", cfg.HTTP.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTP.Addr, err)
	}

	// The server is created only after the port is open, so there is nothing to clean up if
	// Listen fails. From here on, the deferred Close runs on every way out of run (like a
	// try-with-resources block in Java): it closes the listener and any connections still open.
	server := newHTTPServer(cfg.HTTP, router, logger)
	defer func() {
		if err := server.Close(); err != nil {
			logger.Error("close http server", "error", err)
		}
	}()

	readiness.SetReady(true)
	logger.Info("http server listening", "addr", listener.Addr().String())

	// Serve blocks until the server stops. Until graceful shutdown exists (step 5), Ctrl+C
	// simply kills the process. .Serve starts the server, return nil line runs after server stops.
	if err := server.Serve(listener); err != nil {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// newHTTPServer wraps the router in a standard http.Server with timeouts.
//
// Gin's own router.Run() starts a server with no timeouts at all, so one slow or malicious
// client could hold a connection (and its goroutine) open forever. Each timeout below limits
// one phase of a request; the values come from config (see docs/open/07-configuration.md).
func newHTTPServer(cfg config.HTTPConfig, handler http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler, // *gin.Engine is passed as handler, which has the required Handler interfaces serveHttp func.
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		// The server's own errors (e.g. a TLS handshake failure) go through slog too.
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
}
