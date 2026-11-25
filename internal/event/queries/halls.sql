-- name: CreateHall :one
-- CreateHall inserts a hall of a venue and returns it. Postgres fills in id, status (active),
-- version (1), created_at and updated_at. An unknown venue breaks halls_venue_id_fkey, and a name
-- another hall of the venue uses (ignoring case, archived halls not counted) breaks
-- halls_venue_id_name_key; the caller recognizes both by name. Run it after GetVenueForShare in
-- the same transaction, which checks the venue is active and keeps it so until the commit.
INSERT INTO halls (venue_id, name, description, capacity)
VALUES (@venue_id, @name, @description, @capacity)
RETURNING *;

-- name: GetHall :one
-- GetHall returns pgx.ErrNoRows if no hall has this id. It returns archived halls too.
SELECT * FROM halls
WHERE id = @id;

-- name: GetHallForUpdate :one
-- GetHallForUpdate is GetHall that also locks the row until the transaction ends, like
-- GetVenueForUpdate. Use it in postgres.WithTx.
SELECT * FROM halls
WHERE id = @id
FOR UPDATE;

-- name: GetHallForShare :one
-- GetHallForShare is GetHall that also takes a shared lock on the row until the transaction
-- ends, like GetVenueForShare. A change to one of the hall's layouts reads the hall this way
-- (after GetVenueForShare, the parent before the child) to check it is active: layouts of one
-- hall can still be changed at the same time, but archiving, updating or deleting the hall waits
-- until this transaction ends. Use it in postgres.WithTx.
SELECT * FROM halls
WHERE id = @id
FOR SHARE;

-- name: ListHalls :many
-- ListHalls returns the halls of a venue with this status, sorted by name ignoring case, and an
-- empty slice when there are none (or the venue id is unknown: check the venue to tell them
-- apart). There is no paging, since a venue has only a few halls. Archived halls can repeat a
-- name, so id breaks ties and keeps the order the same on every call. The halls_venue_id_idx
-- index finds the venue's halls; sorting a few rows needs no index.
SELECT * FROM halls
WHERE venue_id = @venue_id AND status = @status
ORDER BY lower(name), id;

-- name: UpdateHall :one
-- UpdateHall replaces every changeable column of an active hall and adds 1 to its version, if the
-- hall still has the version the change was based on (compare and set). A hall stays in its
-- venue, so venue_id can't change. pgx.ErrNoRows means no row matched: no hall has this id, it
-- is archived, or its version is different. Run it after GetHallForUpdate in the same
-- transaction, which tells those cases apart. A name another hall of the venue uses breaks
-- halls_venue_id_name_key.
UPDATE halls SET
    name = @name,
    description = @description,
    capacity = @capacity,
    version = version + 1,
    updated_at = now()
WHERE id = @id AND version = @version AND status = 'active'
RETURNING *;

-- name: SetHallStatus :one
-- SetHallStatus moves a hall from one status to another (archive: active → archived, restore:
-- archived → active) and adds 1 to its version, if the hall still has the expected version and
-- the from status (compare and set), like SetVenueStatus. pgx.ErrNoRows means no row matched;
-- read the hall to find out why. Restoring can break halls_venue_id_name_key: an active hall
-- may have taken the name while this one was archived.
UPDATE halls SET
    status = @to_status,
    version = version + 1,
    updated_at = now()
WHERE id = @id AND version = @version AND status = @from_status
RETURNING *;

-- name: DeleteHall :execrows
-- DeleteHall deletes the hall if it still has the expected version and returns the number of
-- deleted rows: 0 means no hall has this id or its version is different. Its layouts, their
-- sections and seats are deleted with it (ON DELETE CASCADE). A hall an event uses can't be
-- deleted (events_layout_id_fkey, a foreign key error); archive it instead.
DELETE FROM halls
WHERE id = @id AND version = @version;
