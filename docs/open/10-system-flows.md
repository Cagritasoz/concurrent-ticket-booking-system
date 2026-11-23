# 10 · System flows

How the planned system behaves from the user's point of view and inside the application: what
each module does, how the event side's tables fit together, the booking lifecycle, Redis's role,
and the main paths (buying, cancelling, abandoning, paying, refunding). The event side's tables
exist (migrations 1 to 15, M1 step 6a); the flows that use them are design, not code yet. The
milestone that builds each part is in the [roadmap](02-roadmap.md); the rules it must respect are
the [invariants](04-invariants.md); why each table looks the way it does is in the
[decisions](05-decisions.md).

## The parts and what each does

| Part | Responsibility | Source of truth for |
|---|---|---|
| **event** module | Admin side: venues with their halls, layouts, sections and seats, events, ticket tiers and ticket types with their prices, generating one ticket row per sellable unit, the event lifecycle (submit, confirm, announce, reschedule, cancel) | Events, tiers, ticket types, prices, tickets |
| **booking** module | Customer side: holding tickets, the booking lifecycle, idempotent requests, per-customer limits, Stripe payments, webhooks, expiry, reconciliation, refunds | Bookings, who owns which ticket, payments |
| **search** module | Fast discovery for users: full-text search, filters, sorting, pagination over Elasticsearch | Nothing: it is a read-only copy of Postgres |
| **auth** | Verifies Keycloak login tokens and roles (venue manager, organizer, customer); organizations, their members and venue access grants live in Postgres | Nothing: Keycloak owns identities |
| **httpapi** | The REST API: routes, middleware, translating HTTP to module calls and errors to status codes | Nothing |
| **Postgres** | The database | Everything that matters: ownership, state, money |
| **Redis** | A fast in-memory filter in front of Postgres for contested tickets | Nothing: losing it never breaks correctness |
| **Stripe** | Takes the card payment | Whether money moved |

## How the event side's tables fit together

Twelve tables, in three groups. Table names are in `code`; the number is the migration that
creates the table. The whole domain, with the tables later milestones add (passes, bookings,
payments), is mapped in [12 · Domain model](12-domain-model.md).

```
 THE ROOM (described once, reused by many events)
 venues (1) ─< halls (2) ─< layouts (3) ─< sections (4) ─< seats (5)
                                 │              │            │
 THE EVENT AND ITS PRICES        │              │            │
 events (7) >────────────────────┘              │            │
   ├──> event_series (6), if it is in one       │            │
   ├─< ticket_tiers (8) ─┐                      │            │
   ├─< ticket_types (9) ─┴─< tier_prices (10)   │            │
   │                                            │            │
 THE INVENTORY                                  │            │
   ├─< admin_holds (11)                         │            │
   └─< tickets (12) >── tier, hold ─────────────┴── section ─┴── seat (seated tickets only)

 A ─< B: one A has many B (B has a foreign key to A). A ──> B: A may point at one B.
```

### The room: venue, hall, layout, section, seat

These five tables describe a building and how its rooms can be set up. They sell nothing and know
nothing about dates or prices.

| Table | What a row is | Example |
|---|---|---|
| `venues` | A building, with its address and time zone | Zorlu PSM, Istanbul |
| `halls` | A room in it that can host an event while the other rooms host theirs | Turkcell Stage; a cinema's Screen 3 |
| `layouts` | One way to set up a hall. A hall has one layout per setup, used one at a time | "Concert, standing floor"; "Theater, all seated" |
| `sections` | A part of a layout: `seated` (has seats) or `standing` (has only a `capacity`), with a `position` for the order of the layout's list | "Block 101" (seated); "Floor" (standing, 3,000 people) |
| `seats` | One seat of a seated section: labels printed on the ticket, positions for ordering, accessibility | Row "A", seat "12"; a wheelchair space |

Deleting a venue deletes everything below it (`ON DELETE CASCADE`), unless an event or ticket uses
it: then the delete is refused, and archiving is the way to retire it. A layout that a confirmed
event uses (the venue agreed to that setup) is frozen; to change it, an admin copies it (M1 step 11). The copy's seats are new rows
but keep each seat's `physical_seat_key`, so the same physical seat can be recognised in both.

### The event: one date in one layout

