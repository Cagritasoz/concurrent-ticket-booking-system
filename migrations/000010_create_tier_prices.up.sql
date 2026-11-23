-- The price matrix of an event: what one ticket type costs in one tier.
--
--              Adult    Student
--   Stalls     450.00   300.00
--   Balcony    300.00   200.00
--   VIP        900.00   (none)
-- No row exists for tier_prices that references VIP and Student.
-- A ticket type without a row in a tier isn't sold there (no Student price in VIP). Publishing
-- checks that every tier has at least one price; the service does that, since the database can't
-- require a child row to exist.
CREATE TABLE tier_prices (
    tier_id        uuid         NOT NULL,
    ticket_type_id uuid         NOT NULL,
    -- The event of both the tier and the ticket type: the two foreign keys below compare it with
    -- each, so a price can't mix a tier of one event with a ticket type of another.
    event_id       uuid         NOT NULL,
    -- Minor units of the event's currency (events.currency): 45000 is 450.00 TRY. Integers, not
    -- numeric: exact, what Stripe takes, and plain int64 in Go. bigint, since some currencies
    -- have large amounts. 0 is a free ticket.
    price_amount   bigint       NOT NULL CHECK (price_amount >= 0),

    -- Prices are edited one at a time, so they have a version like the other admin rows (I15).
    version        integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at     timestamptz  NOT NULL DEFAULT now(),
    updated_at     timestamptz  NOT NULL DEFAULT now(),

    -- One price per tier and ticket type. tier_id comes first, so it also finds a tier's prices,
    -- for the list and for the tier's foreign key.
    -- Many-to-many relationship
    PRIMARY KEY (tier_id, ticket_type_id),
    -- Deleting a tier or a ticket type deletes its prices.
    CONSTRAINT tier_prices_tier_id_fkey FOREIGN KEY (tier_id, event_id)
        REFERENCES ticket_tiers (id, event_id) ON DELETE CASCADE,
    CONSTRAINT tier_prices_ticket_type_id_fkey FOREIGN KEY (ticket_type_id, event_id)
        REFERENCES ticket_types (id, event_id) ON DELETE CASCADE
);

-- A ticket type's prices: for its foreign key, and to list where it is sold. The primary key
-- finds a tier's prices.
CREATE INDEX tier_prices_ticket_type_id_idx ON tier_prices (ticket_type_id);
