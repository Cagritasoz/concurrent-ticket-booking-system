# CLAUDE.md — Concurrent Ticket Booking System

This file gives Claude the full context of the project. Read it completely before doing anything.
Update the **Current status** section whenever a milestone step is finished.

---

## 1. Working agreement (read first)

This is a **serious learning project**. The owner is new to Go, Stripe, Redis and Elasticsearch,
and comes from **Java / Spring Boot**. They chose a realistic system on purpose because building
it is the best way to learn.

- **Go gently.** Keep language and implementations simple. Small steps, not big drops of code.
- **Explain thoroughly when asked.** Explain the *why*, not just the *what*. Comparisons to
  Java/Spring (e.g. `@Autowired`, `MockMvc`, `application.properties`, `synchronized`) help a lot.
- **Don't front-load concepts.** Ideas like the state machine, outbox or idempotency are explained
  when we reach the milestone that needs them. It is fine that the full architecture doesn't make
  sense to the owner yet; understanding grows as we build.
- **Do only what was asked.** Don't start the next step or milestone until the owner says so.
  Answer questions instead of piling on more work.
- **One branch per milestone**, named `milestone/{milestone name}` (e.g. `milestone/foundation`),
  branched from `main`.
- **Never commit or push unless explicitly told to.** When the owner says "generate commit
  details/descriptions", only write the commit message text in chat. Don't stage, commit or push.
- After every step: run the relevant tests (with `-race`) and fix failures before moving on.
  Never generate a large untested chunk of the system.
- `docs/PLAN.md` is a local-only planning file (excluded via `.git/info/exclude`). Never commit it.
  It may not exist on every machine; this file contains everything important from it.

---

## 2. Current status

- **Branch:** `milestone/foundation` (branched from `main` at `4507eab`).
- **Milestone:** M0 Foundation.
  - **Step 1 done:** `cmd/ticketing/main.go` with `main` → `run(ctx) error`; old `cmd/main.go` deleted.
  - **Step 2 done:** `internal/config` (`Load(os.LookupEnv)` → `Config`, defaults, `Validate()`
    reporting all errors with `errors.Join`), table-driven tests, `.env.example`, `docs/env.md`.
    `run` prints the config with `fmt` until the logger exists.
  - **Next step: M0 step 3** (logging with `log/slog`). Not started.
  - Done early from step 7: `.idea/` added to `.gitignore`; the owner untracks it with
    `git rm -r --cached .idea`.
- `main` contains only the early learning code, now under `examples/` (see section 9).
- Open decisions: see section 7.

---

## 3. Project goals

Build a concurrent ticket booking system. Priorities, in order:
**concurrency safety, payment idempotency, fault tolerance, testability, clean architecture.**

Architecture: a **modular monolith**, meaning one deployable Go application with clearly separated
modules. **No microservices.** No unnecessary infrastructure or abstractions.

Modules:
1. **event**: events, venues, ticket inventory, pricing (admin side).
2. **booking**: reservations, temporary holds, booking lifecycle, idempotency, Stripe payments,
   webhooks, reconciliation and refunds.
3. **search**: Elasticsearch indexing, full-text search, filtering, pagination.

### Tech stack

| Concern | Choice |
|---|---|
| Language | Go 1.27 (module `github.com/Cagritasoz/concurrent-ticket-booking-system`) |
| HTTP | Gin (only inside the HTTP layer) |
| Database | PostgreSQL via pgx + sqlc (type-safe code generated from SQL) |
| Migrations | golang-migrate (similar role to Flyway) |
| Holds / cache | Redis (`SET NX` with expiry), never the sole correctness mechanism |
| Search | Elasticsearch (official go-elasticsearch client) |
| Payments | Stripe PaymentIntents + webhooks (stripe-go) |
| Auth | Keycloak via OIDC (coreos/go-oidc); never a custom identity provider |
| Logging / metrics | `log/slog`, Prometheus client |
| Testing | `testing` package, Testcontainers for Go, race detector, fuzzing, k6 load tests |
| Local infra | Docker Compose |
| CI | GitHub Actions |
| Gateway | Traefik only if a real need appears (not planned yet) |

