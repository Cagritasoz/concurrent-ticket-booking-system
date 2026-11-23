-- A ticket type is who a ticket is for, or which offer it is: "Adult", "Student", "Child",
-- "Senior". It is separate from the tier, which is where the ticket is: one seat of the "Stalls"
-- tier can be sold as an Adult ticket for 450.00 or a Student ticket for 300.00. Real platforms
-- split the two the same way (price levels and ticket types). A simple event has one type, e.g.
-- "Standard". What each type costs in each tier is in tier_prices (migration 10).
--
-- The customer picks the type when booking, so a ticket (migration 12) has none: the booking
-- records the type and its price (M3). Whether a buyer qualifies (a student ID, a child's age) is
-- checked at the door; the description tells the customer what is needed.
CREATE TABLE ticket_types (
    id          uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Deleting a draft event deletes its ticket types.
    event_id    uuid         NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    name        text         NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    -- E.g. "With a valid student ID, shown at the entrance".
    description text                  CHECK (length(btrim(description)) BETWEEN 1 AND 2000),

    version     integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at  timestamptz  NOT NULL DEFAULT now(),
    updated_at  timestamptz  NOT NULL DEFAULT now(),

    -- (id, event_id) is unique, so tier_prices can reference both and the database keeps a
    -- price's ticket type in the same event as its tier.
    CONSTRAINT ticket_types_id_event_id_key UNIQUE (id, event_id)
);

-- Two ticket types of one event can't share a name, ignoring case: the customer picks one by its
-- name. It also finds an event's ticket types, for the foreign key and the list.
CREATE UNIQUE INDEX ticket_types_event_id_name_key ON ticket_types (event_id, lower(name));
