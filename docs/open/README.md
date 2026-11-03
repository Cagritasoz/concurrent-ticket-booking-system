# Project documentation

Design and reference documentation for the Concurrent Ticket Booking System. These documents
always describe the **current state of the code**. Planned work is clearly marked as planned.

New to the project? Read them in order:

| # | Document | What it covers |
|---|---|---|
| 01 | [Project overview](01-project-overview.md) | What the system is, priorities, modules, tech stack, requirements |
| 02 | [Roadmap](02-roadmap.md) | Milestones and steps: what is done, what is in progress, what is left |
| 03 | [Architecture](03-architecture.md) | Package layout (current and target), dependency rules, Go conventions |
| 04 | [Correctness invariants](04-invariants.md) | The rules that must never break (no double sale, idempotency, ...) and how they are enforced |
| 05 | [Decisions](05-decisions.md) | Decision log, including open decisions and the recommended option |
| 06 | [Application runtime](06-application-runtime.md) | How the running app works today: startup, logging, request IDs, HTTP server, health checks |
| 07 | [Configuration](07-configuration.md) | Every environment variable with its default and valid values |
| 08 | [Development](08-development.md) | Local setup, tools, commands and testing conventions |
| 09 | [Learning examples](09-examples.md) | The early learning code in `examples/` and the lessons taken from it |
| 10 | [System flows](10-system-flows.md) | How the planned system behaves: modules, booking lifecycle, Redis's role, buying, paying, cancelling, expiry, refunds |

Reading tip: after 01 and 02, [10 · System flows](10-system-flows.md) is the best picture of the
system as a whole.
