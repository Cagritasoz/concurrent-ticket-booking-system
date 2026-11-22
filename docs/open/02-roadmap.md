# 02 · Roadmap

## Where we are

- **Current milestone:** M1 Postgres + Event, on branch `milestone/postgres-event`.
- **Finished:** M0 Foundation, merged into `main` with CI green (entry point, config, logging,
  HTTP server with health checks and request IDs, graceful shutdown, endpoint and shutdown tests,
  Makefile, lint config and CI workflow).
- **Done in M1:** step 1 (Postgres in Docker Compose, connection pool, `/readyz` checks Postgres),
  step 2 (golang-migrate, `cmd/migrate`, the `venues` table), step 3 (tests start their own
  Postgres with Testcontainers), step 4 (sqlc venue queries, `WithTx`, constraint error checks),
  steps 5a and 5b (the venue service and the first REST endpoints: create, read, list in keyset
  pages, update with JSON Merge Patch in a locked transaction, and delete, under
  `/api/v1/admin/venues`, with validation in the service and Problem Details errors), step 5c
  (venues get a lifecycle, archive and restore, and optimistic locking with `ETag` and a required
  `If-Match`; their capacity moved to halls).
- **In progress:** M1 step 6a, the schema of the whole venue tree and of events, one migration per
  table (venues, halls, layouts, sections, seats, events), written and tested; the owner is
  reviewing it, and the generated Go code isn't updated for seats and events yet. Step 6c's first
  layout queries exist; they don't use the status and version yet.
- **Next:** after the review, `make generate`, then M1 step 6b, halls end to end, built like
  venues (archive, restore, `If-Match`).

Each milestone is developed on its own branch named `milestone/{milestone name}`, branched from
`main`. Every milestone ends with `go test -race` green (unit and integration tests) and CI passing.

## Milestones

| M | Name | Scope | Proof it's done | Status |
|---|---|---|---|---|
| 0 | Foundation | Entry point, config, logging, HTTP server, health checks, graceful shutdown, tooling and CI (see steps below) | Handler tests, shutdown test, CI green | Done |
| 1 | Postgres + Event | Postgres in Compose, golang-migrate, sqlc, a shared test harness for Postgres; venues with halls and layouts (seated sections with seats, standing sections with a capacity), events, ticket tiers, tickets (seated and standing); admin CRUD; publish and cancel rules | Integration tests against real Postgres, domain unit tests | In progress |
| 2 | Auth | Keycloak realm in Compose, OIDC middleware, `Principal`, venue-manager, organizer and customer roles; organizations with memberships, venues owned by an operator organization, venue access grants for organizers, events owned by their organizer; ownership checks in the services | Tests with a test key set: bad signature, issuer, audience, expiry, missing role, another user's resource; one Keycloak container test | Not started |
| F | Frontend | TypeScript + React, after M2 (it needs login): first the **admin UI** (venues down to seats, events, tiers, publishing), then the **customer UI** growing with M3 onward (browse, seat map, booking, payment). The owner describes how it should look; Claude writes it. Real-time seat updates (WebSockets considered) come with the customer UI | Component tests; the main flows tried in a browser against the running app | Not started |
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

M1 gives the app a database and the admin side of the system: an admin describes a venue, its
halls and their layouts, creates an event in one of those layouts with ticket tiers, and publishes
it, which creates the tickets that M3 will sell. The first REST endpoints arrive in step 5b; steps
1–5a build what they stand on. Every step includes its tests.

The model aims to match real ticketing platforms:

```
venue                        "Zorlu PSM" (the building)
 └─ hall                     "Turkcell Stage", "Studio" (rooms; can host events at the same time)
     └─ layout               "Concert – standing floor", "Theatre setup" (one at a time per hall)
         └─ section          seated: "Block 101" → seats (row "A", seat "12")
                             standing: "Floor" → a capacity, no seats
```

