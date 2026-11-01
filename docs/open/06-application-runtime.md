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
4. **Readiness and router:** a `httpapi.Readiness` (starts as not ready) and `httpapi.NewRouter`.
5. **Listener:** `net.Listen("tcp", HTTP_ADDR)` opens the port. If the port is taken, `run` fails
   here.
6. **Server:** an `http.Server` with the router as its handler and all timeouts from config.
   A deferred `Close()` closes it (listener and open connections) on every way out of `run`.
7. **Serve:** `server.Serve(listener)` runs in its own goroutine. It accepts connections, one
   goroutine per connection, and sends its result to a channel when it stops.
8. **Ready:** mark readiness as ready, log `http server listening`.
9. **Wait:** `run` waits (`select`) for whichever comes first: the server stopping on its own,
   which is an error, or the context being cancelled, which starts the shutdown.

## Graceful shutdown

On Ctrl+C or SIGTERM `run` logs `shutdown started` and then:

1. **Marks the app not ready**, so `/readyz` answers 503.
2. **Calls `server.Shutdown`** with a deadline of `SHUTDOWN_TIMEOUT` (default 20s). It stops
   accepting new connections, closes idle ones, and waits for in-flight requests to finish.
3. **Waits for `Serve` to return** `http.ErrServerClosed`, its normal way of ending after
   `Shutdown`.
4. Logs `shutdown complete` and returns `nil`, so the process exits with code 0.

If requests are still running when the deadline passes, `run` returns an error (exit code 1) and
the deferred `Close()` force-closes the remaining connections.

After the first signal, signal handling is released: **a second Ctrl+C kills the process
immediately**, for when you don't want to wait for the deadline.

Notes:
- `Shutdown` closes the listener at once, so marking the app not ready mainly matters for
  requests already on open keep-alive connections. Behind a load balancer, a short pause between
  "not ready" and `Shutdown` (so the balancer notices) can be added if a deployment needs it.
- On Windows, Ctrl+C in the terminal triggers the shutdown. SIGTERM is what Linux containers
  receive from Docker and Kubernetes.

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
| GET | `/readyz` | `200 {"status":"ready"}` or `503 {"status":"not ready"}` | **Readiness:** the app should receive traffic. Ready once the listener is open; not ready again as soon as shutdown starts. From M1 also not ready when Postgres is unreachable. |

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
{"time":"…","level":"INFO","msg":"http server listening","addr":"[::]:8080"}
{"time":"…","level":"WARN","msg":"http request","method":"GET","path":"/nope","status":404,"duration_ms":0,"client_ip":"::1","request_id":"e9ad819b-a0d8-4eb2-9808-5c1c2432f07d"}
```
