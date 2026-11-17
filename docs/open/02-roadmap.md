# 02 · Roadmap

## Where we are

- **Current milestone:** M1 Postgres + Event, on branch `milestone/postgres-event`.
- **Finished:** M0 Foundation, merged into `main` with CI green (entry point, config, logging,
  HTTP server with health checks and request IDs, graceful shutdown, endpoint and shutdown tests,
  Makefile, lint config and CI workflow).
- **Done in M1:** step 1 (Postgres in Docker Compose, connection pool, `/readyz` checks Postgres),
  step 2 (golang-migrate, `cmd/migrate`, the `venues` table).
- **Next:** M1 step 3, tests start their own Postgres with Testcontainers.

Each milestone is developed on its own branch named `milestone/{milestone name}`, branched from
`main`. Every milestone ends with `go test -race` green (unit and integration tests) and CI passing.

## Milestones

| M | Name | Scope | Proof it's done | Status |
|---|---|---|---|---|
| 0 | Foundation | Entry point, config, logging, HTTP server, health checks, graceful shutdown, tooling and CI (see steps below) | Handler tests, shutdown test, CI green | Done |
| 1 | Postgres + Event | Postgres in Compose, golang-migrate, sqlc, a shared test harness for Postgres; venues with layouts (seated sections with seats, standing sections with a capacity), events, ticket tiers, tickets (seated and standing); admin CRUD; publish and cancel rules | Integration tests against real Postgres, domain unit tests | In progress |
| 2 | Auth | Keycloak realm in Compose, OIDC middleware, `Principal`, customer and event-admin roles, ownership checks | Tests with a test key set: bad signature, issuer, audience, expiry, missing role, another user's resource; one Keycloak container test | Not started |
| 3 | Booking core | Bookings, the "one active booking per ticket" index, the booking state machine, seat selection (pick seats or a general-admission quantity), per-customer limits per tier, expiry sweeper, `POST /bookings`; the concurrent booking test ported to Postgres | Exactly one winner per ticket (including 1,000 concurrent users booking the same seat), expiry frees seats, multi-ticket bookings are all-or-nothing, deadlock-free ordering, parallel requests can't exceed a customer limit | Not started |
| 4 | Idempotency | `Idempotency-Key` header, request hash, response replay | Concurrent duplicates, payload mismatch, rollback on crash, fuzzed request canonicalization | Not started |
| 5 | Redis holds | Redis hold cache (`SET NX PX` + Lua compare-and-delete), timeouts, circuit breaker, fail-open to Postgres | Redis stopped, paused or flushed mid-test: still exactly one winner | Not started |
| 6 | Payments | `PaymentGateway`, stripe-go adapter, payment attempts, webhook handling (raw body, signature, dedupe) | Fake gateway tests, signed test payloads, duplicate and out-of-order webhooks, an optional Stripe test-mode test, `stripe listen` | Not started |
| 7 | Fault tolerance | Payment reconciler, late-payment policy, refunds, timeouts, uncertain outcomes | Timeout, crash-between-steps and retry tests | Not started |
| 8 | Search | Elasticsearch in Compose, transactional outbox, indexer, mapping, filters, `search_after` pagination, alias-swap rebuild | Testcontainers Elasticsearch: sync, rebuild, ordering | Not started |
| 9 | Hardening | Prometheus metrics, k6 load tests (hot seat, browse/search mix), fuzz tests, coverage in CI, ADRs | k6 thresholds met; a post-run SQL check finds zero double sales | Not started |

## M0 Foundation steps

| Step | What | Spring Boot equivalent | Status |
|---|---|---|---|
| 1 | `cmd/ticketing/main.go`: the composition root, `main` → `run(ctx) error` | `@SpringBootApplication` + ApplicationContext | Done |
| 2 | `internal/config`: environment variables → typed `Config`, defaults, validation reporting all errors at once; `.env.example`; [configuration doc](07-configuration.md) | `application.properties` + `@ConfigurationProperties` + `@Validated` | Done |
| 3 | `internal/platform/telemetry`: `log/slog` logger, JSON or text, level filtering, passed explicitly (no global logger) | SLF4J + Logback | Done |
| 4 | `internal/httpapi`: Gin router, request-ID / request-logging / recovery middleware, `/healthz` and `/readyz`; `http.Server` with timeouts | Spring MVC + Actuator liveness/readiness | Done |
| 5 | Graceful shutdown on Ctrl+C / SIGTERM: mark not ready, let in-flight requests finish within `SHUTDOWN_TIMEOUT` | `server.shutdown=graceful` | Done |
| 6 | Full endpoint tests with `net/http/httptest` (404, panic → 500) and a shutdown test (in-flight request finishes, `run` returns cleanly) | `MockMvc` + `@SpringBootTest(webEnvironment = RANDOM_PORT)` | Done |
| 7 | Makefile, golangci-lint config, GitHub Actions CI. (Untracking `.idea/` was done early.) | Maven/Gradle tasks + CI | Done |

