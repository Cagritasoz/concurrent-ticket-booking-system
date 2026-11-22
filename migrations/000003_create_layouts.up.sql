-- A layout is one way a hall is set up: "Concert – standing floor", "Basketball", "Theatre
-- setup". A hall is in one layout at a time; every event takes place in one layout (migration 6).
-- Its capacity isn't stored: it is the sum of its sections' seats and standing capacities.
--
-- A layout that a published event uses is frozen: its sections and seats can't change, so the
-- event's tickets keep describing the room as it was sold. "Frozen" is worked out from the
-- events, not stored. To change a frozen layout, an admin copies it (M1 step 11).
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
