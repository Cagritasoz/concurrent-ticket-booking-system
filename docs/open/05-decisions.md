# 05 · Decisions

A log of the decisions that shape the project. Open decisions list the recommended option and the
milestone in which they will be settled.

## Made

| Date | Decision |
|---|---|
| 2026-10-14 | Module path `github.com/Cagritasoz/concurrent-ticket-booking-system`. |
| 2026-10-14 | The early learning code was moved to `examples/` (see [09-examples](09-examples.md)). |
| 2026-10-14 | Postgres is added in M1, not M0, to keep the foundation small. |
| 2026-10-20 | One branch per milestone, named `milestone/{milestone name}`, branched from `main`. |
| 2026-10-20 | M0: tests are written in each step, not saved for the end. |
| 2026-10-20 | Environment variables have no project prefix (`HTTP_ADDR`, `LOG_LEVEL`, ...). The default port is `8080`. |
| 2026-10-20 | No `.env` loading library (such as godotenv). The app reads plain environment variables with defaults; locally a `.env` file is passed to the process by the IDE's .env plugin. `.env` is ignored by git. |
| 2026-10-20 | Tooling: GNU `make` (on Windows from MSYS2) for the Makefile, `golangci-lint` v2 for linting. No cross-platform task-runner alternative. |
| 2026-10-20 | Test runs (local and CI) cover only the application code, `./cmd/... ./internal/...`. `examples/` is left untouched and its tests are run by hand only. |
| 2026-10-20 | `.idea/` is not tracked by git. |
| 2026-10-21 | Logs carry a request ID: a valid incoming `X-Request-ID` is reused, otherwise a UUID is generated. It is stored in the request context and added to every log line written with that context. |
| 2026-10-31 | The `http.Server` is created only after the listener is open, and closed by a deferred `Close()` on every exit path of `run`. Whoever creates a resource closes it; `serve` only uses the server it is given. |
| 2026-11-01 | Graceful shutdown order: mark not ready → `server.Shutdown` within `SHUTDOWN_TIMEOUT` → force-close what's left and exit 1 if the deadline passes. A second Ctrl+C exits immediately. No pause between "not ready" and `Shutdown` until a deployment behind a load balancer needs one. |
| 2026-11-01 | The system serves any kind of event (movie showings, concerts, festivals), seated and general admission alike. Every event is one dated occurrence at a venue; a movie shown 30 times is 30 events. |
| 2026-11-01 | Ticket tiers (e.g. Standard, Pro, Ultimate) start with a name, a price and a description. The design must make two later additions easy without reworking bookings: **tier rules that are enforced** (e.g. sales windows, presale codes, access zones) and **multi-event passes** (one purchase covering tickets in several events). Neither is built until it is needed. |
| 2026-11-01 | **Per-customer purchase limits** per tier (e.g. at most 4 Ultimate tickets per customer) are part of the booking rules (M3). |
| 2026-11-01 | **Inventory model:** Venue (optionally with a seat layout) → Event → TicketTier (price, description; later rules) → **one `tickets` row per sellable unit**, linked to a seat for seated events and without a seat for general admission. The tier decides the price, the seat decides the place. Invariant I1 then covers seated and general admission alike, and concurrent general-admission buyers lock different rows (`FOR UPDATE SKIP LOCKED`) instead of queuing on one hot `remaining` counter. Bookings separate *what was bought at what price* (booking lines: a tier ticket, later a pass) from *which inventory units are reserved* (booking items → tickets), so a multi-event pass is one line reserving tickets in several events. |
| 2026-11-01 | **Seat selection:** for seated tiers the customer picks specific seats and the booking reserves exactly those tickets; for general-admission tiers the customer picks a quantity and the booking reserves any free tickets of the tier. "Best available" for seated tiers is added only after the system is proven under load, e.g. 1,000 concurrent users booking the same seat with exactly one winner. |
| 2026-11-01 | **Per-customer limits are enforced** with a counter row per (customer, tier), updated in the same transaction as the booking, with a database `CHECK (count <= limit)`. Two parallel requests from the same customer can't both slip under the limit, and the rows are per customer, so they don't become hot rows. |
| 2026-11-03 | CI runs lint, tests with `-race` and a build on GitHub Actions, on pushes to `main` and `milestone/**` and on pull requests into `main`. The `golangci-lint` version is pinned (v2.15.0, the same as locally) and upgraded on purpose, because a new version can report new issues. |
| 2026-11-03 | Go files are always LF (`.gitattributes`). With `core.autocrlf=true`, switching branches on Windows could otherwise turn them into CRLF, which gofmt and so `make lint` report as unformatted. |

## Open

| Settle in | Topic | Recommendation |
|---|---|---|
| M3 | **Hold duration and anti-hoarding** | A short configurable hold time (around 10 minutes), at most one open booking per event per customer, and rate limiting on `POST /bookings`, on top of the per-customer limits. Details in [10 · System flows](10-system-flows.md#a-user-who-keeps-blocking-tickets). |
| M7 | **Customer-initiated refunds** | Not decided: whether customers may request refunds, and until when. Event cancellation always refunds. |
| M7 | **Late payment on an expired booking** | Pause expiry while a payment is processing. On a late success, reclaim the same tickets if they are still free, otherwise refund automatically. A possible later upgrade is Stripe authorize-then-capture (`capture_method=manual`). "Always refund" was rejected as needlessly unfriendly. |
| M9 | **Observability stack** | Prometheus (scraping `/metrics`) + Grafana in Docker Compose, added together with the k6 load tests. Loki and Tempo are not planned: logs are already JSON on stdout (ready for Loki if ever needed), and a single binary gains little from distributed tracing. |
