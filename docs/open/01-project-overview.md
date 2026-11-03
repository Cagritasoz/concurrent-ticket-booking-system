# 01 · Project overview

## What it is

A concurrent ticket booking system written in Go: customers browse and search events, hold
tickets, pay with Stripe and receive confirmed bookings, while many customers compete for the
same seats at the same time.

It is also a learning project. It was chosen because it forces the hard problems to be solved
properly: concurrency, payments that can fail halfway, and systems that must stay correct when a
dependency goes down.

## Priorities

In order:

1. **Concurrency safety**: a ticket is never sold twice.
2. **Payment idempotency**: retries and duplicates never charge twice or confirm twice.
3. **Fault tolerance**: timeouts, crashes and outages leave the system in a correct state.
4. **Testability**: every important rule is covered by tests, including failure paths.
5. **Clean architecture**: clear module boundaries, few and meaningful abstractions.

## Architecture style

A **modular monolith**: one deployable Go application with clearly separated modules. No
microservices, and no infrastructure or abstraction without a concrete need.

| Module | Responsibility |
|---|---|
| **event** | Events, venues, ticket inventory, pricing (admin side) |
| **booking** | Reservations, temporary holds, booking lifecycle, idempotency, Stripe payments, webhooks, reconciliation, refunds |
| **search** | Elasticsearch indexing, full-text search, filtering, pagination |

The modules are planned; see the [roadmap](02-roadmap.md) for what exists today.

## Tech stack

| Concern | Choice | Status |
|---|---|---|
| Language | Go 1.27, module `github.com/Cagritasoz/concurrent-ticket-booking-system` | in use |
| HTTP | Gin v1.12.0, only inside the HTTP layer | in use |
| Logging | `log/slog` (standard library), JSON to stdout | in use |
| Database | PostgreSQL via pgx + sqlc (type-safe Go generated from SQL) | planned (M1) |
| Migrations | golang-migrate | planned (M1) |
| Auth | Keycloak via OIDC (coreos/go-oidc); never a custom identity provider | planned (M2) |
| Holds / cache | Redis (`SET NX` with expiry), never the only correctness mechanism | planned (M5) |
| Payments | Stripe PaymentIntents + webhooks (stripe-go) | planned (M6) |
| Search | Elasticsearch (official go-elasticsearch client) | planned (M8) |
| Metrics | Prometheus client | planned (M9) |
| Testing | `testing` package, race detector; later Testcontainers, fuzzing, k6 load tests | partly in use |
| Local infra | Docker Compose | in use (Redis for the examples) |
| CI | GitHub Actions | in use |
| Gateway | Traefik, only if a real need appears | not planned |

## Requirements

### Concurrency and inventory
- A ticket must **never** be sold twice.
- **PostgreSQL is the source of truth** for ticket ownership and booking state, using
  transactions, conditional updates, unique constraints and appropriate locking.
- Redis is used only for temporary holds and fast coordination. The system stays correct when
  Redis is down or loses data. Holds expire, and only the holder can release a hold.

### Booking and payment idempotency
- Clients send an idempotency key with booking requests. Keys are stored in PostgreSQL with
  unique constraints. Reusing a key with a different payload is rejected.
- Each logical payment operation uses one stable Stripe idempotency key.
- Stripe webhook signatures are verified; webhooks are deduplicated by the stored Stripe event ID.
- Timeouts, duplicates, crashes, retries and uncertain payment outcomes are handled explicitly.
- A frontend redirect is **never** proof of payment.
- Booking and payment state changes are explicit, including expiration, failure, refund and
  reconciliation.

### Search
- Elasticsearch handles discovery, filtering and relevance. PostgreSQL stays authoritative for
  availability, pricing and booking decisions.
- Index rebuilds are supported, and PostgreSQL → Elasticsearch sync is reliable.

### Auth
- Keycloak OIDC. JWT signature, issuer, expiry, audience and permissions are validated.
- Resource-level authorization in the application, with separate customer and event-admin
  permissions.

### Engineering
- Docker Compose for local infrastructure; GitHub Actions for format, lint, unit tests,
  integration tests, race detection and coverage.
- Health and readiness endpoints, structured logs, metrics, graceful shutdown, HTTP timeouts.
- Secrets are never in source control. Configuration comes from validated environment variables.
- Architecture decisions, API, environment variables, migrations and dev commands are documented.
  Setup is reproducible from a clean checkout.

### Testing
Unit tests (domain rules, state transitions), HTTP handler tests, integration tests against real
Postgres, Redis and Elasticsearch (Testcontainers), Stripe test-mode and webhook tests, concurrent
booking tests (only one customer wins a ticket), idempotency and duplicate-webhook tests,
failure-injection tests (Redis outage, database errors, payment timeouts, retries, expired holds),
the race detector, fuzz tests and k6 load tests. Meaningful failure-path coverage matters more
than a 100% line-coverage target.
