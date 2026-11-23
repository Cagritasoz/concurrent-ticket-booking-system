# 12 · Domain model

Every table the finished system needs, how they relate, and which milestone builds each. The
tables marked **built** exist (M1 step 6a); the rest are **planned**: their place in the model is
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
| `passes` | One product that grants a ticket in each of several events of a series | Planned |
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
 PASSES (planned)            └─< passes ─< pass_tiers >── (event, tier)
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
| Passes | `passes`, `pass_tiers`, `pass_prices` | Planned; sold once bookings exist (after M3) |
| Tables and boxes | `ticket_tiers.sold_as` | Planned, with the booking rules (M3) |
| Access | `organizations`, `memberships`, `venue_access_grants`; `venues.owner_org_id`, `events.organizer_id`, `event_series.organizer_id` | M2 |
| Orders | `bookings`, `booking_items`, `booking_passes` | M3 |
| Reliability | `idempotency_keys` (M4), `payments` and webhook events (M6), `refunds` (M7), `outbox` (M8) | Later milestones |
| Entry and extras | `check_ins`, presale windows per tier, fee lines on bookings, transfers | Additive, when needed |

## Event series (built)

An event series groups events: a film's showings, a theater run, a team's home matches in a
season, the days of a festival. Each event of a series keeps its own date, layout, tiers and
prices, because a matinee is often cheaper than an evening and a derby dearer than other matches;
setting up a showing like an earlier one is a "copy setup" action in the service, not a shared
row. Performing-arts systems work the same way (a production, its season, its performances, each
with its own price map). An event needs no series; a series can span venues (a tour). A series
with events can't be deleted. Series are what passes hang on.

## Passes (planned)

A pass is a product that grants **one ticket in each of several events of one series**: a
festival's 3-day pass, a season ticket for a team's home matches, a theater subscription. The key
point: **a pass has no inventory of its own.** The tickets it grants are ordinary tickets of each
event, taken from a tier named for it. So a pass and single tickets can never oversell each other,
and booking a pass uses the same locks and the same "one active booking per ticket" index (I1) as
any booking.

### The tables

| Table | One row is | Key columns and rules |
|---|---|---|
| `passes` | A pass product | `series_id`; name, description; kind `seated` / `standing`; per-customer limit; status and version like other admin tables |
| `pass_tiers` | In one event, the tier a pass takes its ticket from | Primary key `(pass_id, event_id)`, so one tier per event; foreign keys make sure the tier belongs to that event (`(tier_id, event_id)` → `ticket_tiers`) and the event to the pass's series (`(event_id, series_id)` → `events`, which then needs a `UNIQUE (id, series_id)`) |
| `pass_prices` | One price option of a pass | name ("Adult", "Student"), price in minor units. A pass has its own price list, because ticket types belong to one event and a pass spans several |

### Example: a 3-day festival

The series "Rock'n Coke 2027" has three events, Friday, Saturday and Sunday, each in the same
open-air layout with one standing section, "Field" (capacity 30,000).

1. Each day-event gets two standing tiers in "Field": **"Day ticket"** with 20,000 places and
   **"3-day pass"** with 10,000 places (tickets created as usual, 30,000 per day).
2. The pass "3-day pass" has three `pass_tiers` rows: (Friday, its "3-day pass" tier),
   (Saturday, ...), (Sunday, ...).
3. Its `pass_prices`: "Adult" 3,000.00 TRY, "Student" 2,100.00 TRY.
4. A customer buys one Adult pass. The booking reserves **three tickets**, one available ticket
   of each day's "3-day pass" tier, in one transaction, all or nothing.
5. The booking has one `booking_passes` row (the pass, "Adult", 3,000.00 copied) and three
   `booking_items`, one per ticket, each with an **allocated share** of the price (1,000.00), so
   that if Sunday is cancelled, Sunday alone can be refunded.
6. When the 10,000 pass places of any day are gone, the pass is sold out; day tickets go on
   selling from their own tier. Organizers set the split between the two tiers, and can move
   places between them while unsold (the step 11 rules).

### Example: a season ticket for the same seat

The series "Home matches 2027/28" has 17 events in the stadium's football layout.

1. Each match has a tier "Season" holding the seats sold as season tickets, say blocks 101 to 104.
2. The seated pass "Season ticket" has 17 `pass_tiers` rows, one per match.
3. A customer picks seat 101-C-12 once. The booking reserves **the ticket for seat 101-C-12 in
   each of the 17 matches** (the same `seat_id`, since every match uses one layout), all or
   nothing. The service checks when the pass is set up that its events share one layout.
4. A seat bought this way is sold in every match, so it never shows as free for a single match;
   a season-ticket holder who can't come could later release it for resale (not modeled).

### What a first version leaves out

- **Flexible passes** ("any 3 of these 10 shows", a 10-visit cinema card): the customer picks the
  events later. That is a pass purchase with a number of uses, each use a later booking item at no
  extra price, pointing at the purchase. It adds a column and a flow; it doesn't change the tables
  above.
- **A seated pass across different layouts** (a cup match in another setup): the seat ids differ,
  so the holder has to be moved to an equivalent seat. Real clubs relocate season-ticket holders
  in that case; here it would be an admin action.
- Every event of a pass must be in one currency, since one payment has one currency; the service
  checks this when the pass is set up.

## Tables and boxes (planned)

Real venue maps usually make each box, suite or table its own section ("Box 12", "Table 7"), so a
layout with 40 cabaret tables has 40 seated sections. Whether one is sold whole depends on the
event (the same opera box is sold per seat one night and whole for a gala), so it is a column of
the tier: **`ticket_tiers.sold_as`**, `seat` (default) or `section`.

- A `section` tier sells each of its sections only whole: a booking takes all of that tier's
  tickets in the section that aren't killed, or none. M3's booking service checks it.
- It works for standing too: a standing box for 10 is a standing section of capacity 10 sold
  whole.
- Prices stay per ticket: a table of 4 at 500.00 each is shown as 2,000.00.
- Not covered: tables inside one big section (that needs a `seat_groups` table and a column on
  seats), and a per-table "minimum spend" on drinks (club VIP tables), which is a different
  product.

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
| Minimum spend, add-ons (parking, merchandise) | Product lines on bookings |
| Presales, promo codes, sales windows per tier | Sales-window rows per tier; a codes table |
| Fees and taxes | Fee lines on bookings |
| Resale and transfer | Transfers of booking items between customers |
| An event across several halls (a conference) | An event having several layouts |
| Online events | Events without a hall |