---

## 4. Requirements

### Concurrency and inventory
- A ticket must **never** be sold twice.
- **PostgreSQL is the source of truth** for ticket ownership and booking state. Use transactions,
  conditional updates, unique constraints and appropriate locking.
- Redis is for temporary holds and fast coordination only. Define safe behavior when Redis is down
  or loses data. Hold expiration and **token-safe** hold release.

### Booking and payment idempotency
- Clients send an idempotency key with booking requests. Records are stored in PostgreSQL with
  unique constraints. Reusing a key with a different payload is rejected.
- Stable Stripe idempotency keys per logical payment operation.
- Verify Stripe webhook signatures; dedupe webhooks by stored Stripe event ID.
- Handle timeouts, duplicates, crashes, retries and uncertain payment outcomes.
- A frontend redirect is **never** proof of payment.
- Explicit booking/payment state transitions, including expiration, failure, refund and
  reconciliation.

### Search
- Elasticsearch for discovery, filtering and relevance. PostgreSQL stays authoritative for
  availability, pricing and booking decisions. Support index rebuilds and reliable PG→ES sync.

### Auth
- Keycloak OIDC. Validate JWT signature, issuer, expiry, audience and permissions.
- Resource-level authorization in the app. Separate customer and event-admin permissions.

### Engineering
- Docker Compose for local infra. GitHub Actions for format, lint, unit tests, integration tests,
  race detection and coverage.
- Health and readiness endpoints, structured logs, metrics, graceful shutdown, HTTP timeouts.
- Secrets never in source control. Config via validated environment variables.
- Document architecture decisions, API, env vars, migrations and dev commands. Setup must be
  reproducible from a clean checkout.

### Testing
Unit tests (domain rules, state transitions), HTTP handler tests, integration tests with real
Postgres/Redis/Elasticsearch (Testcontainers), Stripe test-mode and webhook tests, concurrent
booking tests (only one customer wins a ticket), idempotency and duplicate-webhook tests,
failure-injection tests (Redis outage, DB errors, payment timeouts, retries, expired holds),
race detector, fuzz tests, k6. Prefer meaningful failure-path coverage over a 100% line target.

---

## 5. Target structure

```
.
├── cmd/
│   └── ticketing/main.go        # composition root: config → clients → modules → HTTP server + workers; graceful shutdown
├── internal/
│   ├── config/                  # env vars → typed Config + Validate()
│   ├── platform/                # infra client setup ONLY, no business rules
│   │   ├── postgres/            # pgxpool, WithTx helper, pg error mapping (23505 → domain error)
│   │   │   └── pgtest/          # Testcontainers Postgres + migrations, shared by integration tests
│   │   ├── redis/               # client constructor (returns error, never exits)
│   │   ├── elastic/             # client constructor
│   │   └── telemetry/           # slog setup, Prometheus registry
│   ├── auth/                    # OIDC verifier, Principal{Subject, Roles}, ctx helpers (no Gin)
│   ├── event/                   # MODULE: venues, events, price tiers, tickets
│   │   ├── queries/*.sql
│   │   └── eventdb/             # sqlc-generated
│   ├── booking/                 # MODULE: holds, bookings, idempotency, payments, webhooks, reconciliation
│   │   ├── booking.go           # Booking, Status, transition table (pure, unit-tested)
│   │   ├── service.go idempotency.go payment.go holds.go expiry.go reconcile.go
│   │   ├── queries/*.sql
│   │   ├── bookingdb/           # sqlc-generated
│   │   ├── redishold/           # HoldCache impl: SET NX PX + Lua compare-and-delete
│   │   └── stripepay/           # PaymentGateway impl (stripe-go) + webhook signature verification
│   ├── search/                  # MODULE: mapping, outbox consumer, queries, alias-swap rebuild
│   └── httpapi/                 # Gin: router, middleware, handlers, error→status mapping, health
├── examples/                    # early learning code, not part of the app
├── migrations/                  # golang-migrate files
├── sqlc.yaml
├── dev/keycloak/realm.json
├── loadtest/                    # k6 scripts
├── docs/                        # ADRs, api.md, env.md, development.md
├── docker-compose.yaml
├── Makefile
└── .github/workflows/ci.yaml
```

