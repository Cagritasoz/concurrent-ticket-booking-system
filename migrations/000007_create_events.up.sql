-- An event is one dated occurrence in one layout of a hall: a concert on a date, one showing of
-- a movie (30 showings are 30 events, grouped by an event series, migration 6). Its venue follows
-- from the hall, so it isn't stored again; venue_id is added if queries need it. Its organizer
-- (events.organizer_id) comes with organizations in M2.
--
-- The layout is referenced, not copied: a layout a confirmed event uses is frozen (migration 3),
-- and the event's own inventory is its tickets, one per seat or standing place, created as its
-- tiers are given seats and places (M1 step 9, migration 12); a draft's layout can only change
-- while it has no tickets, which the service checks. What a customer bought is copied into the
-- booking when it is confirmed (M3), so later renames don't change past tickets.

-- btree_gist lets one GiST index compare plain values with = (the hall) next to ranges with &&
-- (the times), which the no-overlap rule below needs. It ships with Postgres and is a trusted
-- extension, so the database's owner can enable it without being a superuser.
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE events (
    id                  uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The layout and its hall. hall_id repeats what the layout says, so that the no-overlap rule
    -- can compare events of the same hall; events_layout_id_fkey below keeps the two in line.
    layout_id           uuid         NOT NULL,
    hall_id             uuid         NOT NULL,
    -- The series it belongs to, if any: a film's showings, a season's home matches. NULL for a
    -- one-off event.
    series_id           uuid,
    name                text         NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    description         text                  CHECK (length(btrim(description)) BETWEEN 1 AND 5000),

    -- Instants (timestamptz), entered and shown in the venue's time zone. starts_at and ends_at
    -- are the show, as customers see it. The time the doors open is optional, since not every
    -- event announces it.
    starts_at           timestamptz  NOT NULL,
    ends_at             timestamptz  NOT NULL,
    doors_open_at       timestamptz,
    -- How long the event needs the hall, when that is longer than the show: setting up before
    -- (load-in, sound check) and clearing after (load-out). NULL means no extra time on that
    -- side: from starts_at, or until ends_at. The no-overlap rule below uses these, so the next
    -- event's setup can't start while this one is still being cleared, and customers still see
    -- the show's own times.
    hall_reserved_from  timestamptz,
    hall_reserved_until timestamptz,

    -- When single-ticket sales open and close. Real platforms announce an event first and open
    -- sales at a set time, so customers see it, and its seat map, before they can buy. NULL
    -- on_sale_at means on sale as soon as it is announced; NULL off_sale_at means sales close
    -- when it starts. off_sale_at can be earlier (online sales often close an hour or two before)
    -- or later, up to the end (a cinema selling late entry). Passes are sold by their own times
    -- (migration 13), so they can go on sale before the day tickets. Bookings check them (M3);
    -- the times can still move while they are ahead.
    on_sale_at          timestamptz,
    off_sale_at         timestamptz,

    -- ISO 4217 code, e.g. TRY or EUR. Every price of the event (its ticket tiers, migration 8) is
    -- in this currency, as integer minor units; one payment can only be in one currency.
    currency            text         NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),

    -- At most this many single tickets of the event per customer, of all tiers together ("6 per
    -- customer"), counted over the customer's bookings (M3); no limit when NULL. A tier's own
    -- limit (migration 8) can be lower for that tier: a booking must keep within both. Not
    -- counted here: tickets that come with a pass (passes have their own limit, migration 13),
    -- and tickets of tiers sold as whole sections, whose limit counts tables, not seats.
    per_customer_limit  integer               CHECK (per_customer_limit > 0),

    -- The lifecycle. The organizer prepares the event, the venue confirms the date and the
    -- setup, and the organizer then decides when to announce it:
    --   draft:     being prepared; private to its organizer; doesn't take the hall's time slot.
    --   submitted: sent to the venue, which is asked for this date and layout; takes the slot.
    --   confirmed: the venue agreed; the layout is now frozen (migration 3). The organizer
    --              finishes tiers, prices and holds, which the venue doesn't approve.
    --   announced: public: customers see the event, its seat map and prices; single tickets are
    --              sold between on_sale_at and off_sale_at.
    --   cancelled: was announced and won't happen; its tickets are refunded (M7).
    -- A declined or withdrawn event goes back to draft (with decline_reason when declined) and
    -- frees its slot; one that isn't announced yet can also be deleted, since nothing is sold
    -- before announcing. Changing the date, hall or layout of a confirmed event sends it back to
    -- submitted. Changing the times of an announced event is a reschedule (rescheduled_at): it
    -- stays announced and its tickets stay valid. "On sale", "sold out" and "past" aren't stored,
    -- since they follow from the times and the tickets. From M2 a venue's own events (a cinema's
    -- showings) are confirmed as soon as they are submitted. Changed only by compare-and-set
    -- queries (submit, confirm, decline, withdraw, announce, cancel), like the other statuses.
    status              text         NOT NULL DEFAULT 'draft'
                                     CHECK (status IN ('draft', 'submitted', 'confirmed',
                                                       'announced', 'cancelled')),
    submitted_at        timestamptz,
    confirmed_at        timestamptz,
    announced_at        timestamptz,
    cancelled_at        timestamptz,
    -- Why the venue declined, for the organizer, e.g. "The hall is booked for maintenance".
    -- Cleared when the event is submitted again.
    decline_reason      text                  CHECK (length(btrim(decline_reason)) BETWEEN 1 AND 2000),
    -- When the times of the announced event last changed. Tickets stay valid for the new date;
    -- whether customers may ask for a refund instead is M7's refund policy.
    rescheduled_at      timestamptz,
    version             integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at          timestamptz  NOT NULL DEFAULT now(),
    updated_at          timestamptz  NOT NULL DEFAULT now(),

    -- No ON DELETE: a layout with events can't be deleted, and so neither can its hall or venue
    -- (their CASCADE stops here). Archiving is the way to retire them. Referencing (id, hall_id)
    -- also means hall_id can only be the layout's own hall.
    CONSTRAINT events_layout_id_fkey FOREIGN KEY (layout_id, hall_id)
        REFERENCES layouts (id, hall_id),
    -- No ON DELETE either: a series with events can't be deleted; its events are taken out of it
    -- (series_id set to NULL) or deleted first.
    CONSTRAINT events_series_id_fkey FOREIGN KEY (series_id) REFERENCES event_series (id),
    -- id alone is already unique; this lets a pass's events (pass_events, migration 15) reference
    -- the event with its series and currency, so a pass only covers events of its own series and
    -- currency, and such an event can't leave the series or change currency while a pass covers it.
    CONSTRAINT events_id_series_id_currency_key UNIQUE (id, series_id, currency),

    CONSTRAINT events_ends_after_start CHECK (ends_at > starts_at),
    CONSTRAINT events_doors_open_before_start CHECK (doors_open_at <= starts_at),
    -- The hall is reserved for at least the show, from before the doors open (coalesce takes the
    -- first value that isn't NULL: the doors, or the start when no door time is set).
    CONSTRAINT events_hall_reserved_from_before_doors CHECK (
        hall_reserved_from <= coalesce(doors_open_at, starts_at)),
    CONSTRAINT events_hall_reserved_until_after_end CHECK (hall_reserved_until >= ends_at),
    -- Sales open before the event starts and close after they open, at the end at the latest;
    -- selling at the door isn't modeled.
    CONSTRAINT events_on_sale_before_start CHECK (on_sale_at < starts_at),
    CONSTRAINT events_off_sale_after_on_sale CHECK (off_sale_at > on_sale_at),
    CONSTRAINT events_off_sale_by_end CHECK (off_sale_at <= ends_at),

    -- The timestamps match the status. Each step sets its time and keeps the earlier ones; going
    -- back to draft or submitted clears the later ones. A cancelled event was announced first.
    CONSTRAINT events_submitted_at_matches_status CHECK (
        (submitted_at IS NULL) = (status = 'draft')),
    CONSTRAINT events_confirmed_at_matches_status CHECK (
        (confirmed_at IS NULL) = (status IN ('draft', 'submitted'))),
    CONSTRAINT events_announced_at_matches_status CHECK (
        (announced_at IS NULL) = (status IN ('draft', 'submitted', 'confirmed'))),
    CONSTRAINT events_cancelled_at_matches_status CHECK (
        (cancelled_at IS NULL) = (status <> 'cancelled')),
    CONSTRAINT events_confirmed_after_submitted CHECK (confirmed_at >= submitted_at),
    CONSTRAINT events_announced_after_confirmed CHECK (announced_at >= confirmed_at),
    CONSTRAINT events_cancelled_after_announced CHECK (cancelled_at >= announced_at),
    -- Only a draft has a decline reason, and only an announced (or since cancelled) event can be
    -- rescheduled. The IS NOT NULL is needed: a comparison with NULL would let the CHECK pass.
    CONSTRAINT events_decline_reason_only_draft CHECK (decline_reason IS NULL OR status = 'draft'),
    CONSTRAINT events_rescheduled_after_announced CHECK (
        rescheduled_at IS NULL OR (announced_at IS NOT NULL AND rescheduled_at >= announced_at)),

    -- A hall is set up one way at a time, so two events in one hall can't need it at the same
    -- time: an insert or update that would is refused (SQLSTATE 23P01).
    -- An exclusion constraint tells Postgres: "For any two rows, the specified comparisons must not all evaluate to true simultaneously."
    -- Here: "For any given hall, two events that hold a slot must not have overlapping time intervals."
    -- && is the overlap operator, returns true if there is an overlap false otherwise.
    -- The time is the hall reservation, or the show where there is none, as a range
    -- [from, until), so one event can start the moment the previous one is cleared. Only
    -- submitted, confirmed and announced events hold a slot (the WHERE: rows outside it aren't
    -- checked at all), so drafts can share a date, and the slot is claimed by the submit itself:
    -- of two organizers submitting the same date, the second update is refused, however close
    -- together they come. Declining, withdrawing or cancelling frees it. The constraint's own
    -- GiST index also finds a hall's events by time.
    CONSTRAINT events_no_overlap_in_hall EXCLUDE USING gist (
        hall_id WITH =,
        tstzrange(coalesce(hall_reserved_from, starts_at), coalesce(hall_reserved_until, ends_at)) WITH &&
    ) WHERE (status IN ('submitted', 'confirmed', 'announced'))
);

-- A layout's events by date: the foreign key needs it when a layout (or its hall or venue) is
-- deleted, and archiving one asks "any upcoming events here?" (layout_id = $1 AND starts_at >
-- now() AND status IN ('submitted', 'confirmed', 'announced')), which reads only the future part
-- of this index.
CREATE INDEX events_layout_id_starts_at_idx ON events (layout_id, starts_at);
-- The admin event list: one status at a time, by date, paged with a cursor (like venues).
CREATE INDEX events_status_starts_at_id_idx ON events (status, starts_at, id);
-- A series' events by date ("all showings"), and the series' foreign key when one is deleted.
-- Most events may have no series, so only those with one are in this index; a query for
-- series_id = $1 can still use it, since a NULL is never equal to anything.
CREATE INDEX events_series_id_starts_at_idx ON events (series_id, starts_at)
    WHERE series_id IS NOT NULL;
