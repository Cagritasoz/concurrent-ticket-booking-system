-- An admin hold keeps some of an event's tickets out of public sale for a named reason: "Artist
-- guests", "Sponsor: Turkcell", "Press". Holds are usually placed before sales open, while the
-- event is a draft, and can be added later. Its tickets point at it (tickets.hold_id, migration
-- 11) until an admin releases them to sale or books them for that party. Called admin
-- holds, so they aren't confused with the short holds a customer's booking takes (M3, M5).
--
-- Kills (tickets removed from sale for good, e.g. a camera position) are not holds: they are a
-- ticket status (migration 12) without a row here.
CREATE TABLE admin_holds (
    id          uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Deleting a (draft) event deletes its holds. A hold with tickets can't be deleted (the
    -- tickets' foreign key refuses it): its tickets are released first.
    event_id    uuid         NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    name        text         NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    -- Who the tickets are kept for, for reports and filters. other: described by the name.
    reason      text         NOT NULL CHECK (reason IN ('artist', 'promoter', 'sponsor', 'press',
                                                        'accessibility', 'other')),
    -- E.g. who to contact, or until when the hold is meant to last.
    note        text                  CHECK (length(btrim(note)) BETWEEN 1 AND 2000),

    version     integer      NOT NULL DEFAULT 1 CHECK (version > 0),

    created_at  timestamptz  NOT NULL DEFAULT now(),
    updated_at  timestamptz  NOT NULL DEFAULT now(),

    -- (id, event_id) is unique, so a ticket can reference both and the database keeps a ticket's
    -- hold in the ticket's own event.
    CONSTRAINT admin_holds_id_event_id_key UNIQUE (id, event_id)
);

-- Two holds of one event can't share a name, ignoring case. It also finds an event's holds, for
-- the foreign key and the list.
CREATE UNIQUE INDEX admin_holds_event_id_name_key ON admin_holds (event_id, lower(name));
