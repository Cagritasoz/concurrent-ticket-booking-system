# 02 · Roadmap

## Where we are

- **Current milestone:** M0 Foundation, on branch `milestone/foundation`.
- **Done:** steps 1–6 (entry point, config, logging, HTTP server with health checks and request
  IDs, graceful shutdown, endpoint and shutdown tests).
- **Next:** step 7, Makefile, golangci-lint config and GitHub Actions CI.

Each milestone is developed on its own branch named `milestone/{milestone name}`, branched from
`main`. Every milestone ends with `go test -race` green (unit and integration tests) and CI passing.

## Milestones

| M | Name | Scope | Proof it's done | Status |
|---|---|---|---|---|
| 0 | Foundation | Entry point, config, logging, HTTP server, health checks, graceful shutdown, tooling and CI (see steps below) | Handler tests, shutdown test, CI green | In progress |
| 1 | Postgres + Event | Postgres in Compose, golang-migrate, sqlc, a shared test harness for Postgres; venues (optional seat layouts), events, ticket tiers, tickets (seated and general admission); admin CRUD; publish and cancel rules | Integration tests against real Postgres, domain unit tests | Not started |
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
| 7 | Makefile, golangci-lint config, GitHub Actions CI. (Untracking `.idea/` was done early.) | Maven/Gradle tasks + CI | Next (partly done) |

Tests are written within each step; step 6 added the larger handler and shutdown tests.

### What steps 1–6 delivered

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
