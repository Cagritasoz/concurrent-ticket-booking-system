// Command ticketing is the starting point of the ticket booking service.
//
// This is the composition root: the one place where all parts of the application are created
// and connected by hand (config, logger, clients, modules, HTTP server). It plays the role that
// @SpringBootApplication and the ApplicationContext play in Spring Boot.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/config"
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

	// Temporary output until the logger exists (step 3).
	fmt.Printf("ticketing: starting with config %+v\n", cfg)

	return nil
}
