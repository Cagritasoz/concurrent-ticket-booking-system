# 04 · Correctness invariants

These rules must hold at all times, including under concurrency, retries, crashes and outages.
None of them is implemented yet; they guide the design of milestones M1–M8 and each one gets
tests when it is built (see the [roadmap](02-roadmap.md)).

| # | Invariant | How it is enforced |
|---|---|---|
| I1 | A ticket is in at most one active (held or confirmed) booking. | Partial unique index `booking_items(ticket_id) WHERE active`; tickets are inserted in sorted order to avoid deadlocks; Postgres error 23505 (unique violation) becomes `ErrTicketUnavailable`. |
| I2 | Booking status only changes through allowed transitions; terminal states are final. | A transition table in Go + `UPDATE … WHERE id = $id AND status = $expected`; a CHECK constraint on the allowed values. |
| I3 | A booking becomes CONFIRMED only after Stripe-verified success for the exact amount and currency. | A signed webhook or a server-side PaymentIntent lookup. Never the frontend redirect. |
| I4 | The price is decided by the server and snapshotted. | The price (integer cents + currency) is copied into the booking when it is created; the payment amount comes from the booking, not the client. |
| I5 | Expiry releases inventory atomically. | One transaction sets EXPIRED and `active = false`. Postgres `expires_at` is authoritative; a booking whose payment is processing is not expired. |
| I6 | One (customer, idempotency key) pair leads to at most one booking; the same payload replays the stored response, a different payload is rejected. | Primary key `(customer_id, key)` + `request_hash`; the key, the booking and the stored response are written in one transaction. |
| I7 | One stable Stripe idempotency key per logical payment operation. | The `payment_attempt` row (with its key) is saved before calling Stripe; retries reuse it. |
| I8 | Each Stripe event's effects are applied at most once. | `stripe_events(event_id PRIMARY KEY)` is inserted in the same transaction as the state change. |
| I9 | At most one in-flight payment attempt per booking; money is never kept without delivering tickets. | A partial unique index on active attempts; the late-payment policy (see [decisions](05-decisions.md)). |
| I10 | Redis never grants ownership; losing Redis or its data can't cause a double sale. | Redis is only a contention pre-filter; Postgres decides. |
| I11 | Only the holder can release a hold. | The Redis value is the booking ID; release uses a Lua compare-and-delete. |
| I12 | Elasticsearch never drives booking or pricing, and it converges to Postgres. | Transactional outbox, external versioning, alias-swap rebuild; only coarse availability (e.g. a sold-out flag) is stored in Elasticsearch. |
| I13 | Customers see only their own bookings; admins manage only their own events. | A role check in middleware + an ownership check in the services. |
| I14 | Held or sold inventory can't be removed underneath customers. | Admin edits lock the event `FOR UPDATE`; booking creation takes `FOR SHARE`; cancelling an event triggers refunds. |

## Redis failure behavior

| Failure | Behavior |
|---|---|
| Redis is down | Short timeout + circuit breaker; fall back to Postgres only, with a metric and a warning log. |
| Redis loses keys | Postgres still rejects duplicates (I1, I10). |
| A stale key remains after a crash | The seat looks taken for at most the hold TTL. |

## Planned state machines

These are explained in detail when they are built (M3 and M6).

- **Booking:** `PENDING_PAYMENT → CONFIRMED | EXPIRED | CANCELLED`, then
  `CONFIRMED → REFUND_PENDING → REFUNDED`.
- **Payment attempt:** `CREATING → REQUIRES_ACTION / PROCESSING → SUCCEEDED | FAILED | CANCELED`.

A failed payment attempt doesn't end the booking: the customer can retry until the booking
expires.
