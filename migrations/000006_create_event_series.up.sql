-- An event series groups events that are sold and shown as one production: a film's showings, a
-- theater run, a team's home matches in a season, the days of a festival. Each event of a series
-- is still a full event of its own (migration 7) with its own date, layout, tiers and prices,
-- since a matinee is often cheaper than an evening and a derby dearer than other matches; the
-- series only groups them. Performing-arts systems work the same way (a production, its season
-- and its performances, each performance with its own price map). Setting up a new showing like
-- an earlier one is a copy done by the service, not a shared row.
--
-- An event needs no series: a one-off concert has none. A series can span venues (a tour), and
-- its events can be in different currencies; whatever needs them to match checks it then.
-- Multi-event passes (a festival pass, a season ticket) will belong to a series and grant one
-- ticket in each of the events they cover (planned, see docs/open/12-domain-model.md). Its owner
-- (the organizer) comes with organizations in M2.
--
-- No status: a series is visible through its published events, and a series with events can't be
-- deleted (events_series_id_fkey, migration 7), so there is nothing to archive. version works as
-- in venues (migration 1).
CREATE TABLE event_series (
    id          uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Not unique: the same play can have a run every year.
    name        text         NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    description text                  CHECK (length(btrim(description)) BETWEEN 1 AND 5000),
    version     integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at  timestamptz  NOT NULL DEFAULT now(),
    updated_at  timestamptz  NOT NULL DEFAULT now()
);
