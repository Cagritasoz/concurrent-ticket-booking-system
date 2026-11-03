# Short names for the commands used while working on the project, like Maven or Gradle tasks.
# CI runs the same checks, so "make check" passing locally means CI should pass too.
#
# Syntax: "name: dependencies" followed by the commands to run, each indented with a TAB.

# Only the application code. examples/ is left out on purpose (see docs/open/05-decisions.md).
PACKAGES := ./cmd/... ./internal/...

# Plain "make" shows the list of targets.
.DEFAULT_GOAL := help

# .PHONY says these names are commands, not files to build. Without it, a file or folder named
# "test" would make "make test" think there is nothing to do.
.PHONY: help run build test lint fmt check up down

help:
	@echo "make run     start the app"
	@echo "make build   compile the app into bin/"
	@echo "make test    run the tests with the race detector"
	@echo "make lint    run golangci-lint (includes go vet and format checks)"
	@echo "make fmt     format the code"
	@echo "make check   lint and test, what CI runs"
	@echo "make up      start Redis and redis-commander in Docker"
	@echo "make down    stop them"

run:
	go run ./cmd/ticketing

build:
	go build -o bin/ ./cmd/ticketing

# -count=1 turns off Go's test cache, so the tests always really run.
test:
	go test -count=1 -race $(PACKAGES)

lint:
	golangci-lint run $(PACKAGES)

fmt:
	golangci-lint fmt $(PACKAGES)

check: lint test

up:
	docker compose up -d

down:
	docker compose down
