# 08 · Development

## Prerequisites

| Tool | Why | Notes |
|---|---|---|
| Go 1.27 | build and test | |
| A C compiler (`gcc`) | the race detector (`-race`) needs cgo, and cgo needs a C compiler | Windows: MSYS2, then `pacman -S mingw-w64-ucrt-x86_64-gcc` and add `C:\msys64\ucrt64\bin` to PATH. macOS: `xcode-select --install`. Linux: `build-essential`. |
| GNU `make` | the Makefile | Windows: `pacman -S make` in MSYS2 and add `C:\msys64\usr\bin` to the **end** of PATH (it contains Unix tools such as `find.exe` that would otherwise hide the Windows ones). |
| `golangci-lint` v2.15.0 | linting | Install the official binary into `$(go env GOPATH)/bin`, e.g. `curl -sSfL https://golangci-lint.run/install.sh \| sh -s -- -b "$(go env GOPATH)/bin" v2.15.0` (from a POSIX shell such as Git Bash). |
| `sqlc` v1.31.1 | generates Go from the SQL queries (`make generate`) | `go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1` puts it into `$(go env GOPATH)/bin`. On Windows the build takes a few minutes (it compiles a WebAssembly build of the Postgres parser) and needs nothing else. Only needed when changing queries or migrations: the generated code is committed. |
| Docker Desktop | local infrastructure, and the Postgres the tests start with Testcontainers | Check with `docker ps`. If it isn't running, the Postgres tests fail with "is Docker running?" (Testcontainers' own text in that message, "rootless Docker is not supported on Windows", is misleading). |

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
make generate       # generate Go code from the SQL queries with sqlc
make test           # all of the application's tests, race detector on (needs Docker running)
make test-short     # only the tests that need no Postgres (no Docker needed)
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
need a real Postgres start one with Testcontainers, so Docker Desktop must be running; nothing needs
to be set, and `make up` is not needed for tests. The first run downloads the image, later runs
take a few seconds per package. If Docker isn't running these tests fail with a message saying so;
use `make test-short` to skip them, or point them at an existing server with
`$env:TEST_DATABASE_URL="<a Postgres URL>"; make test`. Linter
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
4. Run `make generate`: sqlc reads the migrations as its schema, so a changed table changes the
   generated structs.
5. `make test` checks the naming rules and runs every migration up, down and up again against a
   fresh database.

**Rules:**

- Never edit a migration after it was applied anywhere (another machine, CI, production). Fix it
  with a new migration. Editing it would leave databases that ran the old version different from
  ones that run the new version, with nothing to show it.
- Each file runs as one transaction: if a statement fails, the whole file is undone. So don't put
  `BEGIN` / `COMMIT` in the files, and avoid statements Postgres can't run inside a transaction,
  such as `CREATE INDEX CONCURRENTLY`.

**When a migration fails ("dirty"):** the file's changes are rolled back, but `schema_migrations`
keeps a version with `dirty = true`, and `up` and `down` refuse to run until it is cleared.
`make migrate-version` shows it. Ctrl+C during a migration leaves the same state. To recover, check
the database, fix the file, and run `make migrate-force version=N` with the version that is really
applied, which is not always the dirty version shown (like Flyway's `repair`). Every case, and how
to choose N, is in [11 · Migration failures and recovery](11-migration-recovery.md).

## Database queries (sqlc)

Queries are plain SQL in each module's `queries/` folder (today `internal/event/queries`). sqlc
reads them together with the migrations and generates Go code into the module's `...db` package
(`internal/event/eventdb`): a struct per table (`eventdb.Venue`), a parameter struct per query and
one method per query on `eventdb.Queries`. Wrong SQL, an unknown column or a type mismatch fails
at `make generate`, before anything runs. `sqlc.yaml` explains the settings and the comment syntax.

**Adding or changing a query:**

1. Write it in the queries file, starting with `-- name: MethodName :one` (or `:many`, `:exec`,
   `:execrows`), then a comment explaining it, which becomes the method's Go comment.
2. `make generate`, and commit the SQL and the generated files together. Never edit the generated
   files; CI runs `sqlc diff` and fails if they don't match.
3. Test it against real Postgres, next to the generated code (`internal/event/eventdb/*_test.go`).

**Using the queries:** `eventdb.New(pool)` runs each query on its own; inside a transaction use
`postgres.WithTx`, which commits when the function returns nil and rolls back on an error or a
panic:

```go
err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
	q := eventdb.New(tx)
	venue, err := q.GetVenueForUpdate(ctx, id) // locks the row until the transaction ends
	if err != nil {
		return err
	}
	// change venue, then q.UpdateVenue(ctx, ...)
	return nil
})
```

**Errors:** a query that finds no row returns `pgx.ErrNoRows` (check with `errors.Is`). A broken
constraint is recognised by its name with `postgres.IsUniqueViolation`, `IsForeignKeyViolation`
or `IsCheckViolation`, so the module can return its own error for it. Postgres names constraints
`table_column_key`, `_fkey` and `_check` unless the migration names them with `CONSTRAINT`.

## Continuous integration

`.github/workflows/ci.yaml` runs on GitHub Actions for every push to `main` or a `milestone/**`
branch and for pull requests into `main`. It installs the Go version from `go.mod`, then runs lint,
`sqlc diff` (the committed generated code matches the queries; the sqlc version is pinned like
golangci-lint's), `make test` (whose Postgres tests start their own container with Testcontainers, as locally), the
migration targets (up, down, up) against a throwaway Postgres service, and `make build`;
`make check` covers the lint and test parts locally. `examples/` is not part of CI. The golangci-lint and sqlc versions are pinned, so when upgrading one, change it in
the workflow and in the prerequisites above together (and run `make generate` for sqlc).

Try the endpoints while the app runs:

```bash
curl -i http://localhost:8080/healthz
curl -i http://localhost:8080/readyz
curl -i -H "X-Request-ID: my-trace-42" http://localhost:8080/healthz
```

There is no endpoint to create venues yet, so add one by hand (e.g. in `psql`) and read it back
with the id it returns:

```sql
INSERT INTO venues (name, address_line1, city, country_code, timezone)
VALUES ('Volkswagen Arena', 'Huzur Mah. Azerbaycan Cad. 1', 'Istanbul', 'TR', 'Europe/Istanbul')
RETURNING id;
```

```bash
curl -i http://localhost:8080/api/v1/admin/venues/{id}
```

On Windows PowerShell, `curl` is an alias of `Invoke-WebRequest`; use `curl.exe` for the real one.

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
- Tests that need Postgres get their own database from `pgtest`, like `@Testcontainers` with
  `@DataJpaTest` in Spring:
  - `pgtest.NewDatabase(t)` returns the URL of a fresh, empty database, dropped when the test ends,
    so tests never see each other's data. `pgtest.NewMigratedDatabase(t)` does the same with every
    migration applied, for code that uses the tables.
  - The server is `TEST_DATABASE_URL` if set; otherwise the first test that needs it starts a
    Postgres container, which the package's other tests share.
  - Every package that uses `pgtest` needs `func TestMain(m *testing.M) { pgtest.Main(m) }` (in its
    `main_test.go`). It removes the container after the tests; a test fails with a clear message if
    it's missing. If the process is killed, Testcontainers' Ryuk container removes it.
  - Under `go test -short` these tests are skipped.
  - In `internal/platform/postgres` the tests live in the external test package
    (`package postgres_test`, not `package postgres`), because `pgtest` imports `postgres` and Go
    doesn't allow import cycles.
- Every test run uses `-race`. CI runs the full set, including the Postgres tests.
- `examples/` is not part of the regular test runs.