### Go conventions for this repo
- `context.Context` is the first parameter of anything doing I/O. Never create
  `context.Background()` inside library code.
- Library code returns errors and never calls `log.Fatal` / `os.Exit`; only `main` decides to exit.
- Wrap errors with `fmt.Errorf("...: %w", err)`; compare with `errors.Is` / `errors.As`.
- **Interfaces are few and defined by the package that uses them.** Only where something external
  must be faked or broken in tests: `PaymentGateway`, `HoldCache`, search indexer, clock.
  Postgres access stays concrete (sqlc + tx helper) and is tested against real Postgres.
- Explicit dependency injection through constructors (`NewXxx(deps...)`), wired by hand in
  `cmd/ticketing`. No DI framework, no global mutable state.
- **Gin only in `internal/httpapi`.** Services never see `*gin.Context`.
- Each module owns its SQL and its generated sqlc package. Modules talk through small interfaces
  (e.g. `booking` reads prices via a `TicketCatalog` interface that `event.Service` satisfies).
- Money is integer minor units + currency code, never floats.
- Background workers (hold expiry, payment reconciliation, search indexing) are goroutines in the
  same binary, coordinated with `SELECT … FOR UPDATE SKIP LOCKED` so multiple replicas are safe.
- Avoid copying Java/Spring patterns: no giant service structs, no Repository interface around
  every table, no generic `utils`/`common` packages.

---

## 6. Critical invariants

| # | Invariant | How it is enforced |
|---|---|---|
| I1 | A ticket is in at most one active (held/confirmed) booking. | Partial unique index `booking_items(ticket_id) WHERE active`; insert in sorted ticket order to avoid deadlocks; Postgres error 23505 → `ErrTicketUnavailable`. |
| I2 | Booking status only changes via allowed transitions; terminal states are final. | Go transition table + `UPDATE … WHERE id=$id AND status=$expected`; CHECK constraint on values. |
| I3 | CONFIRMED only after Stripe-verified success for the exact amount/currency. | Signed webhook or server-side PaymentIntent lookup. Never the frontend redirect. |
| I4 | Price is decided by the server and snapshotted. | `booking_items.price_cents`; payment amount comes from the booking. |
| I5 | Expiry releases inventory atomically. | One transaction sets EXPIRED + `active=false`. Postgres `expires_at` is authoritative; don't expire while payment is `processing`. |
| I6 | (customer, idempotency key) → at most one booking; same payload replays, different payload is rejected. | PK `(customer_id, key)` + `request_hash`; key + booking + stored response in one transaction. |
| I7 | One stable Stripe idempotency key per logical operation. | Save the `payment_attempt` row (with its key) before calling Stripe; retries reuse it. |
| I8 | Each Stripe event's effects apply at most once. | `stripe_events(event_id PK)` inserted in the same transaction as the state change. |
| I9 | At most one in-flight payment attempt per booking; never keep money without delivering tickets. | Partial unique index on active attempts; late-payment policy (section 7). |
| I10 | Redis never grants ownership; Redis loss/outage can't cause a double sale. | Redis is a contention pre-filter; Postgres decides. |
| I11 | Only the holder can release a hold. | Redis value = booking ID; Lua compare-and-delete. |
| I12 | Elasticsearch never drives booking/pricing and converges to Postgres. | Transactional outbox, external versioning, alias-swap rebuild; only coarse availability (e.g. sold-out flag) in ES. |
| I13 | Customers see only their bookings; admins manage only their events. | Role check in middleware + ownership check in services. |
| I14 | Held/sold inventory can't be removed underneath customers. | Admin edits lock the event `FOR UPDATE`, booking creation takes `FOR SHARE`; event cancel triggers refunds. |

Redis failure behavior: **down** → short timeout + circuit breaker, fall back to Postgres only
(metric + warning log). **Loses keys** → Postgres still rejects duplicates. **Stale key after a
crash** → the seat looks taken for at most the hold TTL.