Every event uses one layout of a hall; a venue with one room has one hall, and a hall with only
standing room has a layout with one standing section. Every ticket has a place: a seat, or a
standing section.

| Step | What | Spring Boot equivalent | Status |
|---|---|---|---|
| 1 | Postgres in Docker Compose; `DATABASE_URL` in config and `.env.example`; connection pool in `internal/platform/postgres`; `/readyz` also checks Postgres; the pool closes on shutdown | `spring.datasource.*` + HikariCP + Actuator DB health | Done |
| 2 | golang-migrate: a `migrations/` folder, `make migrate-up` and `make migrate-down`, the first table (`venues`: name, description, address, optional coordinates, time zone, optional capacity) | Flyway | Done |
| 3 | `internal/platform/postgres/pgtest`: starts a real Postgres with Testcontainers and applies the migrations; `go test -short` skips these tests; CI runs them | `@Testcontainers` + `@DataJpaTest` | Done |
| 4 | sqlc generates Go from the SQL in `internal/event/queries`; `WithTx` transaction helper; broken constraints recognised by name, for modules to map to their own errors; first venue queries, tested against real Postgres | Spring Data repositories + `TransactionTemplate` + exception translation | Done |
| 5a | **Venue service** in `internal/event`: its own `Venue` type, the venue's errors (e.g. not found), validation of admin input reporting every problem at once, get / create / list (keyset pages) / update (in a transaction, row locked) / delete; tested against real Postgres | `@Service` + Bean Validation | Done |
| 5b | **First REST endpoints:** venue CRUD handlers under `/api/v1/admin`; strict JSON decoding; RFC 9457 Problem Details as the one error format; error → HTTP status mapping (validation errors are 400); `httptest` tests | `@RestController` + `@ControllerAdvice` + `ProblemDetail` + `MockMvc` | Done |
| 5c | **Venue lifecycle and optimistic locking:** `status` (active / archived) with archive and restore as compare-and-set requests, the list filtered by status; `version` sent as the `ETag`, a required `If-Match` on every change (412 / 428); DELETE only with `If-Match`; the venue's capacity removed (halls have it) | `@Version` + `ETag` / `If-Match` + soft delete | Done |
| 6a | **Schema of the venue tree and events,** one migration per table, before any of them is built end to end, so later tables don't force changes to finished code: `halls` (a venue's rooms), `layouts` (a hall's setups), `sections` (seated, or standing with a capacity), `seats` (labels, positions and accessibility of a seated section's seats) and `events` (one dated occurrence in a layout); names unique per parent among what isn't archived, the tree deleted with its parent, events keeping their layout from being deleted; statuses and versions as for venues | `@OneToMany(cascade = REMOVE)` child entities, written as Flyway migrations | In progress (tables done and tested; owner review, then `make generate`) |
| 6b | **Halls end to end:** queries, service and nested endpoints under a venue, built like venues (archive, restore, `If-Match`); creating a hall locks its venue to check it is active | Child entity repository, service + nested `@RestController` | Not started |
| 6c | **Layouts end to end:** the same for layouts under a hall, with a `draft` status before `active` | The same, one level down | In progress (first queries done; status and version not used yet) |
| 6d | **Sections end to end:** queries, service and nested endpoints under a layout; a version but no status (sections belong to their layout's definition) | The same, one level down | Not started |
| 7 | **Seats end to end** (table from 6a): queries, service and endpoints for a seated section's seats, created in bulk by rows; a version but no status, like sections | Bulk insert + validation | Not started |
| 8 | **Events end to end** (table from 6a): CRUD for draft events in an active layout; delete only drafts | Another `@RestController` + service | Not started |
| 9 | Ticket tiers: name, price (integer minor units + currency), description, and either a set of seats (whole sections or single seats) or a quantity in a standing section | Child entity + validation rules | Not started |
| 10 | Publish: a pure Go status table, a conditional status update, capacity checks, and ticket generation (one row per seat or standing place) in one transaction | Enum state machine + `@Transactional` service method | Not started |
| 11 | Cancel and edit rules: what can change after publishing, enforced with locks so inventory can't be removed underneath customers (invariant I14); layouts used by a published event are frozen and can be copied | Domain rules + pessimistic locking | Not started |
| 12 | Close M1: API reference in `docs/open`, an end-to-end test (venue → hall → layout → seats → event → tiers → publish → tickets exist), CI green | springdoc / REST docs + `@SpringBootTest` | Not started |

### What M1 has delivered so far

- **Step 1:** `postgres:17.11-alpine` in Compose with `${...}` credentials and a `pg_isready` health
  check; `DATABASE_URL` is required and never printed; `postgres.NewPool` uses pgx's default pool
  settings and pings at startup, so a bad connection stops the app; `/readyz` pings the database
  with a 2s limit, `/healthz` doesn't touch it. Tests that need a real database use
  `TEST_DATABASE_URL`: CI provides a Postgres, and locally they are skipped when it isn't set, until
  step 3 replaced this with Testcontainers.
- **Step 2:** golang-migrate v4.20.1. `migrations/` holds the SQL files and embeds them into the
  program; `cmd/migrate` applies them (`up`, `down` one at a time, `version`, `force`) through
  `make migrate-up`, `migrate-down`, `migrate-version` and `migrate-force version=N`, without ever
  printing the password. The first migration creates `venues` with `CHECK`s for every rule the
  database can enforce. `pgtest.NewDatabase` gives each test a fresh database. Tests check the
  file naming rules, run every migration up, down and up again, show that a failed up or down is
  rolled back and marks the database dirty (a failed down at the version below it), and try each
  `venues` rule. CI also runs the make
  targets.
- **Step 3:** testcontainers-go v0.45.0. `pgtest` starts `postgres:17.11-alpine` on the first test
  that needs it, shares it within the test package and removes it in `pgtest.Main` (every such
  package has a one-line `TestMain`); `TEST_DATABASE_URL` still overrides it. `NewMigratedDatabase`
  gives a database with every migration applied. `go test -short` (`make test-short`) skips these
  tests; without Docker or `TEST_DATABASE_URL` they fail instead of skipping. All tests that used
  `TEST_DATABASE_URL` directly now go through `pgtest`, and CI's tests use Testcontainers too (its
  Postgres service remains for the migration targets).
