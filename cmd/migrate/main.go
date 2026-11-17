// Command migrate applies the database migrations in migrations/, like Flyway's command line.
// It reads DATABASE_URL (and the LOG_ settings) from the environment, the same way the app does.
//
//	migrate up         apply every migration that hasn't been applied yet
//	migrate down       roll back the last applied migration
//	migrate version    show the current version
//	migrate force N    set the version to N and clear the dirty flag, after a failed migration
//	                   was fixed by hand (0 means no migration applied)
//
// The Makefile wraps these as make migrate-up, migrate-down, migrate-version and migrate-force.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/config"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/telemetry"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/migrations"
)

const usage = "usage: migrate up | down | version | force N"

// command is a parsed command line.
type command struct {
	name    string // up, down, version or force
	version uint   // only for force
}

func main() {
	// No signal handling on purpose: golang-migrate can't be interrupted cleanly, so Ctrl+C ends
	// the process and Postgres rolls back the migration that was running.
	// slice[start:end], start inclusive, end exclusive
	if err := run(context.Background(), os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cmd, err := parseArgs(args)
	if err != nil {
		return err
	}

	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger, err := telemetry.NewLogger(cfg.Log.Level, cfg.Log.Format, os.Stdout)
	if err != nil {
		return fmt.Errorf("create logger: %w", err)
	}

	migrator, err := postgres.NewMigrator(ctx, cfg.Database.URL, migrations.Files)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer func() {
		if err := migrator.Close(); err != nil {
			logger.Error("close database connection", "error", err)
		}
	}()

	before, _, err := migrator.Version()
	if err != nil {
		return err
	}

	switch cmd.name {
	case "up":
		err = migrator.Up()
	case "down":
		err = migrator.Down()
	case "force":
		err = migrator.Force(cmd.version)
	case "version":
		// nothing to change, only report below
	}
	if err != nil {
		return err
	}

	after, dirty, err := migrator.Version()
	if err != nil {
		return err
	}
	logger.Info("database migrations",
		"command", cmd.name,
		"version_before", before,
		"version", after,
		"dirty", dirty,
	)
	return nil
}

// parseArgs checks the command line before anything connects to the database.
func parseArgs(args []string) (command, error) {
	if len(args) == 0 {
		return command{}, errors.New(usage)
	}
	switch name := args[0]; name {
	case "up", "down", "version":
		if len(args) != 1 {
			return command{}, fmt.Errorf("%s takes no arguments; %s", name, usage)
		}
		return command{name: name}, nil
	case "force":
		if len(args) != 2 {
			return command{}, fmt.Errorf("force needs exactly one version; %s", usage)
		}
		version, err := strconv.ParseUint(args[1], 10, 32)
		if err != nil {
			return command{}, fmt.Errorf("force: version %q must be a whole number of 0 or more", args[1])
		}
		return command{name: name, version: uint(version)}, nil
	default:
		return command{}, fmt.Errorf("unknown command %q; %s", name, usage)
	}
}
