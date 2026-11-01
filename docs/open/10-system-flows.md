# 10 · System flows

How the planned system behaves from the user's point of view and inside the application: what
each module does, the booking lifecycle, Redis's role, and the main paths (buying, cancelling,
abandoning, paying, refunding). This is design, not code: none of it is built yet. The milestone
that builds each part is in the [roadmap](02-roadmap.md); the rules it must respect are the
[invariants](04-invariants.md).

## The parts and what each does

| Part | Responsibility | Source of truth for |
|---|---|---|
| **event** module | Admin side: venues (with an optional seat layout), events, ticket tiers, generating one ticket row per sellable unit, publishing and cancelling events | Events, tiers, prices, tickets |
| **booking** module | Customer side: holding tickets, the booking lifecycle, idempotent requests, per-customer limits, Stripe payments, webhooks, expiry, reconciliation, refunds | Bookings, who owns which ticket, payments |
| **search** module | Fast discovery for users: full-text search, filters, sorting, pagination over Elasticsearch | Nothing: it is a read-only copy of Postgres |
| **auth** | Verifies Keycloak login tokens and roles (customer, event admin) | Nothing: Keycloak owns identities |
| **httpapi** | The REST API: routes, middleware, translating HTTP to module calls and errors to status codes | Nothing |
| **Postgres** | The database | Everything that matters: ownership, state, money |
| **Redis** | A fast in-memory filter in front of Postgres for contested tickets | Nothing: losing it never breaks correctness |
| **Stripe** | Takes the card payment | Whether money moved |

## Booking lifecycle (state machine)

```
                      ┌────────────► CONFIRMED ──► REFUND_PENDING ──► REFUNDED
                      │  payment verified           refund requested    refund confirmed
 create booking       │
 ───────────► PENDING_PAYMENT ──────► EXPIRED        hold time ran out, nothing paid
  (tickets held)      │
                      └────────────► CANCELLED      customer cancelled before paying
```

- **PENDING_PAYMENT:** the booking exists and its tickets are **held**: no one else can book them.
  It has an `expires_at` (the hold time, e.g. 10 minutes; the exact value is a config setting
  decided in M3).
- **CONFIRMED:** Stripe confirmed the payment for the exact amount and currency (invariant I3).
  The tickets are the customer's.
- **EXPIRED / CANCELLED / REFUNDED** are final. Expired and cancelled bookings release their
  tickets in the same transaction (invariant I5).
- Every change is a conditional update, "set CONFIRMED where status is still PENDING_PAYMENT", so
  two things racing (a webhook and the expiry sweeper, say) can't both win (invariant I2).

Each booking can have several **payment attempts** (a declined card can be retried):
`CREATING → REQUIRES_ACTION / PROCESSING → SUCCEEDED | FAILED | CANCELED`. A failed attempt
doesn't end the booking; the customer can try again until it expires.

## Why Postgres decides and Redis only filters

A ticket is sold exactly once only if **one** system decides, durably and atomically. That is
Postgres: the booking row and its ticket items are written in one transaction, and a unique index
("a ticket is in at most one active booking", invariant I1) makes a second booking of the same
ticket fail, no matter how many requests race.

Redis can't be that system:
- It keeps data in memory. A restart or failover can lose recent keys.
- It is a separate system from Postgres, so a Redis write and a Postgres write are never one
  atomic step; after a crash they can disagree.
- Expiry, network splits and retries can all make Redis's view stale.