`events`: one occurrence, such as "Duman, 14 March 2027, 20:00 to 23:00", in one layout (and so
in one hall). 30 showings of a film are 30 events, which an **event series** (`event_series`)
can group: a film's run, a theater season, a team's home matches, a festival's days. Each event
of a series keeps its own date, layout, tiers and prices (a matinee is often cheaper); the series
only groups them, and a series with events can't be deleted. An event has:

- `series_id`: the series it belongs to, or NULL for a one-off event.

- `layout_id` and `hall_id`: the setup and the room. `hall_id` repeats the layout's hall so the
  database can stop **two events in one hall from overlapping in time**
  (`events_no_overlap_in_hall`); a foreign key on `(layout_id, hall_id)` keeps it equal to the
  layout's hall.
- `starts_at`, `ends_at` (the show, as customers see it) and optional `doors_open_at`.
- Optional `hall_reserved_from` and `hall_reserved_until`: how long the event needs the hall
  when that is longer than the show (setting up before, clearing after). The no-overlap rule
  uses them, and falls back to the show's times where they are empty.
- Optional `on_sale_at` and `off_sale_at`: when single-ticket sales open (empty: when it is
  announced) and close (empty: when it starts; can be earlier, or later up to the end for late
  entry). Passes have their own sales times, so they can go on sale first.