Tests are written within each step; step 6 added the larger handler and shutdown tests.

### What steps 1–7 delivered

See [Application runtime](06-application-runtime.md) for how these pieces work together.

- **Step 1:** `main` only calls `run` and decides the exit code; `run` returns errors, so deferred
  cleanup always runs and tests can call it.
- **Step 2:** `config.Load(os.LookupEnv)` with table-driven tests; empty values use defaults;
  invalid values stop startup with every problem listed.
- **Step 3:** `telemetry.NewLogger(level, format, w)`; tests write into a buffer and check the
  format and level filtering.
- **Step 4:** Gin in release mode with `gin.New()`, no trusted proxies; request IDs added to every
  log line written with the request's context; health endpoints; `http.Server` with all four
  timeouts, created after the listener is open and closed by a deferred `Close()`.
- **Step 5:** Ctrl+C / SIGTERM cancel the root context (`signal.NotifyContext`); `Serve` runs in a
  goroutine and `run` waits with `select`; shutdown marks the app not ready, calls
  `server.Shutdown` with `SHUTDOWN_TIMEOUT` and returns `nil`; a second Ctrl+C exits at once.
  Tests: `run` returns `nil` after its context is cancelled, and fails when the port is taken.
- **Step 6:** the serve-and-shut-down part of `run` moved into `serve(ctx, listener, server, …)`
  so tests can pass their own handler. Shutdown tests with a handler the test releases on cue: a
  request running when shutdown starts still gets its response and `serve` returns `nil`; a
  request that outlives `SHUTDOWN_TIMEOUT` is force-closed and `serve` returns a deadline error.
  Router tests: an unknown route gives 404 and is logged as WARN; a panicking handler gives a
  generic 500, is logged with its value and stack, and the server keeps working.
- **Step 7:** `Makefile` (`run`, `build`, `test`, `lint`, `fmt`, `check`, `up`, `down`);
  `.golangci.yml` (standard linters plus `bodyclose`, `errorlint`, `misspell`, `unconvert`, and
  the `gofmt` and `goimports` formatters); `.github/workflows/ci.yaml` (lint, test with `-race`
  and build on pushes to `main` and milestone branches and on pull requests into `main`);
  `.gitattributes` keeps Go files LF.

## M1 Postgres + Event steps

M1 gives the app a database and the admin side of the system: an admin describes a venue and its
layouts, creates an event in one of those layouts with ticket tiers, and publishes it, which creates
the tickets that M3 will sell. The first REST endpoints arrive in step 5; steps 1–4 build what they
stand on. Every step includes its tests.

The model aims to match real ticketing platforms:

```
venue                        "Volkswagen Arena"
 └─ layout                   "Concert – standing floor", "Basketball", "Theatre setup"
     └─ section              seated: "Block 101" → seats (row "A", seat "12")
                             standing: "Floor" → a capacity, no seats
```

Every event uses one layout of its venue; a venue with only standing room has a layout with one
standing section. Every ticket has a place: a seat, or a standing section.

| Step | What | Spring Boot equivalent | Status |
|---|---|---|---|
| 1 | Postgres in Docker Compose; `DATABASE_URL` in config and `.env.example`; connection pool in `internal/platform/postgres`; `/readyz` also checks Postgres; the pool closes on shutdown | `spring.datasource.*` + HikariCP + Actuator DB health | Done |
| 2 | golang-migrate: a `migrations/` folder, `make migrate-up` and `make migrate-down`, the first table (`venues`: name, description, address, optional coordinates, time zone, optional capacity) | Flyway | Done |
| 3 | `internal/platform/postgres/pgtest`: starts a real Postgres with Testcontainers and applies the migrations; `go test -short` skips these tests; CI runs them | `@Testcontainers` + `@DataJpaTest` | Next |
| 4 | sqlc generates Go from the SQL in `internal/event/queries`; `WithTx` transaction helper; Postgres errors mapped to domain errors; first venue queries, tested against real Postgres | Spring Data repositories + `TransactionTemplate` + exception translation | Not started |
| 5 | **First REST endpoints:** venue service and CRUD handlers under `/api/v1/admin`; request validation; one JSON error format; domain error → HTTP status mapping | `@RestController` + Bean Validation + `@ControllerAdvice` | Not started |
| 6 | Layouts and sections: a venue's layouts; each layout's sections, either seated or standing with a capacity | Child entities + nested endpoints | Not started |
| 7 | Seats: the seats of a seated section (row and seat labels), created in bulk by rows | Bulk insert + validation | Not started |
| 8 | Events: CRUD for draft events; an event is one dated occurrence in one layout of its venue | Another `@RestController` + service | Not started |
| 9 | Ticket tiers: name, price (integer minor units + currency), description, and either a set of seats (whole sections or single seats) or a quantity in a standing section | Child entity + validation rules | Not started |
| 10 | Publish: a pure Go status table, a conditional status update, capacity checks, and ticket generation (one row per seat or standing place) in one transaction | Enum state machine + `@Transactional` service method | Not started |
| 11 | Cancel and edit rules: what can change after publishing, enforced with locks so inventory can't be removed underneath customers (invariant I14); layouts used by a published event are frozen and can be copied | Domain rules + pessimistic locking | Not started |
| 12 | Close M1: API reference in `docs/open`, an end-to-end test (venue → layout → seats → event → tiers → publish → tickets exist), CI green | springdoc / REST docs + `@SpringBootTest` | Not started |

