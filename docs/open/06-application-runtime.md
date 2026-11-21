# 06 · Application runtime

How the running application works today. This changes as milestones are added; planned behavior
is marked as such.

## Startup

The binary is `cmd/ticketing`. `main` creates the root `context.Context` with
`signal.NotifyContext`, so the context is cancelled on Ctrl+C (`os.Interrupt`) or SIGTERM. It
calls `run(ctx)` and exits with code 1 if `run` returns an error (the error is printed to stderr),
or 0 after a clean shutdown. `main` is the only place that calls `os.Exit`.

`run` is the composition root: it creates every part and connects them by hand, in this order:

1. **Config:** `config.Load(os.LookupEnv)` reads and validates the environment
   ([07-configuration](07-configuration.md)). Invalid values stop startup with all problems listed.
2. **Logger:** `telemetry.NewLogger(level, format, os.Stdout)`.
3. Log `starting ticketing` with selected config fields (never the whole config).
4. **Postgres:** `postgres.NewPool` creates the connection pool and pings the database, allowing
   5 seconds. If it can't connect, `run` fails here, before the port is opened. A deferred `Close()`
   closes the pool last, after the HTTP server. Then it logs `connected to postgres`.
5. **Services, readiness and router:** the module services on the pool (`event.NewVenueService`),
   a `httpapi.Readiness` (starts as not ready) and `httpapi.NewRouter`, which gets the services
   and also the pool for the `/readyz` check.
6. **Listener:** `net.Listen("tcp", HTTP_ADDR)` opens the port. If the port is taken, `run` fails
   here.
7. **Server:** an `http.Server` with the router as its handler and all timeouts from config.
   A deferred `Close()` in `run` closes it (listener and open connections) on every way out.
   `run` then hands the listener and server to `serve`, which does the remaining steps. (`serve`
   is separate so tests can run it with their own handler; whoever creates the server closes
   it.)
8. **Serve:** `server.Serve(listener)` runs in its own goroutine. It accepts connections, one
   goroutine per connection, and sends its result to a channel when it stops.
9. **Ready:** mark readiness as ready, log `http server listening`.
10. **Wait:** `serve` waits (`select`) for whichever comes first: the server stopping on its own,
   which is an error, or the context being cancelled, which starts the shutdown.

## Graceful shutdown

On Ctrl+C or SIGTERM `serve` logs `shutdown started` and then:

1. **Marks the app not ready**, so `/readyz` answers 503.
2. **Calls `server.Shutdown`** with a deadline of `SHUTDOWN_TIMEOUT` (default 20s). It stops
   accepting new connections, closes idle ones, and waits for in-flight requests to finish.
3. **Waits for `Serve` to return** `http.ErrServerClosed`, its normal way of ending after
   `Shutdown`.
4. Logs `shutdown complete` and returns `nil`, so the process exits with code 0.

If requests are still running when the deadline passes, `serve` (and so `run`) returns an error
(exit code 1) and the deferred `Close()` force-closes the remaining connections.

Both paths are covered by tests in `cmd/ticketing/main_test.go`: a request that is still running
when shutdown starts gets its full response, and one that outlives the deadline is cut off with a
deadline error.

After the first signal, signal handling is released: **a second Ctrl+C kills the process
immediately**, for when you don't want to wait for the deadline.

Notes:
- `Shutdown` closes the listener at once, so marking the app not ready mainly matters for
  requests already on open keep-alive connections. Behind a load balancer, a short pause between
  "not ready" and `Shutdown` (so the balancer notices) can be added if a deployment needs it.
- On Windows, Ctrl+C in the terminal triggers the shutdown. SIGTERM is what Linux containers
  receive from Docker and Kubernetes.

## Database

`internal/platform/postgres` creates a `pgxpool.Pool`, the Hikari equivalent. Nothing creates it
automatically: `run` builds it, passes it to whatever needs it, and closes it.

The pool uses pgx's defaults: at most 4 connections or the CPU count if that is larger, no minimum,
a 1-hour connection lifetime and a 30-minute idle timeout. `pool_max_conns` and similar settings can
be added to `DATABASE_URL` without code.

Unlike Hikari's `connectionTimeout`, the pool has no setting for how long to wait for a free
connection: a query waits until its `context` is cancelled or times out.

