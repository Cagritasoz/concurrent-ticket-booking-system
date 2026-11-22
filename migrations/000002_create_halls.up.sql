-- The inside of a venue is a tree, one table per migration, each level deleted with its parent
-- (ON DELETE CASCADE):
--
--   venue        "Zorlu PSM"                    the building: address, time zone      (1)
--    └─ hall        "Turkcell Stage"            a room; halls host events at once     (2)
--        └─ layout     "Theater setup"          one way to set up the hall            (3)
--            └─ section   "Parterre", "Floor"   seated, or standing with a capacity   (4)
--                └─ seat     row "A", seat "12" only in seated sections               (5)
--
-- Events (6) point at a layout without CASCADE, so a venue, hall or layout that an event uses
-- can't be deleted: it is archived instead.
--
-- status and version work as in venues (migration 1): status is changed only by compare-and-set
-- queries, and version is +1 on every change and sent as the ETag. The children of an archived
-- parent count as archived without their own status being written.

-- A hall is a room of a venue: a cinema's Screen 1, a convention center's Hall A, an arena's main
-- floor. A venue with one room has one hall.
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