### What M1 has delivered so far

- **Step 1:** `postgres:17.11-alpine` in Compose with `${...}` credentials and a `pg_isready` health
  check; `DATABASE_URL` is required and never printed; `postgres.NewPool` uses pgx's default pool
  settings and pings at startup, so a bad connection stops the app; `/readyz` pings the database
  with a 2s limit, `/healthz` doesn't touch it. Tests that need a real database use
  `TEST_DATABASE_URL`: CI provides a Postgres, and locally they are skipped when it isn't set, until
  step 3 replaces this with Testcontainers.
- **Step 2:** golang-migrate v4.20.1. `migrations/` holds the SQL files and embeds them into the
  program; `cmd/migrate` applies them (`up`, `down` one at a time, `version`, `force`) through
  `make migrate-up`, `migrate-down`, `migrate-version` and `migrate-force version=N`, without ever
  printing the password. The first migration creates `venues` with `CHECK`s for every rule the
  database can enforce. `pgtest.NewDatabase` gives each test a fresh database. Tests check the
  file naming rules, run every migration up, down and up again, show that a failed up or down is
  rolled back and marks the database dirty (a failed down at the version below it), and try each
  `venues` rule. CI also runs the make
  targets.

### Planned M1 endpoints

Paths are under `/api/v1/admin`. They are a plan and can change when each step is built.

| Step | Method and path | Action |
|---|---|---|
| 5 | `POST /venues`, `GET /venues`, `GET /venues/{id}`, `PATCH /venues/{id}`, `DELETE /venues/{id}` | Manage venues |
| 6 | `POST /venues/{id}/layouts`, `GET /venues/{id}/layouts`, `GET /layouts/{id}`, `PATCH /layouts/{id}`, `DELETE /layouts/{id}` | Manage a venue's layouts |
| 6 | `POST /layouts/{id}/sections`, `GET /layouts/{id}/sections`, `PATCH /sections/{id}`, `DELETE /sections/{id}` | Manage a layout's sections |
| 7 | `POST /sections/{id}/seats`, `GET /sections/{id}/seats`, `PATCH /seats/{id}`, `DELETE /seats/{id}` | Manage a seated section's seats; `POST` creates whole rows at once |
| 8 | `POST /events`, `GET /events`, `GET /events/{id}`, `PATCH /events/{id}`, `DELETE /events/{id}` | Manage events; only drafts can be deleted |
| 9 | `POST /events/{id}/tiers`, `GET /events/{id}/tiers`, `PATCH /tiers/{id}`, `DELETE /tiers/{id}` | Manage an event's ticket tiers |
| 10 | `POST /events/{id}/publish`, `GET /events/{id}/tickets` | Publish an event and list its tickets |
| 11 | `POST /events/{id}/cancel`, `POST /layouts/{id}/copy` | Cancel an event; copy a frozen layout to change it |

### What M1 leaves out

- **Login and permissions (M2):** the admin endpoints have no authentication during M1, so the app
  must not be deployed publicly until M2 is done. The `/api/v1/admin` route group gives M2 one
  place to add it.
- **Customer endpoints:** booking comes in M3, and search in M8. Customer read endpoints for
  events and availability are added in M3, when availability exists.
- **Refunds (M7):** cancelling an event in M1 only changes its status, because no bookings exist yet.
- **Drawing seat maps:** seats have labels but no screen coordinates, because there is no frontend.
  Coordinates can be added as columns if one is built.
- **Venue images, slugs and websites:** images need file storage, and slugs are for public pages,
  which arrive with the customer side.