Booking states (planned): `PENDING_PAYMENT → CONFIRMED | EXPIRED | CANCELLED`, then
`CONFIRMED → REFUND_PENDING → REFUNDED`. Payment attempt states:
`CREATING → REQUIRES_ACTION/PROCESSING → SUCCEEDED | FAILED | CANCELED`. A failed attempt
doesn't end the booking; the customer can retry until it expires. These are explained in M3/M6.

---

## 7. Decisions

| Date | Decision |
|---|---|
| 2026-10-14 | Module path `github.com/Cagritasoz/concurrent-ticket-booking-system`. |
| 2026-10-14 | Learning code moved to `examples/` (commit `4507eab`). |
| 2026-10-14 | Postgres is added in M1 (not M0) to keep M0 small. |
| 2026-10-20 | M0: tests are written in each step (not saved for step 6). Env vars have no prefix (`HTTP_ADDR`, ...). Default port `8080`. No `godotenv`: locally the owner feeds a `.env` file to the process with an IntelliJ/GoLand .env plugin, and the app reads plain env vars with defaults. |
| 2026-10-20 | Tooling: GNU `make` from MSYS2 (`C:\msys64\usr\bin`) for the Makefile, `golangci-lint` v2 for linting. No cross-platform task-runner alternative. |
| 2026-10-20 | Tests cover only the real code (`./cmd/... ./internal/...`). `examples/` is left untouched and its tests are not part of the step/CI test runs. |
| open (decide in M1) | **Inventory model.** Recommended: one `tickets` row per sellable unit, including general admission (N identical rows), so rule I1 covers everything. Alternative: a `remaining` counter for GA (compact, but a single hot row and a second mechanism). Explained to the owner on 2026-10-14. |
| open (decide in M7) | **Late payment on an expired booking.** Recommended: pause expiry while payment is processing; on a late success, reclaim the same tickets if free, otherwise refund automatically. Possible later upgrade: Stripe authorize-then-capture (`capture_method=manual`). "Always refund" was rejected as needlessly unfriendly. Explained on 2026-10-14. |

---

## 8. Milestones

Every milestone ends with `go test -race` green (unit + integration) and CI passing.

| M | Scope | Proof it's done |
|---|---|---|
| **0 Foundation** | See steps below. | Handler tests, shutdown test, CI green |
| 1 Postgres + Event | Postgres in compose, golang-migrate, sqlc, `pgtest` harness; venues/events/tiers/tickets; admin CRUD; publish/cancel rules. | Integration tests vs real Postgres, domain unit tests |
| 2 Auth | Keycloak realm in compose, go-oidc middleware, `Principal`, customer/event_admin roles, ownership checks. | Test JWKS: bad signature/issuer/audience/expiry, missing role, other user's resource; one Keycloak container test |
| 3 Booking core | Bookings, I1 index, state machine, expiry sweeper, `POST /bookings`; port the 10k-goroutine test to Postgres. | Exactly one winner, expiry frees seats, multi-ticket all-or-nothing, deadlock-free ordering |
| 4 Idempotency | `Idempotency-Key` header, request hash, response replay. | Concurrent duplicates, payload mismatch, rollback-on-crash, fuzzed canonicalization |
| 5 Redis holds | `redishold` (SET NX PX, Lua release), timeouts, circuit breaker, fail-open. | Redis stopped/paused/flushed mid-test: still exactly one winner |
| 6 Payments | `PaymentGateway`, stripe-go adapter, payment attempts, webhook (raw body, signature, dedupe). | Fake gateway tests, signed test payloads, duplicate/out-of-order webhooks, env-gated Stripe test-mode test, `stripe listen` |
| 7 Fault tolerance | Reconciler, late-payment policy, refunds, timeouts, uncertain outcomes. | Timeout, crash-between-steps, retry tests |
| 8 Search | ES in compose, outbox, indexer, mapping, filters, `search_after` pagination, alias-swap rebuild. | Testcontainers ES: sync, rebuild, ordering |
| 9 Hardening | Prometheus metrics, k6 (hot seat, browse/search mix), fuzz tests, coverage in CI, docs/ADRs. | k6 thresholds; post-run SQL check for zero double sales |

