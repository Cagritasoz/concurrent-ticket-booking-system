# 03 · Architecture

## Package layout

### Today

```
.
├── cmd/
│   ├── ticketing/main.go            # composition root: config → logger → Postgres pool → router → listener → http.Server; graceful shutdown
│   └── migrate/main.go              # migration command: up, down, version, force
├── internal/
│   ├── config/                      # environment variables → typed Config + Validate()
│   ├── event/                       # MODULE: venues so far (events, tiers, tickets later in M1)
│   │   ├── venue.go                 # Venue (the module's own type), ErrVenueNotFound, row → Venue
│   │   ├── venue_service.go         # VenueService: the venue rules, on top of the generated queries
│   │   ├── queries/venues.sql       # the SQL sqlc reads
│   │   └── eventdb/                 # sqlc-generated (db.go, models.go, venues.sql.go) + hand-written tests
│   ├── platform/
│   │   ├── postgres/                # connection pool (pgxpool), Migrator (golang-migrate), WithTx, constraint error checks
│   │   │   └── pgtest/              # a fresh database per test on a Testcontainers Postgres (or TEST_DATABASE_URL)
│   │   └── telemetry/               # slog logger setup, request-ID context + log handler
│   └── httpapi/                     # Gin router, middleware, health handlers, Readiness, venue handlers, Problem Details errors
├── migrations/                      # SQL migrations (NNNNNN_name.up.sql / .down.sql), embedded by migrations.go
├── examples/                        # early learning code, not part of the app (see 09-examples.md)
├── docs/open/                       # this documentation
├── sqlc.yaml                        # sqlc settings: schema from migrations/, queries per module, Go types
├── docker-compose.yaml              # Postgres; Redis + redis-commander (used by the examples)
├── Makefile                         # short commands: make test, make lint, make check, make generate, ...
├── .golangci.yml                    # golangci-lint configuration
├── .github/workflows/ci.yaml        # GitHub Actions: lint, sqlc diff, test, migrations, build
├── .gitattributes                   # keeps Go files LF on Windows
├── .env.example                     # documented example configuration
└── go.mod / go.sum
```

### Target

Folders marked *(planned)* don't exist yet; the milestone that adds them is in brackets.

```
.
├── cmd/
│   ├── ticketing/main.go            # composition root: config → clients → modules → HTTP server + workers; graceful shutdown
│   └── migrate/main.go              # migration command
├── internal/
│   ├── config/                      # env vars → typed Config + Validate()
│   ├── platform/                    # infrastructure client setup ONLY, no business rules
│   │   ├── postgres/                # pgxpool, Migrator, WithTx, constraint error checks (e.g. IsUniqueViolation)
│   │   │   └── pgtest/              # a fresh database per test on a Testcontainers Postgres (or TEST_DATABASE_URL), optionally migrated
│   │   ├── redis/                   # (planned, M5) client constructor (returns an error, never exits)
│   │   ├── elastic/                 # (planned, M8) client constructor
│   │   └── telemetry/               # slog setup, request IDs; Prometheus registry later (M9)
│   ├── auth/                        # (planned, M2) OIDC verifier, Principal{Subject, Roles}, ctx helpers (no Gin)
│   ├── event/                       # MODULE: venues, events, price tiers, tickets (venue service started; the rest M1)
│   │   ├── venue.go venue_service.go ...  # the module's types, errors and services
│   │   ├── queries/*.sql
│   │   └── eventdb/                 # sqlc-generated
│   ├── booking/                     # (planned, M3–M7) MODULE: holds, bookings, idempotency, payments, webhooks, reconciliation
│   │   ├── booking.go               # Booking, Status, transition table (pure, unit-tested)
│   │   ├── service.go idempotency.go payment.go holds.go expiry.go reconcile.go
│   │   ├── queries/*.sql
│   │   ├── bookingdb/               # sqlc-generated
│   │   ├── redishold/               # HoldCache implementation: SET NX PX + Lua compare-and-delete
│   │   └── stripepay/               # PaymentGateway implementation (stripe-go) + webhook signature verification
│   ├── search/                      # (planned, M8) MODULE: mapping, outbox consumer, queries, alias-swap rebuild
│   └── httpapi/                     # Gin: router, middleware, handlers, error → status mapping, health
├── examples/                        # early learning code, not part of the app
├── migrations/                      # golang-migrate files, shared by all modules (one schema history)
├── sqlc.yaml
├── dev/keycloak/realm.json          # (planned, M2)
├── loadtest/                        # (planned, M9) k6 scripts
├── docs/open/                       # documentation
├── docker-compose.yaml
├── Makefile
├── .golangci.yml
└── .github/workflows/ci.yaml
```

## Dependency rules

- **Dependencies point inward.** `httpapi` and the infrastructure packages depend on the modules;
  the modules never import `httpapi`, Gin or an infrastructure client directly. Go rejects import
  cycles at compile time, which helps keep this.
- **Gin only in `internal/httpapi`.** Services never see `*gin.Context`. Handlers translate HTTP
  into plain Go calls and back, like `@RestController` classes in Spring.
- **Each module owns its SQL and its generated sqlc package.** Modules talk to each other through
  small interfaces, e.g. `booking` reads prices through a `TicketCatalog` interface that
  `event.Service` satisfies.
- `internal/` is enforced by the Go compiler: nothing outside this module can import it.

## Go conventions

- `context.Context` is the first parameter of anything doing I/O. `context.Background()` is
  created only in `main` (and in tests), never inside library code.
- Library code returns errors and never calls `log.Fatal` or `os.Exit`; only `main` decides to
  exit.
- Errors are wrapped with `fmt.Errorf("...: %w", err)` and compared with `errors.Is` / `errors.As`.
  An error is ignored only explicitly (`_ =`) with a comment saying why.
- **Few interfaces, defined by the package that uses them**, and only where something external
  must be faked or broken in tests: `PaymentGateway`, `HoldCache`, the search indexer, the clock.
  Postgres access stays concrete (sqlc + a transaction helper) and is tested against real
  Postgres.
- **Explicit dependency injection** through constructors (`NewXxx(deps...)`), wired by hand in
  `cmd/ticketing`. No DI framework, no global mutable state. (Gin's release mode is the one
  unavoidable global, set once in `httpapi`'s `init`.)
- **Logging:** one `*slog.Logger` is created in `run` and passed to whoever needs it; there is no
  `slog.SetDefault`. Code that runs during a request logs with `logger.XxxContext(ctx, ...)` so
  the request ID is added. Never log the whole config, since it will contain secrets.
- Money is integer minor units plus a currency code, never floats.
- Background workers (hold expiry, payment reconciliation, search indexing) are goroutines in the
  same binary, coordinated with `SELECT … FOR UPDATE SKIP LOCKED` so several replicas are safe.
  A goroutine needs its own panic recovery; the HTTP recovery middleware can't catch it.
- Avoid copying Java/Spring patterns: no giant service structs, no repository interface around
  every table, no generic `utils` or `common` packages.
