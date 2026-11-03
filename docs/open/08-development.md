# 08 · Development

## Prerequisites

| Tool | Why | Notes |
|---|---|---|
| Go 1.27 | build and test | |
| A C compiler (`gcc`) | the race detector (`-race`) needs cgo, and cgo needs a C compiler | Windows: MSYS2, then `pacman -S mingw-w64-ucrt-x86_64-gcc` and add `C:\msys64\ucrt64\bin` to PATH. macOS: `xcode-select --install`. Linux: `build-essential`. |
| GNU `make` | the Makefile (from M0 step 7) | Windows: `pacman -S make` in MSYS2 and add `C:\msys64\usr\bin` to the **end** of PATH (it contains Unix tools such as `find.exe` that would otherwise hide the Windows ones). |
| `golangci-lint` v2.15.0 | linting | Install the official binary into `$(go env GOPATH)/bin`, e.g. `curl -sSfL https://golangci-lint.run/install.sh \| sh -s -- -b "$(go env GOPATH)/bin" v2.15.0` (from a POSIX shell such as Git Bash). |
| Docker Desktop | local infrastructure | Check with `docker ps`. |

If `go test -race` reports `-race requires cgo; enable cgo by setting CGO_ENABLED=1`, Go found no
C compiler on PATH and turned cgo off. Install one, open a new terminal, and if
`go env CGO_ENABLED` still shows `0`, run `go env -w CGO_ENABLED=1`.

## Configuration

Copy `.env.example` to `.env` and adjust it; `.env` is ignored by git. The app reads only real
environment variables, so pass the file to the process (e.g. with an IDE .env plugin) or set
variables inline. Every variable has a default, so the app also runs with none set.
See [07-configuration](07-configuration.md).

## Commands

```bash
go run ./cmd/ticketing                              # start the app on :8080
go build ./... && go vet ./...                      # build and vet everything
go test -count=1 -race ./cmd/... ./internal/...     # the application's tests, race detector on
golangci-lint run ./cmd/... ./internal/...          # lint
docker compose up -d                                # Redis (6379) + redis-commander UI (8081), used by examples/
go test -v -count=1 -race ./examples/booking/       # the example tests, by hand only (needs Redis running)
```

`-count=1` disables Go's test cache, so tests always really run. Make targets for these commands
arrive in M0 step 7.

Try the endpoints while the app runs:

```bash
curl -i http://localhost:8080/healthz
curl -i http://localhost:8080/readyz
curl -i -H "X-Request-ID: my-trace-42" http://localhost:8080/healthz
```

On Windows PowerShell, set a variable for one run with `$env:LOG_FORMAT="text"; go run ./cmd/ticketing`.

## Testing conventions

- Tests live next to the code (`*_test.go`) and use the standard `testing` package.
- Table-driven tests with `t.Run` subtests are preferred for input/output cases.
- Tests never read the real environment or write to the terminal: config tests pass a fake
  lookup function, logger tests write into a `bytes.Buffer`, HTTP tests use
  `net/http/httptest` in memory. Tests that need a real server (startup and shutdown in
  `cmd/ticketing`) listen on `127.0.0.1:0`, a free port chosen by the operating system.
- Tests wait on channels instead of sleeping: a test handler signals when it has started and
  blocks until the test releases it, so timing never decides the result.
- Every test run uses `-race`.
- Planned with the first integration tests (M1): unit tests run under `go test -short`;
  integration tests call `t.Skip` when `testing.Short()` is true and start one container per
  package in `TestMain`. CI runs both with `-race`.
- `examples/` is not part of the regular test runs.
