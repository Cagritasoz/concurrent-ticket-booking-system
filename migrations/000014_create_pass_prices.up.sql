-- The price options of a pass: who it is for and what it costs, e.g. a 3-day pass for Adult at
-- 1,300.00 and for Student at 900.00. A pass has its own list instead of using ticket_types
-- (migration 9), because ticket types belong to one event: Friday's "Adult" and Saturday's
-- "Adult" are two rows, and a pass spans both. A booking copies the chosen option's name and
-- price (M3) and gives each ticket it reserves a share of the price, so one canceled event can
-- be refunded alone. Announcing a pass checks that it has at least one price (the service).
CREATE TABLE pass_prices (
    id           uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Deleting a pass deletes its prices.
    pass_id      uuid         NOT NULL REFERENCES passes (id) ON DELETE CASCADE,
    name         text         NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    -- Who qualifies, e.g. "With a valid student ID".
    description  text                  CHECK (length(btrim(description)) BETWEEN 1 AND 2000),
    -- Minor units of the pass's currency (passes.currency): 130000 is 1,300.00 TRY. Integers, as
    -- in tier_prices. 0 is a free pass (an invitation, usually with admin visibility).
    price_amount bigint       NOT NULL CHECK (price_amount >= 0),
    -- Who can pick the option, as for ticket types (migration 9): public, or admin only (a free
    -- invitation pass, a staff pass), never shown to customers.
    visibility   text         NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'admin')),
    version     integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at   timestamptz  NOT NULL DEFAULT now(),
    updated_at   timestamptz  NOT NULL DEFAULT now()
);

-- Two price options of one pass can't share a name, ignoring case. It also finds a pass's prices,
-- for the foreign key and the list.
CREATE UNIQUE INDEX pass_prices_pass_id_name_key ON pass_prices (pass_id, lower(name));
