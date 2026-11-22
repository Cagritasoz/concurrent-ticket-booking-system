-- An event is one dated occurrence in one layout of a hall: a concert on a date, one showing of
-- a movie (30 showings are 30 events). Its hall and venue follow from the layout, so they aren't
-- stored again. Its organizer (events.organizer_id) comes with organizations in M2.
--
-- The layout is referenced, not copied: a layout a published event uses is frozen (migration 3),
-- and the event's own inventory is its tickets, one per seat or standing place, created when it
-- is published (M1 step 10). What a customer bought is copied into the booking when it is
-- confirmed (M3), so later renames don't change past tickets.
CREATE TABLE events (
    id            uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- No ON DELETE: a layout with events can't be deleted, and so neither can its hall or venue
    -- (their CASCADE stops here). Archiving is the way to retire them.
    layout_id     uuid         NOT NULL REFERENCES layouts (id),
    name          text         NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    description   text                  CHECK (length(btrim(description)) BETWEEN 1 AND 5000),

    -- Instants (timestamptz), entered and shown in the venue's time zone. The end and the time
    -- the doors open are optional, since not every event announces them.
    starts_at     timestamptz  NOT NULL,
    ends_at       timestamptz,
    doors_open_at timestamptz,

    -- draft: being prepared, tiers freely editable, no tickets yet; it can be deleted.
    -- published: on sale, its tickets exist and its layout is frozen. cancelled: was published and
    -- won't happen; its tickets are refunded (M7). "Upcoming" is published with starts_at still
    -- ahead; it isn't stored, since it changes with the clock. Changed only by compare-and-set
    -- queries (publish, cancel), like the other statuses.
    status        text         NOT NULL DEFAULT 'draft'
                               CHECK (status IN ('draft', 'published', 'cancelled')),
    published_at  timestamptz,
    cancelled_at  timestamptz,
    version       integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at    timestamptz  NOT NULL DEFAULT now(),
    updated_at    timestamptz  NOT NULL DEFAULT now(),

    CONSTRAINT events_ends_after_start CHECK (ends_at > starts_at),
    CONSTRAINT events_doors_open_before_start CHECK (doors_open_at <= starts_at),
    -- The timestamps match the status: a draft was never published, a cancelled event was
    -- published first (a draft is deleted instead of cancelled).
    CONSTRAINT events_published_at_matches_status CHECK ((published_at IS NULL) = (status = 'draft')),
    CONSTRAINT events_cancelled_at_matches_status CHECK ((cancelled_at IS NULL) = (status <> 'cancelled')),
    CONSTRAINT events_cancelled_after_published CHECK (cancelled_at >= published_at)
);

-- A layout's events by date: the foreign key needs it when a layout (or its hall or venue) is
-- deleted, and archiving one asks "any upcoming events here?" (layout_id = $1 AND starts_at >
-- now() AND status = 'published'), which reads only the future part of this index.
CREATE INDEX events_layout_id_starts_at_idx ON events (layout_id, starts_at);
-- The admin event list: one status at a time, by date, paged with a cursor (like venues).
CREATE INDEX events_status_starts_at_id_idx ON events (status, starts_at, id);
