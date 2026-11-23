# 12 · Domain model

Every table the finished system needs, how they relate, and which milestone builds each. The
tables marked **built** exist (migrations 1 to 15, M1 step 6a); the rest are **planned**: their place in the model is
decided here so that adding them later means new tables and new nullable columns, not changing
what an existing row means. Column-level details of the built tables are in the migrations and in
[10 · System flows](10-system-flows.md#how-the-event-sides-tables-fit-together); why they look the
way they do is in the [decisions](05-decisions.md).

## The rule: settle what a row is

Adding a table or a nullable column later is cheap: one new migration, and nothing built before
changes. What is expensive is changing what a row **means** (its grain), because every query and
service built on it changes too. So the model fixes the grain of each core table now:

| Table | One row is | Status |
|---|---|---|
| `seats` | One physical seat of one layout's seated section | Built |
| `event_series` | A group of events sold and shown as one production | Built |
| `events` | One dated occurrence in one layout of one hall | Built |
| `ticket_tiers` | Where a ticket is, in one event (a price level) | Built |
| `ticket_types` | Who a ticket is for, in one event (Adult, Student) | Built |
| `tickets` | One admission unit of one event: one seat, or one standing place | Built |
| `passes` | One product that grants a ticket in each of several events of a series | Built (migration 13); sold once bookings exist (M3) |
| `bookings` | One customer's order, which can span several events | Planned, M3 |
| `booking_items` | One ticket in one booking, with what was paid for it | Planned, M3 |

Everything else (fees, presales, check-ins, transfers) attaches to these without changing them.

## The map

```
 ACCESS (M2)            organizations ─< memberships (users from Keycloak)
                             │  └──< venue_access_grants >── venues
                             │ owns venues, events and series

 THE ROOM (built)       venues ─< halls ─< layouts ─< sections ─< seats

 THE CATALOG (built)    event_series ─< events >── layouts
                             │            ├─< ticket_tiers ─┐
                             │            ├─< ticket_types ─┴─< tier_prices
                             │            ├─< admin_holds
                             │            └─< tickets >── tier, section, seat, hold
                             │
 PASSES (built)              └─< passes ─< pass_events >── (event, tier)
                                    └───< pass_prices

 ORDERS (M3)            bookings ─< booking_items >── tickets
                           │            └──> booking_passes (when bought as part of a pass)
                           └─< booking_passes >── passes, pass_prices

 MONEY AND RELIABILITY  idempotency_keys (M4), payments and webhook events (M6),
                        refunds (M7), outbox (M8)

 ENTRY (later)          check_ins >── booking_items

 A ─< B: one A has many B. A ──> B: A may point at one B.
```

| Area | Tables | Milestone |
|---|---|---|
| The room | `venues`, `halls`, `layouts`, `sections`, `seats` | Built (M1) |
| The catalog | `event_series`, `events`, `ticket_tiers`, `ticket_types`, `tier_prices` | Built (M1) |
| Inventory | `admin_holds`, `tickets` | Built (M1) |
| Passes | `passes`, `pass_prices`, `pass_events`; `ticket_tiers.sold_via` | Built (migrations 8, 13 to 15); sold once bookings exist (M3) |
| Tables and boxes | `ticket_tiers.sold_as` | Built (migration 8); the whole-section booking rule in M3 |
| Access | `organizations`, `memberships`, `venue_access_grants`; `venues.owner_org_id`, `events.organizer_id`, `event_series.organizer_id` | M2 |
| Orders | `bookings`, `booking_items`, `booking_passes` | M3 |
| Reliability | `idempotency_keys` (M4), `payments` and webhook events (M6), `refunds` (M7), `outbox` (M8) | Later milestones |
| Entry and extras | `check_ins`, presale windows per tier, fee lines on bookings, transfers | Additive, when needed |

## Event series (built)

An event series groups events: a film's showings, a theater run, a team's home matches in a
season, the days of a festival. **Each event of a series is still a full event of its own**, with
its own hall, layout, times, tiers, prices and tickets; the series only groups them. That is how
the industry models it: cinema systems have a film with many sessions (each session one screen
at one time), performing-arts systems a production with many performances, Eventbrite a series
with one child event per date.

Keeping each date a separate event is what makes it flexible:

- a showing can move to another hall, or a festival day to another venue, without affecting the
  others;
- each date has its own capacity, its own tickets and its own door, so a Saturday ticket can't
  get anyone in on Sunday;
- the no-overlap rule keeps working per hall: two showings in one screen can't overlap, which a
  single "30 showings" row could never check.

What the series adds on top: one page per film or festival for customers ("all showtimes"), one
place for admins to see and report on a whole run, one search result instead of 30 (M8), and the
container that passes belong to.

An admin doesn't fill in 30 forms. Like the bulk creation of tickets, a **"schedule showings"**
action (service code) creates the events of a series from a pattern (dates × times × halls) and
copies tiers, ticket types and prices from a template event; each event can then be changed on
its own (a matinee cheaper, one showing moved to Screen 5). An event needs no series; a series
can span venues (a tour). A series with events can't be deleted. Shared details of a run (a
film's running time or age rating) can be added to `event_series` as columns when needed.

## Passes (tables built, sold from M3)

A pass is a product that grants **one ticket in each of several events of one series**: a
festival's 3-day pass, a season ticket for a team's home matches, a theater subscription. The key
point: **a pass has no inventory of its own.** The tickets it grants are ordinary tickets of each
event. So a pass and single tickets can never oversell each other, and booking a pass uses the
same locks and the same "one active booking per ticket" index (I1) as any booking.

### A pass mirrors an event's pricing, one level up

```
ONE EVENT    ticket_tiers   WHERE a ticket is            "Field", "VIP deck"
             ticket_types   WHO it is for                Adult, Student
             tier_prices    the price of WHO in WHERE    Field × Adult = 500.00

A SERIES     passes         WHAT the pass gives          "3-day pass", "3-day VIP pass"
             pass_prices    WHO it is for, and price     Adult 1,300.00, Student 900.00
             pass_events    for each event of the pass, the tier its ticket comes from
```

- **A pass has its own price**, set by the organizer, never computed from the day prices: a 3-day
  pass can cost 1,500.00 (three days at 500.00) or be a deal at 1,300.00. The page can show the
  saving by comparing it with the day prices; nothing stores it.
- **A pass has its own types** (`pass_prices`), because ticket types belong to one event: Friday's
  "Adult" and Saturday's "Adult" are two different rows, and a pass spans both.
- **A pass has no tiers of its own.** `pass_events` says where each day's ticket comes from, and
  that tier decides which places: the field, or the VIP deck (a VIP pass is a second pass whose
  `pass_events` point at each day's VIP tier).

### Shared pool or quota

Every tier says who sells its tickets, in **`ticket_tiers.sold_via`**:

| `sold_via` | Sold | Prices | Can be a pass's tier |
|---|---|---|---|
| `single` (default) | On its own only | Its `tier_prices` | No |
| `pass_only` | Only through passes: a **quota** that single tickets can't take, what most festivals do | None of its own; the pass's price applies | Yes |
| `shared` | Both: a **pool** that single tickets and passes compete for. Simple, but day tickets can sell out the pass | Its `tier_prices` for single tickets | Yes |

The database keeps this straight: a `single` tier can't be put in `pass_events`, and a tier a pass
uses can't be switched back to `single` (switching between `pass_only` and `shared` is allowed).

### Why passes belong to a series

The series is the pass's boundary: a pass can only cover events of its series, so it can't mix in
an unrelated event (another organizer's concert, a different film). The database checks it, and
the series' page is where customers find the day tickets and the passes together.

### The tables

| Table | One row is | Key columns and rules |
|---|---|---|
| `passes` (13) | A pass product | `series_id` (no ON DELETE: a series with passes can't be deleted); name, unique per series ignoring case; optional description; kind `seated` / `standing`; for a seated pass, `seat_selection` `same_seat` / `per_event` (see below); `currency`; optional per-customer limit (separate from the limits of its tiers and events); optional `on_sale_at`, independent of the events' sales times; status `draft` / `announced` / `cancelled` with matching `announced_at` / `cancelled_at` (an event's names, without the venue's steps); version |
| `pass_prices` (14) | One price option of a pass | name ("Adult", "Student"), unique per pass ignoring case; optional description; price in minor units of the pass's currency; visibility `public` / `admin`; version. Deleted with the pass |
| `pass_events` (15) | One event a pass covers, and the tier its ticket comes from there | Primary key `(pass_id, event_id)`, so one tier per event; two passes may share a tier. Copies the pass's series, currency and kind and the tier's `sold_via`, and three foreign keys keep them in line: `(pass_id, series_id, currency, kind)` → `passes`, `(event_id, series_id, currency)` → `events` (its `UNIQUE (id, series_id, currency)`), `(tier_id, event_id, kind, tier_sold_via)` → `ticket_tiers` (`ON UPDATE CASCADE`, so the copy follows a switch between `pass_only` and `shared`). So the event is in the pass's series and currency, and the tier is in that event, of the pass's kind and sold through passes. While a pass covers an event, the event can't be deleted, leave the series or change currency, the tier can't be deleted, and the pass can't change series, currency or kind. Deleted with the pass |

### Example: a 3-day festival

The series "Festival 2027" has three events, Friday, Saturday and Sunday, each in the same
open-air layout with one standing section, "Field" (capacity 30,000).

| Each day's tier | Places | Adult | Student |
|---|---|---|---|
| Day ticket (`single`) | 20,000 | 500.00 | 350.00 |
| 3-day pass (`pass_only`) | 10,000 | none: sold only through the pass | |

```
passes:       "3-day pass"   (standing, at most 4 per customer)
pass_events:  (Friday,   Friday's "3-day pass" tier)
              (Saturday, Saturday's "3-day pass" tier)
              (Sunday,   Sunday's "3-day pass" tier)
pass_prices:  Adult 1,300.00   Student 900.00
```

Ayşe buys one Adult pass:

1. The booking service reads the pass's three `pass_events` rows and, in one transaction,
   reserves **one available ticket from each day's "3-day pass" tier**: three ordinary tickets.
   If any day has none left, nothing is reserved and the pass shows as sold out.
2. The booking records it:
   ```
   bookings:        B1 (Ayşe, total 1,300.00)
   booking_passes:  BP1 (B1, "3-day pass", "Adult", 1,300.00 copied)
   booking_items:   Friday ticket    share 433.34   part of BP1
                    Saturday ticket  share 433.33   part of BP1
                    Sunday ticket    share 433.33   part of BP1
   ```
   Each ticket carries an **allocated share** of the pass price, split in minor units with the
   leftover kuruş on the first, so the shares add up exactly. If Sunday is cancelled, Sunday's
   433.33 can be refunded alone (whether it is, is the organizer's refund policy, M7). Splitting
   in proportion to the day prices instead of equally is a choice for M3; the tables are the same.
3. She gets three QR codes, one per day, each scanned on its own day.

Mehmet buys a Saturday day ticket for 500.00 from Saturday's "Day ticket" tier, another pool: he
and Ayşe can't get the same place.

### What the admin does

The admin shouldn't have to build the quota by hand (a tier in each day, then the links). A
**"create pass"** action (service code) takes the series, the pass's name, which events it covers,
the places per event and the prices, and creates the quota tiers, their tickets and the
`pass_events` rows in one transaction. Afterwards the pass is edited as one thing; its quota tiers
show in each day's tier list, marked as belonging to the pass.

Two rules change once passes exist:

- **Announcing an event:** its `single` and `shared` tiers each need a price; a `pass_only` tier
  needs none, but must be used by a pass, or nothing could sell it.
- **Selling a pass** needs the pass to be announced and past its own `on_sale_at`, and every
  event it covers to be announced (customers see the dates they buy) with none started yet; a
  "half-season" pass is a second pass. **The events' `on_sale_at` and `off_sale_at` are for single
  tickets only**, so a festival can sell its 3-day pass from 1 March and the day tickets from
  1 May: the pass's tickets come from `pass_only` tiers, which single tickets can't reach anyway,
  or from a `shared` tier, which single tickets enter through the event's times and pass tickets
  through the pass's.

### Seated passes: the same seat, or a seat per event

A seated pass says how its seats are chosen, in **`passes.seat_selection`**:

| `seat_selection` | The customer | Needs | Typical use |
|---|---|---|---|
| `same_seat` | Picks one seat once and gets it in every event | Every event of the pass in **one layout or copies of it**, so the seat exists in each under the same `physical_seat_key` (the service checks it when an event is added to the pass, and when a draft event of it changes layout) | A season ticket: the same stadium seat at every home match |
| `per_event` | Picks a seat in each event, or is given the best available one in each | Nothing: the events can be in different layouts, halls or venues | A theater subscription across stages, a festival's seated stands that differ per day |

**Why copies count:** a seat row belongs to one layout, so when the club copies its frozen
stadium layout mid-season to fix something (M1 step 11), the copy's seats have new ids. Each seat
also has a **`physical_seat_key`**: a new seat gets a new one, a copied seat keeps the key of the
seat it was copied from. Seat 101-C-12 of "Football" and of "Football v2" are two rows with one
key, so a season ticket finds its seat in matches of either. The same key lets an admin kill one
broken seat in every layout of the hall. A layout built from scratch gets new keys; to give a new
setup the same seats (a cup match in another setup), copy a layout and change it.

The layout rule of `same_seat` is a service check, not a database rule: checking it in the
database would mean copying the seats onto the pass and its rows, for one mode of one kind of
pass. Whether picked or assigned, `per_event` is the same in the tables; it is a choice of the
booking screen (M3).

**Example, `same_seat`:** the series "Home matches 2027/28" has 17 events in the stadium's
football layout. Each match has a `pass_only` tier "Season" with blocks 101 to 104. The pass
"Season ticket" has 17 `pass_events` rows. A customer picks seat 101-C-12 once; the booking
reserves **the ticket for seat 101-C-12 in each of the 17 matches** (the same `seat_id`, or the
same `physical_seat_key` in matches that use a copy of the layout), all or nothing, and the seat never shows as free for a single match.

**Example, `per_event`:** the series "Shakespeare season" has three plays, on the main stage and
in the studio, which have different layouts. The pass "3-play subscription" has three
`pass_events` rows, one per play, each pointing at that play's "Subscription" tier. The customer
picks a seat in each play's seat map (or the booking takes the best available one in each); the
booking reserves those three tickets, all or nothing.

### What a first version leaves out

- **Flexible passes** ("any 3 of these 10 shows", a 10-visit cinema card): the customer picks the
  events later. That is a pass purchase with a number of uses, each use a later booking item at no
  extra price, pointing at the purchase. It adds a column and a flow; it doesn't change the tables
  above.
- **A `same_seat` pass with one event in a layout built from scratch** (a cup match in a setup
  that wasn't copied from the pass's layout, so its seats have other keys): that event can't be
  in the pass. Either the pass becomes `per_event`, or the holders are moved to an equivalent seat
  for that match, as real clubs do; the moving would be an admin action. A setup made as a copy
  keeps the keys and works.

## Tables and boxes (built, sold whole from M3)

Real venue maps usually make each box, suite or table its own section ("Box 12", "Table 7"), so a
layout with 40 cabaret tables has 40 seated sections. Whether one is sold whole depends on the
event (the same opera box is sold per seat one night and whole for a gala), so it is a column of
the tier: **`ticket_tiers.sold_as`**, `seat` (default) or `section`. Nothing else is needed in
the tables: the box is a section, its places are seats (or a standing capacity), and its tickets
are created like any others.

**Example:** a gala dinner's layout has 40 seated sections, "Table 1" to "Table 40", of 8 seats
each. The event's tier "Gala table" (`sold_as = section`) is given all 40 sections, which creates
320 tickets. Its prices are per seat: Adult 1,000.00, Child 400.00. A family books Table 12: the
booking takes **all 8 tickets of Table 12 or none**, picks a type for each (6 Adult, 2 Child),
and pays 6,800.00. Another night the same layout is a concert and the same sections go into a
`seat` tier, sold one seat at a time.

| Rule | Enforced by |
|---|---|
| A tier sold as whole sections is sold on its own (`sold_via = single`), never through a pass: a pass takes one ticket per event, which would break a table up | The database (`ticket_tiers_section_sold_alone`); since a pass's tier must be `pass_only` or `shared`, it also keeps whole sections out of passes |
| A section sold whole has all its tickets of the event in that one tier, so it isn't split with another tier | The service, when tiers are given seats (M1 step 9) |
| A booking takes every ticket of the section that isn't killed, or none; a section with a ticket in an admin hold or in another booking isn't offered | The booking service (M3) |
| `per_customer_limit` counts sections, not tickets ("at most 2 tables per customer") | The booking service (M3) |

- It works for standing too: a standing box for 10 is a standing section of capacity 10, sold
  whole.
- Prices stay per ticket, so each guest can have their own type; a table of 4 at 500.00 is
  shown as 2,000.00.
- Not covered: tables inside one big section (that needs a `seat_groups` table and a column on
  seats), a per-table "minimum spend" on drinks (club VIP tables), and **season boxes** (one box
  for every match), which would be a product of their own.

## Per-customer limits (columns built, enforced from M3)

There are three limits, each optional. A booking must keep within every one that applies, so the
lowest one decides. Each counts the customer's held and confirmed bookings, not only the current
one.

| Limit | Column | Counts |
|---|---|---|
| The event's | `events.per_customer_limit` | Single tickets of the event, of all its tiers together |
| A tier's | `ticket_tiers.per_customer_limit` | Single tickets of that tier; for a tier sold as whole sections, sections (tables) |
| A pass's | `passes.per_customer_limit` | Passes of that pass, of all price options together |

- **A pass's tickets count only toward the pass's limit**, not toward the limits of the events
  and tiers they come from. Each limit then means one thing: "4 passes" and "6 Saturday tickets"
  are separate offers, and a customer with 4 passes can still buy day tickets for a friend.
- **Whole sections count only toward their tier's limit**, in tables: a table of 8 would otherwise
  break an event limit of 6 tickets.
- A tier limit above the event's limit is allowed but never reached; the admin UI can warn.

**Example:** a concert with an event limit of 6, a VIP tier limited to 2 and a Floor tier with no
limit. A customer can have 2 VIP and 4 Floor, but not 3 VIP (the tier's limit) and not 7
tickets in all (the event's). That is how real platforms show it: one limit per event
("6 tickets per customer"), sometimes lower on a premium price level.

Enforcement (M3) uses a counter row per customer and event, per customer and tier, and per
customer and pass, updated in the booking's transaction with a `CHECK (count <= limit)` (see the
decisions), so two parallel requests of one customer can't both slip under a limit.

## Ticket types only admins can pick (built)

`ticket_types.visibility` and `pass_prices.visibility` are `public` (the default: customers see
and pick it) or `admin` (only admins can pick it, and customers never see it). It is what makes
these possible without opening them to everyone:

- a **"Comp" type at 0.00**, used when an admin books a hold's tickets for the artist's guests or
  the press;
- a **box-office or staff rate** that only admins sell;
- a **free invitation pass** (`pass_prices`, 0.00).

A type unlocked by a presale code becomes a third value when codes are built.

## Bookings are orders (M3)

A booking is a customer's order, **not tied to one event**: `bookings` has no `event_id`, and
each booking item's ticket says its event. A pass booking spans several events, and theater
systems commonly sell several performances in one basket, so tying bookings to one event would
force a redesign of bookings the day passes are sold. Rules that are per event (the per-customer
limit, "one open booking per event per customer") are checked through the items. All items of a
booking are in one currency.

A booking item copies what was sold: the tier's and ticket type's names and the price (or the
allocated share of a pass price), so later renames and price changes don't alter a past sale.
When M3 designs `booking_items`, a foreign key from `(tier_id, ticket_type_id)` to `tier_prices`
can make the database refuse a type that isn't sold in the ticket's tier.

## After the sale: the ticket a customer gets

- **The PDF and its QR code** are Go code, with no table: the QR code holds a signed token (an
  HMAC over the booking item's id), so nobody can make a valid one without the server's key. A
  refunded or cancelled booking's QR code stops working because the booking's status changes.
- **Emailing it** after payment must happen only once the payment is committed, and must survive
  a crash in between: the app writes "send the tickets" to the outbox in the same transaction, and
  a worker sends it. The outbox is already planned (M8); the email would be its first user.
- **Scanning at the door, once:** a `check_ins` table (booking item, time, gate) when entry is
  built. Barcodes that change every few seconds (as Ticketmaster's SafeTix does, against
  screenshots) would add a secret per booking item. Both are additions.

## Known limits

None of these needs an existing table to change; each is an addition when it is wanted:

| Not covered | What it would add |
|---|---|
| Flexible passes and visit cards | A use count on the pass purchase and a redemption flow |
| Tables inside one large section | `seat_groups` and `seats.group_id` |
| Season boxes and suites (one box for every event of a series) | A box product of its own next to passes |
| Minimum spend, add-ons (parking, merchandise) | Product lines on bookings |
| Presales, promo codes, sales windows per tier | Sales-window rows per tier; a codes table |
| Fees and taxes | Fee lines on bookings |
| Resale and transfer | Transfers of booking items between customers |
| An event across several halls (a conference) | An event having several layouts |
| Online events | Events without a hall |
| Event categories, performers, an age limit (18+); search filters by genre or artist (M8) | `events.category` (or a categories table), a `performers` table linked to events, `events.min_age` |
| The gate or entrance printed on the ticket; levels or zones grouping sections ("Lower Bowl") | `sections.entrance`; an area or level column, or a table grouping sections |
| Named tickets: a holder's name or ID per ticket (festivals, Turkish football's Passolig) | Holder columns on `booking_items` (M3) |
| A price per seat (premium or dynamic pricing of single seats) | A nullable price override on `tickets` |
| Halls that split or combine (Hall A is A1 + A2): the no-overlap rule doesn't see that an event in A clashes with one in A1 | A relation between a hall and its parts, checked when events are created |
| A companion seat paired with its wheelchair space, sold together | `seats.companion_of` |
| A series page announced before any of its events is announced ("coming soon") | A status on `event_series` |
| "Postponed, new date not known yet" (a new date is already a reschedule) | A `postponed` status that pauses sales and shows "new date to be announced" (the old date stays in `starts_at` until the reschedule) |
| Several tentative holds on one date ("first hold, second hold"): only one event can be submitted for a slot at a time | Ranked date requests next to `events`, the first one holding the slot |
