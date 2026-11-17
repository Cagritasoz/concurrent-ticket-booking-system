# 08 · Development

## Prerequisites

| Tool | Why | Notes |
|---|---|---|
| Go 1.27 | build and test | |
| A C compiler (`gcc`) | the race detector (`-race`) needs cgo, and cgo needs a C compiler | Windows: MSYS2, then `pacman -S mingw-w64-ucrt-x86_64-gcc` and add `C:\msys64\ucrt64\bin` to PATH. macOS: `xcode-select --install`. Linux: `build-essential`. |
| GNU `make` | the Makefile | Windows: `pacman -S make` in MSYS2 and add `C:\msys64\usr\bin` to the **end** of PATH (it contains Unix tools such as `find.exe` that would otherwise hide the Windows ones). |
| `golangci-lint` v2.15.0 | linting | Install the official binary into `$(go env GOPATH)/bin`, e.g. `curl -sSfL https://golangci-lint.run/install.sh \| sh -s -- -b "$(go env GOPATH)/bin" v2.15.0` (from a POSIX shell such as Git Bash). |
| Docker Desktop | local infrastructure | Check with `docker ps`. |

If `go test -race` reports `-race requires cgo; enable cgo by setting CGO_ENABLED=1`, Go found no
C compiler on PATH and turned cgo off. Install one, open a new terminal, and if
`go env CGO_ENABLED` still shows `0`, run `go env -w CGO_ENABLED=1`.

## Configuration

Copy `.env.example` to `.env` and adjust it; `.env` is ignored by git. Docker Compose reads the file
on its own, so `make up` and `make down` need it. The app reads only real environment variables, so
pass the file to the process (e.g. with an IDE .env plugin) or set variables inline. `make run` and
`go run` don't load it, and the app needs at least `DATABASE_URL`.
See [07-configuration](07-configuration.md).

## Running the app

1. `make up` starts Postgres and waits until it is healthy.
2. Set `DATABASE_URL` in the terminal (`make` doesn't read `.env`) and apply the migrations. It
   stays set in that PowerShell window, so the next step can use it too:

```powershell
$env:DATABASE_URL="postgres://user:password@localhost:5432/database?sslmode=disable"
make migrate-up
```

3. Start the app from the IDE with the .env plugin, or with `make run` in the same terminal.

## Commands

```bash
make                # list the targets below
make run            # start the app on :8080
make build          # compile the app into bin/ (ignored by git)
make test           # the application's tests, race detector on
make lint           # golangci-lint, including go vet and format checks
make fmt            # format the code
make check          # lint and test, what to run before pushing
make up             # Postgres (5432), waits until it is healthy
make down           # stop all Compose containers; the Postgres data stays in a Docker volume
make migrate-up     # apply all new migrations (needs DATABASE_URL)
make migrate-down   # roll back the last migration
make migrate-version            # show the version and whether it is dirty
make migrate-force version=N    # mark version N as applied, after fixing a failed migration

# examples/ only, by hand: Redis (6379) + redis-commander UI (8081), then the example tests
docker compose up -d redis redis-commander
go test -v -count=1 -race ./examples/booking/
```

`make test` uses `-count=1`, which disables Go's test cache, so tests always really run. Tests that
need a real Postgres are skipped unless `TEST_DATABASE_URL` is set. To include them, start Postgres
and run `$env:TEST_DATABASE_URL="<your DATABASE_URL>"; make test`. Linter
settings are in `.golangci.yml`. Lint reports one issue per line, so a format problem can stay
hidden until another issue on the same line is fixed.

## Database migrations

The schema is built by numbered SQL files in `migrations/`, applied in order by golang-migrate (the
Go counterpart of Flyway). `schema_migrations` records the current version, like Flyway's
`flyway_schema_history`. The files are embedded into `cmd/migrate`, so it works from any folder.

**Adding a migration:**

1. Create two files with the next number and a lowercase name:
   `000002_create_venue_layouts.up.sql` and `000002_create_venue_layouts.down.sql`.
2. The up file makes the change; the down file undoes exactly that change.
3. Check both directions: `make migrate-up`, `make migrate-down`, `make migrate-up`.
4. `make test` checks the naming rules and runs every migration up, down and up again against a
   fresh database (when `TEST_DATABASE_URL` is set; CI always does).

**Rules:**

- Never edit a migration after it was applied anywhere (another machine, CI, production). Fix it
  with a new migration. Editing it would leave databases that ran the old version different from
  ones that run the new version, with nothing to show it.
- Each file runs as one transaction: if a statement fails, the whole file is undone. So don't put
  `BEGIN` / `COMMIT` in the files, and avoid statements Postgres can't run inside a transaction,
  such as `CREATE INDEX CONCURRENTLY`.

**When a migration fails ("dirty"):** the file's changes are rolled back, but `schema_migrations`
keeps the failed version with `dirty = true`, and every further command refuses to run.
`make migrate-version` shows it. To recover:

1. Read the error and fix the SQL in the failed file (it was never applied, so editing it is fine).
2. Check the database by hand (e.g. `psql`) for anything the file left behind. Because each file is
   one transaction, normally nothing is.
3. Mark the last version that is really applied: if migration 2 failed, `make migrate-force version=1`.
4. Run `make migrate-up` again.

This is what Flyway's `repair` does. Ctrl+C during a migration leaves the same state.

## Continuous integration

`.github/workflows/ci.yaml` runs on GitHub Actions for every push to `main` or a `milestone/**`
branch and for pull requests into `main`. It starts a throwaway Postgres, installs the Go version
from `go.mod`, then runs lint, `make test`, the migration targets (up, down, up) and `make build`;
`make check` covers the lint and test parts locally. `examples/` is not part of CI. Its golangci-lint version is pinned, so when upgrading, change it in the workflow
and in the prerequisites above together.

Try the endpoints while the app runs:

```bash
curl -i http://localhost:8080/healthz
curl -i http://localhost:8080/readyz
curl -i -H "X-Request-ID: my-trace-42" http://localhost:8080/healthz
```

On Windows PowerShell, `$env:LOG_FORMAT="text"` sets a variable for that terminal session, so it also
applies to later commands there.

## Testing conventions

- Tests live next to the code (`*_test.go`) and use the standard `testing` package.
- Table-driven tests with `t.Run` subtests are preferred for input/output cases.
- Tests never read the real environment or write to the terminal: config tests pass a fake
  lookup function, logger tests write into a `bytes.Buffer`, HTTP tests use
  `net/http/httptest` in memory. Tests that need a real server (startup and shutdown in
  `cmd/ticketing`) listen on `127.0.0.1:0`, a free port chosen by the operating system.
- Tests wait on channels instead of sleeping: a test handler signals when it has started and
  blocks until the test releases it, so timing never decides the result. The exception is waiting
  for the whole app to start, where a test polls `/readyz`, with a time limit.
- Tests that need Postgres get their own database from `pgtest.NewDatabase(t)`: a fresh, empty
  database that is dropped when the test ends, so tests never see each other's data. Tests that use
  `pgtest` live in the external test package (`package postgres_test`, not `package postgres`),
  because `pgtest` will import `postgres` in M1 step 3 and Go doesn't allow import cycles.
- Every test run uses `-race`.
- Planned with the first integration tests (M1): unit tests run under `go test -short`;
  integration tests call `t.Skip` when `testing.Short()` is true and start one container per
  package in `TestMain`. CI runs both with `-race`.
- `examples/` is not part of the regular test runs.