The tables come from the migrations in `migrations/`. The app doesn't run them and doesn't check
which version the database is at: `make migrate-up` is run before starting the app (see
[08-development](08-development.md#database-migrations)).

## HTTP server

`http.Server` is used instead of Gin's `router.Run()`, which has no timeouts. Each timeout limits
one phase of a request (values and defaults in [07-configuration](07-configuration.md)):

| Timeout | Limits | Protects against |
|---|---|---|
| `ReadHeaderTimeout` | reading the request headers | clients sending headers very slowly (Slowloris) |
| `ReadTimeout` | reading the whole request | slow uploads holding a connection |
| `WriteTimeout` | writing the response | slow readers holding a connection |
| `IdleTimeout` | idle keep-alive connections | idle connections piling up |

The server's own errors (e.g. a failed TLS handshake) are logged through slog at error level.

## Request pipeline

Gin runs in release mode, created with `gin.New()` (no built-in logger or recovery), and trusts
no proxy headers when working out the client IP. Every request passes through this middleware
chain, in order:

| Order | Middleware | What it does |
|---|---|---|
| 1 | `requestID` | Reuses a valid incoming `X-Request-ID` (1–64 chars of `A-Z a-z 0-9 - _ .`), otherwise generates a UUID. Stores it in the request's context and sets the `X-Request-ID` response header. |
| 2 | `requestLogger` | After the handler finishes, logs one `http request` line with `method`, `path`, `status`, `duration_ms`, `client_ip` (and `request_id`). Level: 5xx → ERROR, 4xx → WARN, `/healthz` and `/readyz` → DEBUG, everything else → INFO. |
| 3 | `recovery` | Turns a panic in a handler into a `500 {"error":"internal server error"}` and logs `panic recovered` with the panic value and stack trace. Panics in goroutines started by a handler are not caught. |
| 4 | handler | The route's handler. |

Unknown routes get Gin's default `404 page not found`.

## Endpoints

| Method | Path | Response | Purpose |
|---|---|---|---|
| GET | `/healthz` | `200 {"status":"ok"}` | **Liveness:** the process is alive. Always 200; it never checks dependencies, because restarting the app doesn't fix a database outage. |
| GET | `/readyz` | `200 {"status":"ready"}` or `503 {"status":"not ready"}` | **Readiness:** the app should receive traffic. Ready once the listener is open; not ready as soon as shutdown starts, or while Postgres doesn't answer a ping within 2 seconds. The reason is logged as `readiness check failed`, never sent to the client. It recovers by itself when Postgres returns. |
| POST | `/api/v1/admin/venues` | `201` with the venue and a `Location` header, or `400` / `413` / `415` | **Create a venue.** Admin API, no authentication until M2. |
| GET | `/api/v1/admin/venues?limit=20&cursor=...` | `200` with one page of venues, or `400` (bad `limit` or `cursor`) | **List venues** sorted by name, page by page. Admin API, no authentication until M2. |
| GET | `/api/v1/admin/venues/{id}` | `200` with the venue, or `404` (no venue has this id, or the id isn't a UUID) | **Read a venue.** Admin API, no authentication until M2. |

### Admin API

The admin routes are one Gin route group under `/api/v1/admin`, so M2 can add authentication to
all of them in one place. A request goes handler → service → generated queries → Postgres; the
handler only translates between HTTP and the service.

A venue looks like this. Names are snake_case, optional values are `null` (never left out), times
are RFC 3339 in UTC:

```json
{
  "id": "4851a6ac-f32c-4b12-a9d6-465900e510b1",
  "name": "Volkswagen Arena",
  "description": null,
  "address": {
    "line1": "Huzur Mah. Azerbaycan Cad. 1", "line2": null, "city": "Istanbul",
    "region": null, "postal_code": null, "country_code": "TR"
  },
  "location": {"latitude": 41.108934, "longitude": 29.010547},
  "time_zone": "Europe/Istanbul",
  "capacity": 5000,
  "created_at": "2026-11-19T22:41:42.814925Z",
  "updated_at": "2026-11-19T22:41:42.814925Z"
}
```

**Creating a venue:** the request body has the same shape without `id`, `created_at` and
`updated_at`. Required: `name`, `address.line1`, `address.city`, `address.country_code` and
`time_zone`; every other field may be left out or `null`. The service trims surrounding
whitespace from text, and optional text that is empty after trimming is stored as `null`. The
rules (checked in `event`, before Postgres sees the input):

| Field | Rule |
|---|---|
| `name`, `address.line1`, `address.line2` | at most 200 characters |
| `description` | at most 5000 characters |
| `address.city`, `address.region` | at most 100 characters |
| `address.postal_code` | at most 20 characters |
| `address.country_code` | two capital letters (the shape of an ISO 3166-1 alpha-2 code; whether the code is assigned is not checked) |
| `location` | both `latitude` (-90 to 90) and `longitude` (-180 to 180); 6 decimals are kept |
| `time_zone` | a name in the IANA time zone database, e.g. `Europe/Istanbul` (the binary embeds the database, `time/tzdata`) |
| `capacity` | a whole number from 1 to 2147483647 |

The body is read strictly: `Content-Type` must be `application/json`, the body at most 64 KiB, one
JSON object with no unknown fields (a typo like `capcity` is an error, not ignored) and nothing
after it.

**Listing venues:** `GET /api/v1/admin/venues` returns venues sorted by name (in the database's
collation), venues with the same name by id. The answer is one page:

```json
{"venues": [{"id": "...", "name": "Arena", ...}, ...], "next_cursor": "eyJuYW1lIjoiQ2x1YiIsImlkIjoi..."}
```

| Query parameter | Meaning |
|---|---|
| `limit` | venues per page, 1 to 100; default 20 |
| `cursor` | where the page starts: the `next_cursor` of the previous page; without it, the first page |

To read every venue, request pages until `next_cursor` is `null`. It is `null` on the last page,
also when that page is exactly full, so a client never gets an empty page at the end. An empty
list is `{"venues": [], "next_cursor": null}`.

The cursor is opaque: clients pass it back unchanged and never build one. Inside it are the name
and id of the previous page's last venue (base64url JSON), so the next page starts right after
that venue (keyset pagination). Venues added or deleted meanwhile don't shift the pages, and a
cursor still works after its venue was renamed or deleted. There is no total count. Unknown query
parameters are ignored.

Errors from the admin API are **RFC 9457 Problem Details** with `Content-Type:
application/problem+json`:

```json
{"type":"about:blank","title":"Not Found","status":404,"detail":"venue not found","instance":"/api/v1/admin/venues/abc"}
```

| Status | When | `detail` |
|---|---|---|
| `400` | invalid fields | "the request has invalid fields; see errors", plus an `errors` list with every problem at once: `[{"field":"address.city","message":"is required"}, ...]`. `field` is the path in the JSON. |
| `400` | a list query with a `limit` that isn't a whole number from 1 to 100, or a `cursor` that isn't a `next_cursor` | "the request has invalid fields; see errors", with `field` `limit` or `cursor` |
| `400` | a body that can't be read: empty, not valid JSON, a value of the wrong type, an unknown field, more than one object | what is wrong, e.g. `unknown field "capcity"` or `capacity must be a whole number` |
| `404` | no venue has the id, or the id isn't a UUID | "venue not found" |
| `413` | body over 64 KiB | "the request body must be at most 65536 bytes" |
| `415` | `Content-Type` isn't `application/json` | what to send instead |
| `500` | anything unexpected, e.g. the database fails | none; the cause is logged as `request failed` at ERROR, with the request ID |

A missing `location.latitude` or `location.longitude` is checked in the handler, before the
service, so it is reported on its own, before the other field problems. The `/healthz` and `/readyz` bodies, Gin's
`404 page not found` for unknown routes and the recovery middleware's 500 don't use this format yet.

## Logging

- `log/slog` writing to **stdout**; the platform (Docker, Kubernetes) collects it. The app never
  writes log files.
- `LOG_FORMAT=json` (default) writes one JSON object per line; `text` writes `key=value` lines for
  reading in a terminal. `LOG_LEVEL` sets the minimum level written.
- One logger is created in `run` and passed through constructors; there is no global logger.
- **Request IDs:** a wrapping slog handler (`telemetry`) adds `request_id` to every line logged
  with a context that carries one, like MDC in SLF4J. This only works with the `...Context` log
  methods (`logger.InfoContext(ctx, ...)`, `logger.Log(ctx, ...)`); lines logged without the
  request's context, such as the startup lines, have no `request_id`.

Example output:

```json
{"time":"…","level":"INFO","msg":"starting ticketing","http_addr":":8080","log_level":"info","log_format":"json"}
{"time":"…","level":"INFO","msg":"connected to postgres","host":"localhost","database":"ticketing"}
{"time":"…","level":"INFO","msg":"http server listening","addr":"[::]:8080"}
{"time":"…","level":"WARN","msg":"http request","method":"GET","path":"/nope","status":404,"duration_ms":0,"client_ip":"::1","request_id":"e9ad819b-a0d8-4eb2-9808-5c1c2432f07d"}
```
