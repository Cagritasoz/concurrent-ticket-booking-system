-- A section is a part of a layout, of one of two kinds:
--   seated:   "Block 101", "Balcony"; its seats are rows of the seats table (migration 5)
--   standing: "Floor", "Pit"; general admission, no seats, only a capacity
-- Every ticket has a place: a seat, or a standing section (migration 12). Whether an event is
-- seated, standing or mixed isn't stored anywhere: it follows from its layout's sections.
--
-- A section has no status: it is part of its layout's definition and is changed while the layout
-- is a draft. It has its own version, so changing a section doesn't change its layout's.
CREATE TABLE sections (
    id          uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    layout_id   uuid         NOT NULL REFERENCES layouts (id) ON DELETE CASCADE,
    name        text         NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    -- text with a CHECK, not a Postgres enum type: adding a kind later is a one-line change of
    -- the CHECK, and the Go code compares it with its own constants anyway.
    kind        text         NOT NULL CHECK (kind IN ('seated', 'standing')),
    -- How many people a standing section holds. A seated section holds as many as it has seats,
    -- so it has no capacity of its own (sections_capacity_only_standing below).
    capacity    integer               CHECK (capacity > 0),
    -- Where the section comes in its layout's list (1 first): "Table 2" before "Table 10", and
    -- the stalls before the balcony, which sorting by name gets wrong. The service gives a new
    -- section the next number. Not unique, so sections can be reordered one at a time without a
    -- clash in between; sections with the same position are listed by name.
    position    integer      NOT NULL CHECK (position > 0),

    version    integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at  timestamptz  NOT NULL DEFAULT now(),
    updated_at  timestamptz  NOT NULL DEFAULT now(),

    CONSTRAINT sections_capacity_only_standing CHECK ((kind = 'standing') = (capacity IS NOT NULL)),
    -- id alone is already unique; (id, kind) is unique too, so seats can reference both and the
    -- database itself keeps seats out of standing sections (see seats_section_id_fkey, migration
    -- 5). A seated section that has seats then can't become standing either.
    CONSTRAINT sections_id_kind_key UNIQUE (id, kind)
);

-- Two sections of one layout can't share a name, ignoring case: a ticket names its section.
-- Sections have no status, so this index covers every section and also finds a layout's
-- sections, for the foreign key and the list (a layout has few sections, so sorting them by
-- position needs no index of its own).
CREATE UNIQUE INDEX sections_layout_id_name_key ON sections (layout_id, lower(name));