- **Step 4:** sqlc v1.31.1 (`sqlc.yaml`, `make generate`) turns `internal/event/queries/venues.sql`
  into `internal/event/eventdb`: create, get, get-and-lock (`FOR UPDATE`), a keyset-paginated list
  by name (with an index, now in migration 1), update and delete. Standard Go types in the generated
  code (`uuid.UUID`, `time.Time`, pointers for `NULL`). `postgres.WithTx` commits or rolls back
  (also on a panic); `postgres.IsUniqueViolation`, `IsForeignKeyViolation` and `IsCheckViolation`
  recognise a broken constraint by name. Tests against real Postgres cover every query, paging
  through equal names, each WithTx outcome (a failing commit included) and each error check; 20
  concurrent read-change-write transactions lose no update with `FOR UPDATE` (without it, 15 of 20
  were lost). CI checks the generated code with `sqlc diff`.
- **Step 5a:** `event.VenueService` with `Get`, built on the generated queries
  (no repository layer of its own). It returns the module's own `event.Venue` (address grouped,
  coordinates as one optional `Location`, capacity as `*int`, removed in step 5c), converted from the row in one
  place, so `httpapi` will never import `eventdb`. An unknown id is `event.ErrVenueNotFound`; any
  other database error is wrapped and passed on. Tests against real Postgres: every field read
  back, optional fields `nil`, unknown id, and a failing database that isn't reported as "not
  found". `Create` cleans and checks an `event.VenueInput` first (text trimmed, blank optional
  text as `nil`, the length rules of the table's `CHECK`s, country code shape, coordinate ranges,
  IANA time zone, capacity range) and returns every problem at once as a `*event.ValidationError`;
  nothing is stored then. Validation has its own unit tests, without a database. `List` returns
  one keyset page (`event.VenuePage`: the venues and an `event.VenueCursor` for the next page,
  nil on the last), 1 to 100 venues, fetching one row more than asked to know whether another
  page follows. Tests walk every page, including equal names across a page boundary, a full last
  page without a next cursor, and a cursor whose venue was deleted meanwhile. `Update` locks the
  venue (`GetVenueForUpdate` in `postgres.WithTx`), hands its current values to the caller's
  change function, checks the result with Create's rules and writes it back; an error or a broken
  rule stores nothing. 20 concurrent updates that each add 1 to the capacity lose none. `Delete`
  turns "0 rows deleted" into `event.ErrVenueNotFound`.
