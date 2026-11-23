-- An event is one dated occurrence in one layout of a hall: a concert on a date, one showing of
-- a movie (30 showings are 30 events, grouped by an event series, migration 6). Its venue follows
-- from the hall, so it isn't stored again; venue_id is added if queries need it. Its organizer
-- (events.organizer_id) comes with organizations in M2.
--
-- The layout is referenced, not copied: a layout a published event uses is frozen (migration 3),
-- and the event's own inventory is its tickets, one per seat or standing place, created as its
-- tiers are given seats and places (M1 step 9, migration 12); a draft's layout can only change
-- while it has no tickets, which the service checks. What a customer bought is copied into the booking when it is
-- confirmed (M3), so later renames don't change past tickets.

-- btree_gist lets one GiST index compare plain values with = (the hall) next to ranges with &&
-- (the times), which the no-overlap rule below needs. It ships with Postgres and is a trusted
-- extension, so the database's owner can enable it without being a superuser.
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE events (
    id            uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The layout and its hall. hall_id repeats what the layout says, so that the no-overlap rule
    -- can compare events of the same hall; events_layout_id_fkey below keeps the two in line.
    layout_id     uuid         NOT NULL,
    hall_id       uuid         NOT NULL,
    -- The series it belongs to, if any: a film's showings, a season's home matches. NULL for a
    -- one-off event.
    series_id     uuid,
    name         text         NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    description   text                  CHECK (length(btrim(description)) BETWEEN 1 AND 5000),

    -- Instants (timestamptz), entered and shown in the venue's time zone. The end is required: it
    -- is when the hall is free again, which the no-overlap rule needs. The time the doors open is
    -- optional, since not every event announces it.
    starts_at     timestamptz  NOT NULL,
    ends_at       timestamptz  NOT NULL,
    doors_open_at timestamptz,
    -- When sales open. Real platforms announce an event first and open sales at a set time, so
    -- customers see it, and its seat map, before they can buy. NULL means on sale as soon as it
    -- is published. Bookings check it (M3); the time can still move while it is ahead.
    on_sale_at    timestamptz,

    -- ISO 4217 code, e.g. TRY or EUR. Every price of the event (its ticket tiers, migration 8) is
    -- in this currency, as integer minor units; one payment can only be in one currency.
    currency      text         NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),

    -- draft: being prepared; its tiers, prices and tickets are freely editable, nobody can book,
    -- and it can be deleted. published: visible to customers, its layout is frozen; on sale from
    -- on_sale_at, or right away without one. canceled: was published and won't happen; its
    -- tickets are refunded (M7). "Upcoming" is published with starts_at still
    -- ahead; it isn't stored, since it changes with the clock. Changed only by compare-and-set
    -- queries (publish, cancel), like the other statuses.
    status        text         NOT NULL DEFAULT 'draft'
                               CHECK (status IN ('draft', 'published', 'cancelled')),
    published_at  timestamptz,
    cancelled_at  timestamptz,
    version       integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at    timestamptz  NOT NULL DEFAULT now(),
    updated_at    timestamptz  NOT NULL DEFAULT now(),

    -- No ON DELETE: a layout with events can't be deleted, and so neither can its hall or venue
    -- (their CASCADE stops here). Archiving is the way to retire them. Referencing (id, hall_id)
    -- also means hall_id can only be the layout's own hall.
    CONSTRAINT events_layout_id_fkey FOREIGN KEY (layout_id, hall_id)
        REFERENCES layouts (id, hall_id),
    -- No ON DELETE either: a series with events can't be deleted; its events are taken out of it
    -- (series_id set to NULL) or deleted first.
    CONSTRAINT events_series_id_fkey FOREIGN KEY (series_id) REFERENCES event_series (id),

    CONSTRAINT events_ends_after_start CHECK (ends_at > starts_at),
    CONSTRAINT events_doors_open_before_start CHECK (doors_open_at <= starts_at),
    -- Sales open before the event starts; selling at the door isn't modeled.
    CONSTRAINT events_on_sale_before_start CHECK (on_sale_at < starts_at),
    -- The timestamps match the status: a draft was never published, a canceled event was
    -- published first (a draft is deleted instead of canceled).
    CONSTRAINT events_published_at_matches_status CHECK ((published_at IS NULL) = (status = 'draft')),
    CONSTRAINT events_cancelled_at_matches_status CHECK ((cancelled_at IS NULL) = (status <> 'cancelled')),
    CONSTRAINT events_cancelled_after_published CHECK (cancelled_at >= published_at),

    -- A hall is set up one way at a time, so two events in one hall can't overlap in time: an
    -- insert or update that would is refused (SQLSTATE 23P01). The range is [start, end), so one
    -- event can start the moment the previous one ends. Drafts count too, so a draft keeps its
    -- time slot and publishing can't fail on a clash found late; a canceled event frees its slot.
    -- The constraint's own GiST index also finds a hall's events by time.
    -- An exclusion constraint tells Postgres: "For any two rows, the specified comparisons must not all evaluate to true simultaneously."
    -- "For any given hall, two non-canceled events must not have overlapping time intervals."
    -- So for events that share the same hall_id their times should not overlap if they are not canceled.
    -- && is the overlap operator, returns true if there is an overlap false otherwise
    CONSTRAINT events_no_overlap_in_hall EXCLUDE USING gist (
        hall_id WITH =,
        tstzrange(starts_at, ends_at) WITH &&
    ) WHERE (status <> 'cancelled')
);

-- A layout's events by date: the foreign key needs it when a layout (or its hall or venue) is
-- deleted, and archiving one asks "any upcoming events here?" (layout_id = $1 AND starts_at >
-- now() AND status = 'published'), which reads only the future part of this index.
CREATE INDEX events_layout_id_starts_at_idx ON events (layout_id, starts_at);
-- The admin event list: one status at a time, by date, paged with a cursor (like venues).
CREATE INDEX events_status_starts_at_id_idx ON events (status, starts_at, id);
-- A series' events by date ("all showings"), and the series' foreign key when one is deleted.
-- Most events may have no series, so only those with one are in this index; a query for
-- series_id = $1 can still use it, since a NULL is never equal to anything.
CREATE INDEX events_series_id_starts_at_idx ON events (series_id, starts_at)
    WHERE series_id IS NOT NULL;
