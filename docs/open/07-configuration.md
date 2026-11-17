# 07 · Configuration

The application is configured only through environment variables, read by `internal/config`.
`DATABASE_URL` is required. Every other variable is optional: if it is not set, or is set to an
empty value, it uses its default. Invalid values stop the application at startup, and all problems
are reported together.

Locally, copy `.env.example` to `.env` and load it into the run configuration with your IDE's
.env plugin. Docker Compose also reads `.env`, on its own, to fill in its `${...}` values. `.env` is
ignored by git; never commit real secrets.

Durations use Go syntax (`time.ParseDuration`): `500ms`, `15s`, `2m`, `1h30m`.
A plain number without a unit (e.g. `15`) is invalid.

## Database

| Variable | Default | Valid values | Meaning |
|---|---|---|---|
| `DATABASE_URL` | none, required | `postgres://` or `postgresql://` URL | Postgres connection, e.g. `postgres://user:password@localhost:5432/database?sslmode=disable`. Special characters in the password must be percent-encoded (`@` becomes `%40`). `sslmode=disable` is for local use only. Pool settings such as `pool_max_conns=10` can be added. It contains a password, so it is never logged or printed in errors. |

Docker Compose reads these to create the local Postgres container. The app doesn't read them, so
they must match `DATABASE_URL`.

| Variable | Default | Meaning |
|---|---|---|
| `POSTGRES_USER` | none, required | Database user created on first start. |
| `POSTGRES_PASSWORD` | none, required | Its password. |
| `POSTGRES_DB` | none, required | Database created on first start. |
| `POSTGRES_PORT` | `5432` | Port on this machine. Change it if another Postgres already uses 5432. |

The user, password and database are only applied when the data volume is first created. To change
them later, run `docker compose down -v`, which **deletes the local database**, then `make up`.

`cmd/migrate` (`make migrate-up` and the other migration targets) reads the same `DATABASE_URL`,
plus `LOG_LEVEL` and `LOG_FORMAT`.

`TEST_DATABASE_URL` is read only by tests that need a real Postgres, and they are skipped when it
isn't set. CI sets it. Most of these tests create their own database on that server, named
`test_` plus random letters, and drop it when they finish, so the user needs permission to create
databases (the Compose user has it). The database named in the URL is never changed, so it can be
the same as `DATABASE_URL`. M1 step 3 replaces this with a Postgres the tests start themselves.

## HTTP server

| Variable | Default | Valid values | Meaning |
|---|---|---|---|
| `HTTP_ADDR` | `:8080` | `host:port` or `:port` | Address the server listens on. `:8080` means all interfaces, port 8080. |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | duration > 0 | Time allowed to read the request headers. Protects against clients that send headers very slowly (Slowloris). |
| `HTTP_READ_TIMEOUT` | `15s` | duration > 0 | Time allowed to read the whole request, including the body. |
| `HTTP_WRITE_TIMEOUT` | `15s` | duration > 0 | Time allowed to write the response. |
| `HTTP_IDLE_TIMEOUT` | `60s` | duration > 0 | How long an idle keep-alive connection stays open. |

## Shutdown

| Variable | Default | Valid values | Meaning |
|---|---|---|---|
| `SHUTDOWN_TIMEOUT` | `20s` | duration > 0 | Maximum time in-flight requests get to finish after Ctrl+C / SIGTERM. If they are still running after it, the remaining connections are force-closed and the app exits with code 1. |

## Logging

| Variable | Default | Valid values | Meaning |
|---|---|---|---|
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` | Lowest level that is written. Lowercase only. |
| `LOG_FORMAT` | `json` | `json`, `text` | `json` for production and log tools, `text` for reading in a terminal. |
