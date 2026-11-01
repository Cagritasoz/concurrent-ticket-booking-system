# concurrent-ticket-booking-system

A concurrent ticket booking system in Go: a modular monolith built around never selling a ticket
twice, idempotent Stripe payments and fault tolerance.

Documentation: [docs/open](docs/open/README.md) — start with the
[project overview](docs/open/01-project-overview.md) and the [roadmap](docs/open/02-roadmap.md).

Quick start:

```bash
go run ./cmd/ticketing                 # serves on :8080
curl http://localhost:8080/healthz
```
