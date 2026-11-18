# Short names for the commands used while working on the project, like Maven or Gradle tasks.
# CI runs the same checks, so "make check" passing locally means CI should pass too.
#
# Syntax: "name: dependencies" followed by the commands to run, each indented with a TAB.

# Only the application code. examples/ is left out on purpose (see docs/open/05-decisions.md).
PACKAGES := ./cmd/... ./internal/... ./migrations/...

# Plain "make" shows the list of targets.
.DEFAULT_GOAL := help

# .PHONY says these names are commands, not files to build. Without it, a file or folder named
# "test" would make "make test" think there is nothing to do.
.PHONY: help run build test test-short lint fmt check up down migrate-up migrate-down migrate-version migrate-force

help:
	@echo "make run              start the app"
	@echo "make build            compile the app into bin/"
	@echo "make test             run all tests with the race detector (needs Docker or TEST_DATABASE_URL)"
	@echo "make test-short       only the tests that need no Postgres"
	@echo "make lint             run golangci-lint (includes go vet and format checks)"
	@echo "make fmt              format the code"
	@echo "make check            lint and test, what CI runs"
	@echo "make up               start Postgres in Docker (needs .env)"
	@echo "make down             stop all Compose containers (the data stays)"
	@echo "make migrate-up       apply all new database migrations (needs DATABASE_URL)"
	@echo "make migrate-down     roll back the last migration"
	@echo "make migrate-version  show the database's migration version"
	@echo "make migrate-force version=N   mark version N as applied after fixing a failed migration"

run:
	go run ./cmd/ticketing

build:
	go build -o bin/ ./cmd/ticketing

# -count=1 turns off Go's test cache, so the tests always really run.
# Tests that need Postgres get it from pgtest: TEST_DATABASE_URL if set, otherwise a container
# started by Testcontainers (Docker must be running).
test:
	go test -count=1 -race $(PACKAGES)

# -short skips the tests that need Postgres, so no Docker is needed.
test-short:
	go test -count=1 -race -short $(PACKAGES)

lint:
	golangci-lint run $(PACKAGES)

fmt:
	golangci-lint fmt $(PACKAGES)

check: lint test

# Only Postgres: Redis and redis-commander are just for examples/.
# --wait returns only when Postgres passes its health check.
up:
	docker compose up postgres -d --wait

down:
	docker compose down

# Database migrations (cmd/migrate). They read DATABASE_URL from the environment, like the app.
migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down

migrate-version:
	go run ./cmd/migrate version

# $(version) comes from the command line: make migrate-force version=3
migrate-force:
	go run ./cmd/migrate force $(version)
