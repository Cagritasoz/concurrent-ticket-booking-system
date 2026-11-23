-- A ticket is one sellable unit of an event, in one tier: one seat, or one place in a standing
-- section. The tickets are the event's inventory (the layout only describes the room), and they
-- are also how tiers are given what they sell (M1 step 9), in bulk, while the event is a draft:
--   - putting a section's seats in a tier inserts one ticket per seat (INSERT ... SELECT from
--     seats); moving a seat to another tier changes its ticket's tier_id;
--   - giving a standing tier 500 places of the Floor inserts 500 tickets (generate_series), and
--     lowering that deletes available ones.
-- So there is one source of truth, before and after announcing. Tickets can't be booked before
-- then: bookings need an announced event that is on sale (M3). One row per unit means seated and
-- standing sales work the same way, and buyers of standing places lock different rows (FOR
-- UPDATE SKIP LOCKED) instead of queuing on one counter.
--
-- Two rules the database can't see without repeating the layout on every ticket, checked by the
-- service with the event locked: a ticket's section is in the event's layout, and a standing
-- section's tickets (of all tiers together) fit its capacity.
--
-- Whether a ticket is sold isn't stored here: a booking reserves tickets through its booking
-- items (M3), and a unique index there lets a ticket be in at most one active booking (I1). The
-- ticket type (Adult, Student) isn't stored either: the customer picks it when booking.
CREATE TABLE tickets (
    id          uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- No ON DELETE on any reference: tickets keep their event (through its tiers), tier, section,
    -- seat and hold from being deleted, even for a draft event, so a layout change can't remove
    -- a seat a draft is selling. Deleting a draft deletes its tickets first, explicitly. Kept for
    -- history after the event, like bookings.
    event_id    uuid         NOT NULL,
    tier_id     uuid         NOT NULL,
    -- seated: the ticket is for one seat. standing: for one place in a standing section. It is
    -- the tier's kind and the section's kind, which the foreign keys below check.
    kind        text         NOT NULL CHECK (kind IN ('seated', 'standing')),
    section_id  uuid         NOT NULL,
    -- The seat, for a seated ticket only (tickets_seat_only_seated).
    seat_id     uuid,

    -- available: can be booked (unless an active booking already has it, see above).
    -- held: kept out of public sale by an admin hold (hold_id). killed: removed from sale for
    -- good, e.g. a camera position or a seat the stage blocks. Changed by admins in bulk, with
    -- the event locked (I14), so tickets have no version of their own.
    status      text         NOT NULL DEFAULT 'available'
                             CHECK (status IN ('available', 'held', 'killed')),
    hold_id     uuid,

    created_at  timestamptz  NOT NULL DEFAULT now(),
    updated_at  timestamptz  NOT NULL DEFAULT now(),

    -- The tier: of this event and of this kind. There is no separate foreign key to events,
    -- since the tier's event_id already is one.
    CONSTRAINT tickets_tier_id_fkey FOREIGN KEY (tier_id, event_id, kind)
        REFERENCES ticket_tiers (id, event_id, kind),
    -- The section, of this kind: a standing ticket is in a standing section.
    CONSTRAINT tickets_section_id_fkey FOREIGN KEY (section_id, kind)
        REFERENCES sections (id, kind),
    -- The seat, in this section. Not checked for a standing ticket, since seat_id is NULL.
    CONSTRAINT tickets_seat_id_fkey FOREIGN KEY (seat_id, section_id)
        REFERENCES seats (id, section_id),
    -- The admin hold, of this event. Not checked when hold_id is NULL.
    CONSTRAINT tickets_hold_id_fkey FOREIGN KEY (hold_id, event_id)
        REFERENCES admin_holds (id, event_id),

    CONSTRAINT tickets_seat_only_seated CHECK ((kind = 'seated') = (seat_id IS NOT NULL)),
    CONSTRAINT tickets_hold_only_held CHECK ((status = 'held') = (hold_id IS NOT NULL))
);

-- One ticket per seat and event. seat_id comes first, so it also finds a seat's tickets for its
-- foreign key. Standing tickets have no seat and aren't affected: NULLs are never equal.
CREATE UNIQUE INDEX tickets_seat_id_event_id_key ON tickets (seat_id, event_id);
-- A tier's tickets by status: "any available ticket of this tier" for a standing booking, the
-- counts of what is left, and the tier's foreign key.
CREATE INDEX tickets_tier_id_status_idx ON tickets (tier_id, status);
-- A section's tickets in an event: the seat map of one section, and the section's foreign key.
CREATE INDEX tickets_section_id_event_id_idx ON tickets (section_id, event_id);
-- A hold's tickets: to list or release them, and for the hold's foreign key. Most tickets have no
-- hold, so only the held ones are in this index.
CREATE INDEX tickets_hold_id_idx ON tickets (hold_id) WHERE hold_id IS NOT NULL;