### M0 steps

| Step | What we build | Spring Boot equivalent |
|---|---|---|
| 1 | `cmd/ticketing/main.go`: the starting point where all parts are created and connected by hand (replaces `cmd/main.go`) | `@SpringBootApplication` + ApplicationContext |
| 2 | `internal/config`: read env vars into a struct and validate | `application.properties` + `@ConfigurationProperties` + `@Validated` |
| 3 | Logging with `log/slog` | SLF4J + Logback |
| 4 | Gin HTTP server with timeouts, `/healthz` (alive) and `/readyz` (ready for traffic) | Spring MVC + Actuator liveness/readiness |
| 5 | Graceful shutdown on Ctrl+C / SIGTERM | `server.shutdown=graceful` |
| 6 | Endpoint tests with `net/http/httptest` | `MockMvc` |
| 7 | Makefile, golangci-lint, GitHub Actions CI, untrack `.idea/` | Maven/Gradle tasks + CI |

Testing conventions: unit tests run with `go test -short`; integration tests call `t.Skip` when
`testing.Short()` is true and start one container per package in `TestMain`. CI runs both with
`-race`.

---

## 9. The `examples/` folder

Early learning code (movie/seat domain): `MemoryStore` (not thread-safe), `ConcurrentStore`
(`sync.RWMutex`), `RedisStore` (`SET NX` holds) and a Redis client in
`examples/adapters/redis`. `examples/booking/service_test.go` runs 10,000 goroutines against one
seat and expects exactly one winner (needs Redis running). Kept as a reference only.

Known flaws there, which the real implementation must **not** repeat:
- `session:{id}` stores only one seat key; a second hold overwrites it and orphans the first seat.
- The cleanup `DEL` isn't token-safe, so it can delete someone else's hold.
- Redis is the only correctness mechanism (Postgres must decide instead).
- `context.Background()` is created inside methods instead of passing `ctx` in.
- `log.Fatalf` inside library code (`examples/adapters/redis/redis.go`).
- `defaultHoldTTL` was defined twice with different values (old `cmd/main.go` 2m, since deleted;
  `redis_store.go` 3m). Config values must have one source.
- `Status` is a free-form string instead of a typed state.

---

## 10. Development environment and commands

```bash
docker compose up -d                      # Redis (6379) + redis-commander UI (8081)
go build ./... && go vet ./...
go test -count=1 -race ./cmd/... ./internal/...   # the real code; race detector needs cgo (see below)
go test -v -count=1 -race ./examples/booking/     # examples only, run by hand when needed (needs Redis)
```

**`-race` requires cgo, and cgo requires a C compiler (`gcc`).** If Go reports
`-race requires cgo; enable cgo by setting CGO_ENABLED=1`, the machine has no gcc on `PATH`, so
Go has turned cgo off.
- Windows (main desktop): MSYS2 UCRT64 gcc at `C:\msys64\ucrt64\bin` (on the user PATH; open a new
  terminal after installing). If `go env CGO_ENABLED` still shows `0`, run
  `go env -w CGO_ENABLED=1`.
- Install on another Windows machine: MSYS2, then `pacman -S mingw-w64-ucrt-x86_64-gcc`.
  macOS: `xcode-select --install`. Linux: `build-essential`.
- Docker Desktop must be running; check with `docker ps`. If containers are missing, run
  `docker compose up -d`.

Shell notes for Claude: the desktop uses Windows PowerShell 5.1 (no `&&`; use `; if ($?) { … }`)
and Git Bash is also available. Run `go test -race` from **PowerShell**: in Claude's Git Bash the
cgo step of the race build fails (`runtime/cgo: cgo.exe: exit status 2`), while the same command
works in PowerShell. Likely cause (not confirmed): McAfee blocks the program, and the owner sees a
McAfee "stopped an app from running" popup at the same time.