So Redis is a **pre-filter**. When 1,000 users press "reserve" on seat B-7-12 at the same moment,
Redis lets exactly one request through (`SET NX` on the ticket's key) and rejects the other 999 in
microseconds, before they reach the database. Postgres then sees one request instead of 1,000
queuing on the same row lock. If Redis is down or has lost the key, requests simply go to
Postgres, which still lets only one win. Redis makes the system **fast under contention**;
Postgres makes it **correct**.

Redis is not a session store here. A hold key is **per ticket**: `hold:{ticket_id} → booking_id`,
with a TTL matching the booking's `expires_at`. The booking ID works as the hold's token: only the
booking that owns the key can delete it (Lua compare-and-delete, invariant I11). The real hold is
the PENDING_PAYMENT booking in Postgres; the Redis key is a fast mirror of it.

## Happy path: buying a seated ticket

1. **Search.** The user searches "Dune Berlin". The **search** module queries Elasticsearch, which
   is built for this: full-text matching, typo tolerance, filters (city, date, category, price),
   sorting and paging. Results show events with a "from €12" price and, at most, a coarse
   "sold out" flag. Elasticsearch never decides availability (invariant I12).
2. **Open the event.** The event page loads from **Postgres**: tiers, prices, and, for a seated
   event, the seat map with each seat's state (free, or taken by an active booking).
3. **Pick seats.** The user clicks B-7-12 and B-7-13. Clicking is only a selection in the
   frontend; **nothing is reserved yet**, and no Redis key exists.
4. **Reserve.** The user presses "Continue", which sends `POST /bookings` with the two tickets and
   an `Idempotency-Key` header (the user must be logged in through Keycloak). The **booking**
   module:
   1. checks the idempotency key: a retry or a double-click returns the same booking instead of
      creating a second one (invariant I6);
   2. runs the booking rules: the tier is on sale, the per-customer limit isn't exceeded, and so on;
   3. picks a new booking ID and tries `SET NX` in Redis for each ticket. If any key is already
      taken, it stops right away with "seat no longer available" and releases the keys it did get;
   4. in **one Postgres transaction**: inserts the booking (PENDING_PAYMENT, `expires_at`), its
      line ("2 × Standard, €12 each", price copied from the tier, invariant I4) and its items
      (the two ticket IDs), and bumps the customer's limit counter. The unique index rejects any
      ticket that is already in an active booking, which rolls the whole booking back
      (all-or-nothing); the Redis keys are then released;
   5. returns the booking with its ID and `expires_at`. The frontend shows a countdown.
5. **Pay.** See [Payment with Stripe](#payment-with-stripe) below.
6. **Confirmed.** After Stripe's verified webhook, the booking becomes CONFIRMED. The frontend,
   which polls `GET /bookings/{id}`, shows the tickets. The Redis keys are no longer needed and
   are deleted (or simply expire).

**General admission** works the same, except step 3 is a quantity ("3 × GA Floor") and in step 4
the booking module picks any 3 free tickets of that tier, skipping rows another transaction is
already locking (`FOR UPDATE SKIP LOCKED`), so parallel buyers don't wait for each other.

## Payment with Stripe

Card details never touch our server: the frontend sends them straight to Stripe. Our server only
creates the payment and listens for Stripe's verdict.

1. **Start the payment.** The frontend calls `POST /bookings/{id}/payments`. The booking module
   checks the booking is still PENDING_PAYMENT and not expired, then **first saves a payment
   attempt row** (CREATING) with a stable Stripe idempotency key (invariant I7), and only then
   asks Stripe to create a PaymentIntent for the booking's amount and currency, tagged with the
   booking ID. It stores the PaymentIntent ID and returns its `client_secret`.
2. **Customer pays.** Stripe's own payment form in the frontend uses the `client_secret`. The
   customer enters their card; the bank may ask for an extra check (3-D Secure), which is the
   REQUIRES_ACTION state.
3. **Redirect back.** Stripe sends the browser back to our success page. **This proves nothing**:
   anyone can open that URL. The page just shows "processing" and polls the booking.
4. **Webhook.** Stripe calls `POST /webhooks/stripe` with `payment_intent.succeeded`. The booking
   module verifies Stripe's signature over the raw request body, then, in **one transaction**:
   records the Stripe event ID (a repeated delivery is ignored, invariant I8), checks the amount
   and currency match the booking, marks the attempt SUCCEEDED and the booking CONFIRMED.
5. **If the webhook never arrives**, the **reconciler** (a background worker) periodically asks
   Stripe about attempts stuck in PROCESSING and applies the same result.

**Failed payment:** Stripe sends `payment_intent.payment_failed`; the attempt becomes FAILED, the
booking stays PENDING_PAYMENT with its tickets held, and the customer can try another card until
`expires_at`.

**Payment still processing when the hold runs out:** the booking is not expired while a payment is
in flight. If money arrives after the booking did expire, the system reclaims the same tickets if
they are still free, otherwise refunds automatically (decision settled in M7).

## Cancelling, and closing the browser

**The customer presses "Cancel".** The frontend calls `POST /bookings/{id}/cancel`. If the
booking is still PENDING_PAYMENT and no payment is in flight, it becomes CANCELLED in one
transaction: its items are released, the limit counter goes down, and its Redis keys are deleted
(compare-and-delete with the booking ID, so it can never delete someone else's hold). An open
PaymentIntent is cancelled at Stripe. The seats are free again immediately.

**The customer closes the browser.** The server is never told, and it doesn't need to be: the hold
has a deadline. The **expiry sweeper**, a background worker, regularly finds PENDING_PAYMENT
bookings past `expires_at` (skipping those with a payment in flight) and moves each to EXPIRED in
one transaction that releases its tickets and lowers the limit counter. The Redis keys expire on
their own through their TTL. If the customer comes back before `expires_at`, the booking is still
there and they can continue paying.

Postgres's `expires_at` is the authority. The Redis TTL is only a convenience: if Redis expires
early the database still holds the booking, and if Redis is wiped nothing breaks.

## A user who keeps blocking tickets

Holding seats without paying, letting them expire, and holding again would deny seats to real
buyers. Holds are therefore limited on several sides:

| Defense | Effect |
|---|---|
| Login required | Holds belong to a Keycloak account, not an anonymous browser. |
| Short hold time | Each hold lasts minutes, not hours. |
| Per-customer limit per tier | Counts **held and confirmed** tickets, so one account can never hold more than the limit (e.g. 4) at once. |
| One open booking per event per customer (proposed) | A new hold first requires finishing or cancelling the previous one. |
| Rate limiting on `POST /bookings` (proposed) | Stops rapid hold/expire/hold loops. |
| Cool-down after repeated expiries (possible later) | An account that lets many holds expire waits before holding again. |

Bots creating many accounts are a wider problem (CAPTCHAs, waiting rooms) and out of scope for
now.

## Refunds and event cancellation

- **Event cancelled by its admin:** the event is locked so no new bookings start, pending bookings
  are cancelled, and every CONFIRMED booking moves to REFUND_PENDING. A refund is requested from
  Stripe with a stable idempotency key; Stripe's refund webhook moves the booking to REFUNDED.
  The reconciler catches refunds whose webhook never arrives.
- **Customer-initiated refunds:** the policy (allowed or not, until when) is not decided yet.

## Search sync

Every change to an event, tier or availability flag is written to an **outbox** table in the same
transaction as the change itself. A background indexer reads the outbox and updates
Elasticsearch, so a crash can delay the search index but never lose an update. The index can be
rebuilt from Postgres at any time and swapped in without downtime.
