# CLAUDE.md — Concurrent Ticket Booking System

How Claude works in this repository. **All project knowledge lives in [`docs/open/`](docs/open/README.md):**
read it before doing anything, starting with the README, `01-project-overview.md` and
`02-roadmap.md`.

---

## Working agreement

This is a **serious learning project**. The owner is new to Go, Stripe, Redis and Elasticsearch,
and comes from **Java / Spring Boot**.

- **Go gently.** Keep language and implementations simple. Small steps, not big drops of code.
- **Explain thoroughly when asked.** Explain the *why*, not just the *what*. Comparisons to
  Java/Spring (e.g. `@Autowired`, `MockMvc`, `application.properties`, `synchronized`) help a lot.
- **Don't front-load concepts.** Ideas like the state machine, outbox or idempotency are explained
  when we reach the milestone that needs them.
- **Do only what was asked.** Don't start the next step or milestone until the owner says so.
  Answer questions instead of piling on more work.
- **Never commit or push unless explicitly told to.** When the owner asks for commit details, only
  write the commit message text in chat. Don't stage, commit or push.
- After every step: run the relevant tests (with `-race`) and fix failures before moving on.
  Never generate a large untested chunk of the system.

## Documentation rule

`docs/open/` must **always reflect the final state of the code**. Never let it go stale.

- Update the affected docs in the **same change** as the code: package layout
  (`03-architecture.md`), runtime behavior (`06-application-runtime.md`), environment variables
  (`07-configuration.md` and `.env.example`), commands and tools (`08-development.md`),
  decisions (`05-decisions.md`).
- After every finished step, update `02-roadmap.md` (status markers and "Where we are").
- When the owner asks where the project stands, answer from `02-roadmap.md`.
- Keep this file light: no design details here; they belong in `docs/open/`.

## Shell notes for Claude

- The desktop uses Windows PowerShell 5.1 (no `&&`; use `; if ($?) { … }`); Git Bash is also
  available.
- Run `go test -race` from **PowerShell**. In Claude's Git Bash the cgo step of the race build
  fails (`runtime/cgo: cgo.exe: exit status 2`) while the same command works in PowerShell. Likely
  cause (not confirmed): McAfee blocks the program; the owner sees a McAfee popup at the same time.
- Test command for the application code: `go test -count=1 -race ./cmd/... ./internal/...`
  (never include `./examples/...`). Setup and other commands: `docs/open/08-development.md`.