- **Step 5b:** `GET /api/v1/admin/venues/{id}` in an `/api/v1/admin` route group,
  wired in `cmd/ticketing` (`event.NewVenueService(pool)` → `httpapi.NewRouter`). The JSON form
  of a venue lives in `httpapi` (snake_case, optional values `null`, times in UTC). Errors are RFC
  9457 Problem Details (`application/problem+json`): an unknown or malformed id is 404, any other
  error a 500 without detail, logged with the request ID. Tests send requests through the whole
  stack (router, handler, real service, real Postgres) and compare the exact JSON.
  `POST /api/v1/admin/venues` reads the body strictly (`application/json` only, 64 KiB, no
  unknown fields, one object) and answers 201 with a `Location` header; invalid fields are a 400
  with an `errors` list, an unusable body a 400 / 413 / 415 with our own message. The binary
  embeds the time zone database (`time/tzdata`). `GET /api/v1/admin/venues?limit=&cursor=`
  returns `{"venues": [...], "next_cursor": ...}`; the cursor is opaque to clients (base64url
  JSON), and a bad `limit` or `cursor` is a 400 with an `errors` list.
  `PATCH /api/v1/admin/venues/{id}` takes a JSON Merge Patch (RFC 7396; `application/json` is
  accepted too): fields left out are kept, `null` clears a field, nested objects are merged. The
  body is checked against the create body's shape first, for the same error messages, then
  applied in the service's transaction; 200 with the venue as stored. `DELETE` answers 204, or 404
  for an unknown id (also when deleting twice).
- **Step 5c:** migration 1 now holds the whole venues table: `status` (`active` / `archived`,
  default active), `version` (default 1) and the `(status, name, id)` list index; `capacity` is
  gone (migrations are edited until M1 merges, see the decisions). `VenueService` takes the
  version a change is based on: `Update` checks it inside its locked transaction, and the new
  `Archive`, `Restore` and `Delete` are compare-and-set statements (`... WHERE id AND version AND
  status`) that read the venue only when nothing matched, to say why: `ErrVenueNotFound`,
  `ErrVersionMismatch` or `ErrVenueArchived` / `ErrVenueNotArchived`. `List` takes a status. The
  API sends the version as `ETag` and in the JSON (`status`, `version`), adds `POST
  /venues/{id}/archive` and `/restore` and `?status=archived`, and requires `If-Match` on PATCH,
  DELETE, archive and restore: 412 for another version, 428 when missing or `*`, 400 when
  malformed, 409 for a change to an archived venue. Tests: 20 concurrent changes from the same
  version have exactly one winner; 20 clients that retry after a 412 lose no change; every
  refusal names its reason and changes nothing.
