-- The events a pass covers, and for each one the tier its ticket comes from there. A 3-day pass
-- has three rows: (Friday, Friday's "3-day pass" tier), (Saturday, ...), (Sunday, ...). Booking
-- the pass reserves one available ticket of each row's tier (M3).
--
-- The tier decides which places the pass gets, and whether they are shared (its sold_via,
-- migration 8):
--   - a pass_only tier in each event ("3-day pass", 10,000 places) is a quota that single
--     tickets can't take, what most festivals do; it has no tier_prices of its own, since it is
--     sold only through passes, so announcing doesn't require a price for it;
--   - a shared tier ("Day ticket") is a pool that single tickets and passes compete for.
-- A single tier can't be a pass's tier. Two passes may take tickets from the same tier. A "create pass" action in the service builds
-- the quota tiers, their tickets and these rows in one go, so an admin doesn't link them by hand.
CREATE TABLE pass_events (
    pass_id       uuid         NOT NULL,
    event_id      uuid         NOT NULL,
    tier_id       uuid         NOT NULL,
    -- Copies of the pass's series, currency and kind. The foreign keys below compare them with
    -- the pass, the event and the tier, so the database keeps them all in line: the event is in
    -- the pass's series and currency, and the tier is in that event and of the pass's kind.
    series_id     uuid         NOT NULL,
    currency      text         NOT NULL,
    kind          text         NOT NULL,
    -- A copy of the tier's sold_via, compared with the tier by pass_events_tier_id_fkey: only a
    -- tier sold through passes can be a pass's tier. When the tier switches between pass_only and
    -- shared the copy follows (ON UPDATE CASCADE); switching it to single is refused by this
    -- check while a pass uses it.
    tier_sold_via text         NOT NULL CHECK (tier_sold_via IN ('pass_only', 'shared')),

    -- Rows are changed one at a time (another tier for one event), so they have a version (I15).
    version       integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at    timestamptz  NOT NULL DEFAULT now(),
    updated_at    timestamptz  NOT NULL DEFAULT now(),

    -- One tier per event and pass. pass_id comes first, so it also finds a pass's events, for the
    -- list and for the pass's foreign key.
    PRIMARY KEY (pass_id, event_id),
    -- Deleting a pass deletes these rows. Referencing (id, series_id, currency, kind) also means a
    -- pass's series, currency and kind can't change while it covers events.
    CONSTRAINT pass_events_pass_id_fkey FOREIGN KEY (pass_id, series_id, currency, kind)
        REFERENCES passes (id, series_id, currency, kind) ON DELETE CASCADE,
    -- No ON DELETE: an event a pass covers can't be deleted, and it can't leave the series or
    -- change its currency (events_id_series_id_currency_key), so a pass never silently loses a
    -- day. The service removes the event from its passes first.
    CONSTRAINT pass_events_event_id_fkey FOREIGN KEY (event_id, series_id, currency)
        REFERENCES events (id, series_id, currency),
    -- No ON DELETE either: a tier a pass takes tickets from can't be deleted. ON UPDATE CASCADE
    -- only matters for sold_via, the one referenced column that changes.
    CONSTRAINT pass_events_tier_id_fkey FOREIGN KEY (tier_id, event_id, kind, tier_sold_via)
        REFERENCES ticket_tiers (id, event_id, kind, sold_via) ON UPDATE CASCADE
);

-- An event's passes (which passes cover this day?) and the event's foreign key.
CREATE INDEX pass_events_event_id_idx ON pass_events (event_id);
-- A tier's passes (is this tier a pass's quota?) and the tier's foreign key.
CREATE INDEX pass_events_tier_id_idx ON pass_events (tier_id);
