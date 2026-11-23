-- A ticket tier is a price level of an event: where the ticket is. "Stalls", "VIP", "Floor early
-- bird". It is either seated (it sells chosen seats) or standing (it sells a number of places in
-- standing sections); the customer picks seats for the first and a quantity for the second.
--
-- What a tier sells is its tickets (migration 12): putting seats or standing places in a tier
-- creates their tickets, also while the event is a draft, so there is no second list to keep in
-- sync with them. What a tier costs depends on who the ticket is for: its prices per ticket type
-- ("Adult", "Student") are in tier_prices (migrations 9 and 10). A booking copies the price when it
-- is made (M3), so what a customer pays never changes afterwards. Enforced tier rules (sales
-- windows, presale codes) come later, as new tables next to this one. A pass (migration 13) can
-- take its tickets in an event from one of its tiers (pass_events, migration 15).
CREATE TABLE ticket_tiers (
    id                 uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Deleting a draft event deletes its tiers. A tier with tickets can't be deleted (the
    -- tickets' foreign key refuses it), so the service removes a draft's tickets first.
    event_id           uuid         NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    name               text         NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    -- What the tier includes, e.g. "Includes a drink and early entry".
    description        text                  CHECK (length(btrim(description)) BETWEEN 1 AND 2000),
    kind               text         NOT NULL CHECK (kind IN ('seated', 'standing')),
    -- At most this many tickets of the tier per customer, of all ticket types together, counted
    -- over the customer's bookings (M3); no limit when NULL. For a tier sold as whole sections it
    -- counts sections ("at most 2 tables per customer"), since one table is several tickets.
    -- Tickets that come with a pass don't count (the pass has its own limit). The event's limit
    -- (events.per_customer_limit) applies on top: "6 per event, of which at most 2 VIP".
    per_customer_limit integer               CHECK (per_customer_limit > 0),
    -- How the tier's places are sold. seat: one at a time, the usual case. section: each of its
    -- sections only whole, all of the tier's tickets there that aren't killed in one booking or
    -- none (a box, a table, a standing box for 10); real venue maps make each box or table its
    -- own section. It is per tier, so per event: the same opera box is sold per seat one night
    -- and whole for a gala. Prices stay per ticket (a table of 4 at 500.00 shows as 2,000.00),
    -- and each ticket of the table can be of its own type (two adults and two children).
    -- Checked by the services: a section sold whole has all its tickets of the event in this one
    -- tier, so it can't be split with another tier (when tiers are given seats, M1 step 9); and a
    -- booking takes the whole section or nothing, so a section with a ticket in an admin hold or
    -- in another booking isn't offered (M3).
    sold_as            text         NOT NULL DEFAULT 'seat' CHECK (sold_as IN ('seat', 'section')),
    -- Who sells the tier's tickets. single: sold on their own, at the tier's prices. pass_only:
    -- only through passes (pass_events, migration 15), a pass's quota, so the tier has no prices
    -- of its own and announcing doesn't ask for one. shared: both, a pool single tickets and
    -- passes compete for. Only pass_only and shared tiers can be a pass's tier (pass_events
    -- checks it), and a tier can't go back to single while a pass uses it.
    sold_via           text         NOT NULL DEFAULT 'single'
                                    CHECK (sold_via IN ('single', 'pass_only', 'shared')),

    version            integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at         timestamptz  NOT NULL DEFAULT now(),
    updated_at         timestamptz  NOT NULL DEFAULT now(),

    -- A tier sold as whole sections is sold on its own: a pass takes one ticket per event, which
    -- would break a table up. Since only pass_only and shared tiers can be a pass's tier, this
    -- also keeps whole sections out of passes, and a pass's tier can't be switched to section.
    -- Season boxes (a box for every match) would be a product of their own.
    CONSTRAINT ticket_tiers_section_sold_alone CHECK (sold_as = 'seat' OR sold_via = 'single'),

    -- id alone is already unique; these let other tables reference more than the id, so the
    -- database keeps related rows together. (id, event_id): a tier's prices are for ticket types
    -- of the same event (tier_prices). (id, event_id, kind): a tier's tickets are in its own event
    -- and of its kind (tickets). (id, event_id, kind, sold_via): a pass's tier is in the covered
    -- event, of the pass's kind, and sold through passes (pass_events).
    CONSTRAINT ticket_tiers_id_event_id_key UNIQUE (id, event_id),
    CONSTRAINT ticket_tiers_id_event_id_kind_key UNIQUE (id, event_id, kind),
    CONSTRAINT ticket_tiers_id_event_id_kind_sold_via_key UNIQUE (id, event_id, kind, sold_via)
);

-- Two tiers of one event can't share a name, ignoring case: the customer picks a tier by its
-- name. It also finds an event's tiers, for the foreign key and the list.
CREATE UNIQUE INDEX ticket_tiers_event_id_name_key ON ticket_tiers (event_id, lower(name));
