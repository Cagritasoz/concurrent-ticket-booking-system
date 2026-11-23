-- A pass is a product that grants one ticket in each of several events of one series: a
-- festival's 3-day pass, a season ticket for a team's home matches, a theater subscription. It
-- mirrors an event's pricing one level up: an event has tiers (where), ticket types (who) and
-- tier_prices; a pass has its price options (pass_prices, migration 14: who, and for how much)
-- and, for each event it covers, the tier its ticket comes from (pass_events, migration 15).
--
-- A pass has no inventory of its own: the tickets it grants are ordinary tickets of each event
-- (migration 12), so passes and single tickets can never oversell each other. Its price is its
-- own, never computed from the events' prices: a 3-day pass can be a deal at 1,300.00 next to
-- day tickets at 500.00. A booking reserves one ticket per covered event and copies the price
-- (M3). The full design, with examples, is in docs/open/12-domain-model.md.
--
-- Rules left to the service, which can't be seen from one row: a same_seat pass's events use one
-- layout or copies of it, so each seat of the pass is in every event under the same
-- physical_seat_key (seats, migration 5); checked when an event is added to the pass, and when a
-- an event of it that isn't confirmed yet changes layout. The seat a same_seat pass books is found in each event by
-- that key and must be in the pass's tier there (M3). Checking the seats in the database would
-- mean copying them onto the pass and its rows, for one mode of one kind.
--
-- When a pass is sold, also checked by the service (M3): the pass is announced and past its own
-- on_sale_at, every event it covers is announced (customers see the dates they buy) and none has
-- started yet; a "half-season" pass is a second pass. The events' on_sale_at and off_sale_at are
-- for single tickets only, so passes can go on sale weeks before the day tickets.
CREATE TABLE passes (
    id                 uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The series is the pass's boundary: it only covers events of this series (checked by
    -- pass_events). No ON DELETE: a series with passes can't be deleted.
    series_id          uuid         NOT NULL REFERENCES event_series (id),
    name               text         NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    -- What the pass includes, e.g. "Entry on all three days, re-entry allowed".
    description        text                  CHECK (length(btrim(description)) BETWEEN 1 AND 2000),
    -- seated: one chosen seat in every event; standing: one place in every event. Its tiers in
    -- pass_events are of this kind.
    kind               text         NOT NULL CHECK (kind IN ('seated', 'standing')),
    -- How a seated pass's seats are chosen; NULL for a standing pass. same_seat: one seat, picked
    -- once, in every event (a season ticket), which needs the seat to exist in each: the events
    -- use one layout or copies of it, which keep each seat's physical_seat_key. per_event: a seat
    -- in each event, picked by the customer or assigned (best available) when booking, so the
    -- events can be in different layouts or halls.
    seat_selection     text                  CHECK (seat_selection IN ('same_seat', 'per_event')),
    -- The currency of its prices, and of every event it covers (pass_events checks it), since one
    -- payment is in one currency. ISO 4217, like events.currency.
    currency           text         NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    -- At most this many passes per customer, of all price options together (M3); no limit when
    -- NULL. Separate from the limits of the tiers it takes tickets from and of the events it
    -- covers: a pass's tickets count only here.
    per_customer_limit integer               CHECK (per_customer_limit > 0),
    -- When sales of the pass open; NULL means as soon as it is announced. Independent of the
    -- events' on_sale_at: passes often go on sale first. Sales close when the first covered
    -- event starts.
    on_sale_at         timestamptz,

    -- The names of an event's lifecycle (migration 7), without the venue's steps, since a pass
    -- takes no hall time of its own. draft: being set up, not visible, can be deleted.
    -- announced: visible, sold from on_sale_at. cancelled: was announced and is no longer sold;
    -- refunds of sold passes come with M7. Changed only by compare-and-set queries, like an
    -- event's status.
    status             text         NOT NULL DEFAULT 'draft'
                                    CHECK (status IN ('draft', 'announced', 'cancelled')),
    announced_at       timestamptz,
    cancelled_at       timestamptz,
    version            integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at         timestamptz  NOT NULL DEFAULT now(),
    updated_at         timestamptz  NOT NULL DEFAULT now(),

    CONSTRAINT passes_seat_selection_only_seated CHECK ((kind = 'seated') = (seat_selection IS NOT NULL)),
    -- The timestamps match the status, as for events.
    CONSTRAINT passes_announced_at_matches_status CHECK ((announced_at IS NULL) = (status = 'draft')),
    CONSTRAINT passes_cancelled_at_matches_status CHECK ((cancelled_at IS NULL) = (status <> 'cancelled')),
    CONSTRAINT passes_cancelled_after_announced CHECK (cancelled_at >= announced_at),

    -- id alone is already unique; this lets pass_events reference the pass together with its
    -- series, currency and kind, so each covered event is of the same series and currency and
    -- each tier of the same kind. It also means those three can't change while the pass covers
    -- events.
    CONSTRAINT passes_id_series_id_currency_kind_key UNIQUE (id, series_id, currency, kind)
);

-- Two passes of one series can't share a name, ignoring case: the customer picks a pass by its
-- name. It also finds a series' passes, for the foreign key and the list.
CREATE UNIQUE INDEX passes_series_id_name_key ON passes (series_id, lower(name));