- **Step 6a (in progress, tables done):** one migration per table: 2 `halls` (venue, name,
  optional description and capacity), 3 `layouts` (hall, name, optional description), 4
  `sections` (layout, name, kind `seated` or `standing`, a capacity only for standing sections),
  5 `seats` (section, row and seat labels, row and seat positions, type `standard` /
  `wheelchair` / `companion`, restricted view with an optional note, aisle) and 6 `events`
  (layout, name, optional description, start, optional end and door time, status `draft` /
  `published` / `cancelled` with matching `published_at` / `cancelled_at`). Halls have a
  `status` (`active` / `archived`, default active), layouts one with a draft first (`draft` /
  `active` / `archived`, default draft), sections and seats none; every table has a `version`
  (default 1). Names are unique per parent, ignoring case, through the unique indexes
  `halls_venue_id_name_key`, `layouts_hall_id_name_key` (both partial, `WHERE status <>
  'archived'`, so an archived hall or layout frees its name) and `sections_layout_id_name_key`;
  seats are unique per section by position (`seats_section_id_position_key`) and by labels
  (`seats_section_id_label_key`). A partial index can't find archived rows, so
  `halls_venue_id_idx` and `layouts_hall_id_idx` serve the foreign keys and the lists. Seats
  reference their section by `(id, kind)` (`seats_section_id_fkey`), so the database keeps
  seats out of standing sections and keeps a section with seats from turning standing. Deleting
  a venue, hall, layout or section deletes everything below it (`ON DELETE CASCADE`), but
  `events_layout_id_fkey` has no cascade: a layout, hall or venue that an event uses can't be
  deleted. `events_layout_id_starts_at_idx` serves that foreign key and the "upcoming events in
  this layout" check; `events_status_starts_at_id_idx` the admin list. Tests try each table
  rule, the defaults, names and seat places by index name, names freed by archiving, seats only
  in seated sections, the cascade from each level, and events blocking deletes. Still to do:
  `make generate` after the review (`eventdb.Seat` and `eventdb.Event`; until then `sqlc diff`
  and so CI report `models.go` as out of date).
- **Step 6c (in progress, first queries done):** `internal/event/queries/layouts.sql`: create,
  get, get-and-lock (`FOR UPDATE`), list a hall's layouts (all of them, by name ignoring case,
  then id, no paging), update (name and description; a layout never changes hall) and delete.
  They don't use the status and version yet: listing by status, the version check, compare-and-set
  status changes and locking the hall come with the rest of 6c. Tests against real Postgres check
  each query, and that an unknown hall breaks `layouts_hall_id_fkey` and a taken name (on create
  and on rename) breaks `layouts_hall_id_name_key`, the names the service will map to its errors.

### Planned M1 endpoints

Paths are under `/api/v1/admin`. They are a plan and can change when each step is built.

| Step | Method and path | Action |
|---|---|---|
| 5b, 5c | `POST /venues`, `GET /venues`, `GET /venues/{id}`, `PATCH /venues/{id}`, `POST /venues/{id}/archive`, `POST /venues/{id}/restore`, `DELETE /venues/{id}` | Manage venues |
| 6b | `POST /venues/{id}/halls`, `GET /venues/{id}/halls`, `GET /halls/{id}`, `PATCH /halls/{id}`, `POST /halls/{id}/archive`, `POST /halls/{id}/restore`, `DELETE /halls/{id}` | Manage a venue's halls |
| 6c | `POST /halls/{id}/layouts`, `GET /halls/{id}/layouts`, `GET /layouts/{id}`, `PATCH /layouts/{id}`, `POST /layouts/{id}/activate`, `POST /layouts/{id}/archive`, `POST /layouts/{id}/restore`, `DELETE /layouts/{id}` | Manage a hall's layouts |
| 6d | `POST /layouts/{id}/sections`, `GET /layouts/{id}/sections`, `PATCH /sections/{id}`, `DELETE /sections/{id}` | Manage a layout's sections |
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
- **Drawing seat maps:** seats get labels but no screen coordinates, because the frontend comes
  after M2. Coordinates can be added as columns when it draws maps.
- **Venue images, slugs and websites:** images need file storage, and slugs are for public pages,
  which arrive with the customer side.
