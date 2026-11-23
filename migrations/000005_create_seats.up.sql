-- A seat is one place in a seated section: row "A", seat "12". Admins create them in bulk, a row
-- at a time (M1 step 7). An event has one ticket per seat its tiers sell (migration 12); what
-- changes per event (the tier, held, killed, sold) is on those tickets and their bookings, not
-- here. Like sections, seats have no status and are part of their layout's definition.
CREATE TABLE seats (
    id              uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    section_id      uuid         NOT NULL,
    -- Always 'seated'. It exists only for the foreign key below, which references sections by
    -- (id, kind): a seat can then only belong to a seated section, checked by the database itself.
    section_kind    text         NOT NULL DEFAULT 'seated' CHECK (section_kind = 'seated'),

    -- The labels printed on the ticket. Text, since real labels look like "AA" or "12A".
    row_label       text         NOT NULL CHECK (length(btrim(row_label)) BETWEEN 1 AND 10),
    seat_label      text         NOT NULL CHECK (length(btrim(seat_label)) BETWEEN 1 AND 10),
    -- The order of rows (1 is the first row) and of seats within a row (1 is the leftmost seat
    -- as seen from the stage). Labels don't sort reliably ("AA" after "Z", "10" before "9"), so
    -- seats are listed by these, and "best available" can find seats next to each other: seats
    -- side by side have consecutive positions, and gaps are allowed (an aisle, a missing seat).
    row_position    integer      NOT NULL CHECK (row_position > 0),
    seat_position   integer      NOT NULL CHECK (seat_position > 0),

    -- standard: an ordinary seat. wheelchair: a space for a wheelchair user, often without a fixed
    -- seat. companion: next to a wheelchair space, for a companion. Shown on the seat map and used
    -- by tier rules (which tiers may sell accessible seats, M1 step 9).
    type            text         NOT NULL DEFAULT 'standard'
                                 CHECK (type IN ('standard', 'wheelchair', 'companion')),
    -- A restricted or obstructed view, with an optional note for the customer (e.g. "pillar").
    restricted_view boolean      NOT NULL DEFAULT false,
    view_note       text                  CHECK (length(btrim(view_note)) BETWEEN 1 AND 200),
    -- Next to an aisle, which some customers prefer and the seat map shows.
    aisle           boolean      NOT NULL DEFAULT false,

    version         integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at      timestamptz  NOT NULL DEFAULT now(),
    updated_at      timestamptz  NOT NULL DEFAULT now(),

    -- Deleting a section deletes its seats. Changing a section's kind to standing is refused
    -- while it has seats (NO ACTION on update).
    CONSTRAINT seats_section_id_fkey FOREIGN KEY (section_id, section_kind)
        REFERENCES sections (id, kind) ON DELETE CASCADE,
    CONSTRAINT seats_view_note_only_restricted CHECK (view_note IS NULL OR restricted_view),
    -- (id, section_id) is unique, so a ticket can reference both and the database keeps a
    -- ticket's section equal to its seat's (tickets_seat_id_fkey, migration 12).
    CONSTRAINT seats_id_section_id_key UNIQUE (id, section_id)
);

-- One seat per place: two seats of a section can't have the same row and seat position. section_id
-- comes first, so this index also finds a section's seats in order: for the list, and for the
-- foreign key when a section is deleted.
CREATE UNIQUE INDEX seats_section_id_position_key ON seats
    (section_id, row_position, seat_position);
-- Two seats of a section can't have the same labels, ignoring case: the ticket names the seat.
CREATE UNIQUE INDEX seats_section_id_label_key ON seats
    (section_id, lower(row_label), lower(seat_label));