- `currency`: every price of the event is in it.
- `per_customer_limit` (optional): at most this many single tickets of the event per customer,
  across its tiers; a tier can have a lower limit of its own (see
  [Per-customer limits](12-domain-model.md#per-customer-limits-columns-built-enforced-from-m3)).
- `status`, with a time for each step (`submitted_at`, `confirmed_at`, `announced_at`,
  `cancelled_at`), a `decline_reason` and `rescheduled_at`; see the lifecycle below.

#### The event's lifecycle

The organizer prepares the event, **the venue confirms the date and the setup**, and the
organizer decides when to announce it. This is how venue-based ticketing works: the venue's
concern is its calendar, its room and its capacity, not the prices.

```
            submit              confirm               announce            cancel
  draft ──────────────> submitted ──────────> confirmed ──────────> announced ──────> cancelled
    ^                      │                     │   ^                  │
    └──── decline ─────────┘                     │   │                  └── reschedule (stays
    └──── withdraw ────────┴─────────────────────┘   │                      announced)
                                    change date, hall or layout: back to submitted
```

| Status | Meaning | Takes the hall's slot | Public |
|---|---|---|---|
| `draft` | Being prepared, private to its organizer; can be deleted | No | No |
| `submitted` | The venue is asked for this date and layout | Yes | No |
| `confirmed` | The venue agreed; the layout is frozen; the organizer finishes tiers, prices and holds, which the venue doesn't approve | Yes | No |
| `announced` | Customers see the event, its seat map and prices; single tickets sell between `on_sale_at` and `off_sale_at` | Yes | Yes |
| `cancelled` | Was announced and won't happen; tickets are refunded (M7) | No | Yes, as cancelled |

- **Declined** by the venue, the event goes back to `draft` with a `decline_reason`; withdrawn by
  the organizer, back to `draft` too. Both free the slot. Until it is announced, nothing can be
  sold, so it can also be deleted.
- **Changing the date, hall or layout** of a confirmed event sends it back to `submitted`, for
  the venue to agree again.
- **Rescheduling** an announced event changes its times and sets `rescheduled_at`: it stays
  announced, its tickets stay valid for the new date, and the no-overlap rule checks the new
  slot. From M2 the venue manager must agree to the new date.
- **Drafts don't take the slot:** the no-overlap rule only counts submitted, confirmed and
  announced events, so two organizers can draft the same Saturday. Submitting is what claims it:
  the second submit for the slot is refused by the database (`23P01`), however close together
  they come. The admin UI can warn while drafting, but only the submit decides.
- **A venue's own events** (a cinema scheduling its showings) are confirmed as soon as they are
  submitted, from M2 when organizations exist. Until M2 there are no roles, so any admin takes
  every step.
- "On sale", "sold out" and "past" aren't statuses: they follow from the times and the tickets.

### What an event sells: tiers, ticket types and prices

A ticket's price depends on two separate questions, and each has its own table:

| Question | Table | Examples |
|---|---|---|
| **Where** is the ticket? | `ticket_tiers`: a price level. `kind` `seated` (the customer picks seats) or `standing` (the customer picks a quantity); an optional per-customer limit | "Stalls", "Balcony", "VIP", "Floor", "Floor early bird" |
| **Who** is it for? | `ticket_types`: per event, with a description of who qualifies (checked at the door) and a `visibility`: `public`, or `admin` for types only admins can pick | "Adult", "Student", "Child"; or just "Standard"; "Comp" (admin) |
| **What does it cost?** | `tier_prices`: one row per tier and ticket type, the price in minor units of the event's currency (`45000` is 450.00 TRY) | Stalls + Adult = 450.00 |

So the prices of an event form a matrix:

```
              Adult     Student
 Stalls       450.00    300.00
 Balcony      300.00    200.00
 VIP          900.00    -          no row: Student tickets aren't sold in VIP
```

**An event with only one kind of ticket**, such as only standard adult tickets, is the smallest
case: one ticket type ("Standard" or "Adult") and one price per tier. A standing-only club night
is one tier ("Floor"), one ticket type ("Standard") and one price. There is no hidden default
type: every price needs a ticket type, so every event has at least one.

The ticket type isn't stored on the ticket. A seat is a seat; the customer chooses "Adult" or
"Student" when booking, and the booking records the type and the price it had (M3).

### The inventory: tickets

`tickets`: one row per thing that can be sold once: one seat, or one place in a standing
section. 1,500 seats in tiers plus 3,000 standing places on the floor is 4,500 ticket rows for
that event.
A ticket has:

- `event_id`, `tier_id`: which event and which tier sells it;
- `kind`, `section_id`, and `seat_id` for a seated ticket (empty for a standing one);
- `status`: `available`, `held` (kept out of sale by an admin hold, `hold_id`) or `killed`
  (removed from sale for good, such as a seat behind a camera).

Not on the ticket: whether it is sold (a booking's items say so, M3) and its ticket type (chosen
when booking).

Each seat has at most one ticket per event (`tickets_seat_id_event_id_key`). A ticket's tier is
how the seat is sold, so this one index also means **a seat is in at most one tier of an event**.
The same seat has its own ticket in every other event that uses the layout.

### How tickets are created

Tickets are created **while the event is still a draft**, when an admin gives a tier what it
sells (M1 step 9). They are created in bulk, by one SQL statement that Postgres runs on its own,
without sending rows through Go:

| Admin action | What happens to `tickets` |
|---|---|
| Put section "Block 101" (400 seats) in tier "Stalls" | 400 rows inserted, one per seat: `INSERT INTO tickets … SELECT … FROM seats WHERE section_id = …` |
| Put single seats A-1 to A-4 in tier "VIP" | 4 rows inserted |
| Move seat A-5 from "Stalls" to "VIP" | its ticket's `tier_id` changes |
| Give tier "Floor" 2,500 places of the standing section "Floor" | 2,500 rows inserted: `INSERT … SELECT … FROM generate_series(1, 2500)` |
| Give tier "Floor early bird" 500 places of the same section | 500 more rows: tiers can share a standing section, up to its capacity |
| Lower "Floor" from 2,500 to 2,000 | 500 available "Floor" tickets deleted |
| Hold 20 seats for the artist | an `admin_holds` row, and those 20 tickets become `held` with its `hold_id` |
| Kill 6 seats behind the mixing desk | those tickets become `killed` |

A seat of the layout that no tier was given has no ticket and isn't sold. Every change takes the
event's lock (invariant I14), so two admins can't change the same event's tickets at once.

**After announcing** (M1 step 11) the same actions still work, on **unsold** tickets only: a
ticket that a held (pending payment) or confirmed booking has can't be moved, held or killed, and
a bulk change that includes one is refused as a whole. Two actions change: lowering a quantity or
taking a seat out of a tier **kills** tickets instead of deleting them, and raising a quantity
revives that tier's killed places first. Prices can change too; a new price applies to new
bookings, since a booking copies its price. The layout stays fixed (frozen since the venue
confirmed it), and a new date is a reschedule (see the lifecycle above). The full rule is in
[05 · Decisions](05-decisions.md) ("What can change after announcing").

### An event's data from draft to sale

1. **Create the event** in a layout: an `events` row, `draft`.
2. **Submit it** to the venue: `submitted`, which claims the hall's slot. The venue **confirms**
   it (`confirmed`, the layout is now frozen) or declines it (back to `draft`).
3. **Create the tiers** (`ticket_tiers`) and **the ticket types** (`ticket_types`).
4. **Set the prices**: a `tier_prices` row for each type sold in each tier.
5. **Give the tiers seats and standing places**: tickets are created (above).
6. **Place admin holds and kills** if needed.
7. **Announce**: the service checks that the event is confirmed, every `single` or `shared` tier
   has at least one price and every `pass_only` tier is used by a pass, each standing section's
   tickets fit its capacity, and all tickets fit the hall's capacity; then the status becomes
   `announced`. No rows are created at this point.
8. **Sales open** at `on_sale_at` (or right away) and close at `off_sale_at` (or at the start).
   Customers book tickets (M3): picking seats or a quantity, and a ticket type for each.
9. **Reschedule** or **cancel** if needed: a reschedule keeps the tickets valid; a cancel makes
   the status `cancelled` and its bookings are refunded (M7).

Steps 3 to 6 can start while the event is still a draft. An event that isn't announced can be
deleted: the service deletes its tickets first (tickets refuse to let anything they point at be
deleted), and the event then takes its tiers, ticket types, prices and holds with it.

### Following one ticket through the tables

A customer looks at ticket T for seat A-12 of the event "Duman, 14 March":

```
 tickets T ── tier_id ───► ticket_tiers "Stalls" ── event_id ──► events "Duman, 14 March"
   │                                                               │ layout_id
   ├── seat_id ───► seats A-12 ── section_id ─┐                    ▼
   └── section_id ─────────────────────────────┴► sections "Block 101" ──► layouts "Theater"
                                                                           ──► halls ──► venues

 Price shown: tier_prices (Stalls, Adult) = 450.00 TRY, (Stalls, Student) = 300.00 TRY
```

### What the database guarantees, and what the services check

The database guarantees, with the constraint that does it:

| Rule | How |
|---|---|
| Seats only in seated sections | `seats_section_id_fkey`: seats reference `(id, kind)` of sections, with `section_kind` always `seated` |
| An event's hall is its layout's hall | `events_layout_id_fkey` on `(layout_id, hall_id)` |
| No two events that hold a slot (submitted, confirmed, announced) overlap in one hall, counting the time reserved for setting up and clearing | `events_no_overlap_in_hall`, an exclusion constraint with a `WHERE` |
| An event's times match its status, in order (submitted, then confirmed, then announced, then cancelled) | `events_…_matches_status` and `events_…_after_…` checks |
| A price's tier and ticket type are of the same event | `tier_prices_tier_id_fkey` and `tier_prices_ticket_type_id_fkey`, both over `(…, event_id)` |
| A ticket's tier is of the ticket's event and kind | `tickets_tier_id_fkey` on `(tier_id, event_id, kind)` |
| A standing ticket is in a standing section; a seated ticket has a seat of its section | `tickets_section_id_fkey` on `(section_id, kind)`, `tickets_seat_id_fkey` on `(seat_id, section_id)`, `tickets_seat_only_seated` |
| A held ticket has a hold of its own event | `tickets_hold_only_held`, `tickets_hold_id_fkey` on `(hold_id, event_id)` |
| One ticket per seat and event, so one tier per seat | `tickets_seat_id_event_id_key` |
| Names unique per parent, ignoring case | `…_name_key` unique indexes |

The services check what the database can't see, with the event locked:
- a ticket's section is in the event's layout (so a draft changes layout only while it has no
  tickets);
- a standing section's tickets, of all tiers together, fit its capacity;
- at announce, every `single` or `shared` tier has at least one price, and every `pass_only` tier
  is used by a pass.

## Which events the model supports

**Supported:**

| Event | How it fits |
|---|---|
| Reserved-seat theater, opera, seated concerts, cinema showings | Seated sections and seated tiers; each showing is its own event |
| Standing concerts, club nights | Standing sections with a capacity, standing tiers with quantities |
| Mixed arena shows (seated blocks and a standing floor) | One layout with both kinds of section |
| One hall in different setups (concert one night, basketball the next) | Several layouts per hall; events never overlap in a hall |
| Several price levels; early bird and regular sharing one floor | Several tiers; several standing tiers in one section |
| Adult, student, child (or only "Standard") prices | Ticket types and the price matrix |
| Free events | A price of 0 |
| Per-customer limits | `per_customer_limit` on the event, the tier and the pass (enforced in M3) |
| Complimentary tickets, box-office rates | Ticket types with `admin` visibility, which customers never see |
| Guest lists, press and sponsor seats, camera positions | Admin holds and kills |
| Announced first, on sale later; online sales closing before the start | `on_sale_at`, `off_sale_at` |
| Passes on sale before day tickets | A pass has its own `on_sale_at`; the event's sales times are for single tickets |
| A venue confirming each organizer's date and setup | The lifecycle: `submitted`, then `confirmed` by the venue |
| Load-in and load-out between events | `hall_reserved_from` / `hall_reserved_until`, used by the no-overlap rule |
| A show moved to another date | Rescheduling: the event stays announced and its tickets stay valid |
| Multiplexes, buildings with several rooms running at once | The no-overlap rule is per hall |
| A film's run, a theater season, a team's home matches, a festival's days | An event series groups the events; each keeps its own date, tiers and prices |
| Rescaling while on sale (move unsold seats to another tier, reprice, late holds and kills) | Unsold tickets stay editable after announcing; bookings copy their price (M1 step 11) |

**Not supported yet:**

| Event or feature | Why not |
|---|---|
| Festival passes, season tickets, multi-day tickets | Their tables exist (`passes`, `pass_prices`, `pass_events`, migrations 13 to 15, see 12 · Domain model); selling them comes with bookings (M3) |
| An event using several halls at once (a conference, a whole-venue festival) | An event has one layout, so one hall |
| Timed entry (museum slots) | Possible as many small standing events, but clumsy |
| Tables or boxes sold only as a whole | Each box or table is its own section and its tier is `sold_as = section` (the column exists); the booking rule that takes the section whole comes in M3 |
| Online or streamed events, events without a venue | Every event needs a hall |
| "Postponed, new date not known yet" | A new date is a reschedule; without one, the event stays at its old date or is cancelled (it would need an empty start time and paused sales) |
| Several tentative holds on one date ("first hold, second hold") | Only one event can be submitted for a slot at a time; a second waits until the first is declined or withdrawn |
| Rules per ticket type (a child needs an adult, a limit per type), presales and sales windows per tier | Only the description says who qualifies; enforced tier rules come later |
| Fees, taxes, automatic dynamic pricing | Not modelled yet (an admin can change prices by hand); fees and taxes are rounded to minor units when they come |
| A drawn seat map (section shapes, curved rows, the stage) | The seat map is a grid per section from the row and seat positions; a drawing per layout comes with the admin UI |

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
2. **Open the event.** The event page loads from **Postgres**: tiers, ticket types and their
   prices, and, for a seated event, the seat map with each seat's state (free, or taken by an
   active booking).
3. **Pick seats.** The user clicks B-7-12 and B-7-13 and picks a ticket type for each (Adult,
   Student). Clicking is only a selection in the
   frontend; **nothing is reserved yet**, and no Redis key exists.
4. **Reserve.** The user presses "Continue", which sends `POST /bookings` with the two tickets and
   an `Idempotency-Key` header (the user must be logged in through Keycloak). The **booking**
   module:
   1. checks the idempotency key: a retry or a double-click returns the same booking instead of
      creating a second one (invariant I6);
   2. runs the booking rules: the event is announced and on sale (between `on_sale_at` and
      `off_sale_at`; for a pass, the pass's own sales times), each ticket type has a price in the
      ticket's tier, the per-customer limits aren't exceeded, and so on;
   3. picks a new booking ID and tries `SET NX` in Redis for each ticket. If any key is already
      taken, it stops right away with "seat no longer available" and releases the keys it did get;
   4. in **one Postgres transaction**: inserts the booking (PENDING_PAYMENT, `expires_at`), its
      lines ("1 × Stalls Adult, €12" and "1 × Stalls Student, €8", the type and its price copied
      from `tier_prices`, invariant I4) and its items
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
| Per-customer limits per event and per tier | Count **held and confirmed** tickets, so one account can never hold more than the limit (e.g. 4) at once. |
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
