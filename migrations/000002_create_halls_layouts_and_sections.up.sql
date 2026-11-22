-- The inside of a venue is a tree, each level deleted with its parent (ON DELETE CASCADE):
--
--   venue     "Zorlu PSM"                       the building: address, time zone
--    └─ hall     "Turkcell Stage"               a room; halls can host events at the same time
--        └─ layout  "Theatre seating"           one way to set up the hall; one at a time
--            └─ section "Parterre", "Floor"     seated (seats, M1 step 7) or standing (a capacity)
--
-- A venue with one room has one hall. Every event will take place in one layout of a hall
-- (M1 step 8), and events will point at layouts without CASCADE, so a venue, hall or layout
-- that an event uses can't be deleted: it is archived instead.
--
-- status and version work as in venues (migration 1): status is changed only by compare-and-set
-- queries, and version is +1 on every change and sent as the ETag. The children of an archived
-- parent count as archived without their own status being written.

-- A hall is a room of a venue: a cinema's Screen 1, a convention center's Hall A, an arena's main
-- floor.
CREATE TABLE halls (
    id          uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    venue_id    uuid         NOT NULL REFERENCES venues (id) ON DELETE CASCADE,
    name        text         NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    -- E.g. which entrance leads to it, or its floor in the building.
    description text                  CHECK (length(btrim(description)) BETWEEN 1 AND 5000),
    -- Maximum occupancy of the room, if known. Publishing an event checks its tickets against it
    -- (M1 step 10).
    capacity    integer               CHECK (capacity > 0),

    -- active: events can be created in its layouts. archived: kept for history, closed to changes
    -- and new layouts; it can be restored.
    status      text         NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    version     integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at  timestamptz  NOT NULL DEFAULT now(),
    -- Set by the update queries themselves; there is no trigger.
    updated_at  timestamptz  NOT NULL DEFAULT now()
);

-- Two halls of one venue can't share a name, ignoring case ("Screen 1" and "screen 1"), since an
-- admin picks a hall by its name. Archived halls don't count, so a name can be used again after
-- its hall is archived. Different venues can use the same names.
CREATE UNIQUE INDEX halls_venue_id_name_key ON halls (venue_id, lower(name))
    WHERE status <> 'archived';
-- Finds all of a venue's halls, archived ones too, which the partial index above can't: the
-- foreign key needs it when a venue is deleted, and so does listing them.
CREATE INDEX halls_venue_id_idx ON halls (venue_id);

-- A layout is one way a hall is set up: "Concert – standing floor", "Basketball", "Theatre
-- setup". Its capacity isn't stored: it is the sum of its sections' seats and standing
-- capacities.
CREATE TABLE layouts (
    id          uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    hall_id     uuid         NOT NULL REFERENCES halls (id) ON DELETE CASCADE,
    name        text         NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    -- E.g. which areas are open or closed in this setup.
    description text                  CHECK (length(btrim(description)) BETWEEN 1 AND 5000),

    -- draft: being built (sections and seats are added over many requests); no event can use it
    -- yet. active: events can be created in it. archived: kept for history, closed to changes; it
    -- can be restored.
    status      text         NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'archived')),
    version     integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at  timestamptz  NOT NULL DEFAULT now(),
    updated_at  timestamptz  NOT NULL DEFAULT now()
);

-- Two layouts of one hall can't share a name, ignoring case; archived layouts don't count, as for
-- halls.
CREATE UNIQUE INDEX layouts_hall_id_name_key ON layouts (hall_id, lower(name))
    WHERE status <> 'archived';
-- Finds all of a hall's layouts, for the foreign key and the lists, like halls_venue_id_idx.
CREATE INDEX layouts_hall_id_idx ON layouts (hall_id);

-- A section is a part of a layout, of one of two kinds:
--   seated:   "Block 101", "Balcony"; its seats are a separate table (M1 step 7)
--   standing: "Floor", "Pit"; general admission, no seats, only a capacity
-- Every ticket will have a place: a seat, or a standing section (M1 step 10).
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
    -- so it has no capacity of its own (last CHECK below).
    capacity    integer               CHECK (capacity > 0),

    version     integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at  timestamptz  NOT NULL DEFAULT now(),
    updated_at  timestamptz  NOT NULL DEFAULT now(),

    CONSTRAINT sections_capacity_only_standing CHECK ((kind = 'standing') = (capacity IS NOT NULL))
);

-- Two sections of one layout can't share a name, ignoring case: a ticket names its section.
-- Sections have no status, so this index covers every section and also finds a layout's
-- sections, for the foreign key and the list.
CREATE UNIQUE INDEX sections_layout_id_name_key ON sections (layout_id, lower(name));
