-- A ticket tier is a price level of an event: where the ticket is. "Stalls", "VIP", "Floor early
-- bird". It is either seated (it sells chosen seats) or standing (it sells a number of places in
-- standing sections); the customer picks seats for the first and a quantity for the second.
--
-- What a tier sells is its tickets (migration 12): putting seats or standing places in a tier
-- creates their tickets, also while the event is a draft, so there is no second list to keep in
-- sync with them. What a tier costs depends on who the ticket is for: its prices per ticket type
-- ("Adult", "Student") are in tier_prices (migrations 9 and 10). A booking copies the price when it
-- is made (M3), so what a customer pays never changes afterwards. Enforced tier rules (sales
-- windows, presale codes) and multi-event passes come later, as new tables next to this one.
CREATE TABLE ticket_tiers (
    id                 uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Deleting a draft event deletes its tiers. A tier with tickets can't be deleted (the
    -- tickets' foreign key refuses it), so the service removes a draft's tickets first.
    event_id           uuid         NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    name               text         NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    -- What the tier includes, e.g. "Includes a drink and early entry".
    description        text                  CHECK (length(btrim(description)) BETWEEN 1 AND 2000),
    kind               text         NOT NULL CHECK (kind IN ('seated', 'standing')),
    -- At most this many tickets of the tier per customer, of all ticket types together, held and
    -- bought together (M3); no limit when NULL.
    per_customer_limit integer               CHECK (per_customer_limit > 0),

    version            integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at         timestamptz  NOT NULL DEFAULT now(),
    updated_at         timestamptz  NOT NULL DEFAULT now(),

    -- id alone is already unique; these let other tables reference more than the id, so the
    -- database keeps related rows together. (id, event_id): a tier's prices are for ticket types
    -- of the same event (tier_prices). (id, event_id, kind): a tier's tickets are in its own event
    -- and of its kind (tickets).
    CONSTRAINT ticket_tiers_id_event_id_key UNIQUE (id, event_id),
    CONSTRAINT ticket_tiers_id_event_id_kind_key UNIQUE (id, event_id, kind)
);

-- Two tiers of one event can't share a name, ignoring case: the customer picks a tier by its
-- name. It also finds an event's tiers, for the foreign key and the list.
CREATE UNIQUE INDEX ticket_tiers_event_id_name_key ON ticket_tiers (event_id, lower(name));
