# Environment variables

The application is configured only through environment variables, read by `internal/config`.
Every variable is optional. A variable that is not set, or is set to an empty value, uses its
default. Invalid values stop the application at startup, and all problems are reported together.

Locally, copy `.env.example` to `.env` and load it into the run configuration with your IDE's
.env plugin. `.env` is ignored by git; never commit real secrets.

Durations use Go syntax (`time.ParseDuration`): `500ms`, `15s`, `2m`, `1h30m`.
A plain number without a unit (e.g. `15`) is invalid.

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
| `SHUTDOWN_TIMEOUT` | `20s` | duration > 0 | Maximum time in-flight requests get to finish after Ctrl+C / SIGTERM. |

## Logging

| Variable | Default | Valid values | Meaning |
|---|---|---|---|
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` | Lowest level that is written. Lowercase only. |
| `LOG_FORMAT` | `json` | `json`, `text` | `json` for production and log tools, `text` for reading in a terminal. |
