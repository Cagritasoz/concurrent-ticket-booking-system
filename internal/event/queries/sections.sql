-- name: CreateSection :one
-- CreateSection inserts a section of a layout and returns it. Postgres fills in id, version (1),
-- created_at and updated_at. An unknown layout breaks sections_layout_id_fkey, a name another
-- section of the layout uses (ignoring case) breaks sections_layout_id_name_key, and a capacity
-- that doesn't match the kind (standing needs one, seated has none) breaks
-- sections_capacity_only_standing; the caller recognizes them by name. Run it after locking the
-- venue, the hall and the layout (FOR SHARE) in the same transaction, which check the layout is
-- a draft and keep it so until the commit.
INSERT INTO sections (layout_id, name, kind, capacity, position)
VALUES (@layout_id, @name, @kind, @capacity, @position)
RETURNING *;

-- name: NextSectionPosition :one
-- NextSectionPosition returns the position a new section of the layout gets: one after the
-- highest, or 1 for the first. Positions aren't unique, so two creates at the same moment may
-- both get the same number, which is harmless: sections with one position are listed by name.
-- The sections_layout_id_name_key index finds the layout's sections.
SELECT (coalesce(max(position), 0) + 1)::integer AS next_position
FROM sections
WHERE layout_id = @layout_id;

-- name: GetSection :one
-- GetSection returns pgx.ErrNoRows if no section has this id.
SELECT * FROM sections
WHERE id = @id;

-- name: GetSectionForUpdate :one
-- GetSectionForUpdate is GetSection that also locks the row until the transaction ends, like
-- GetVenueForUpdate. Use it in postgres.WithTx, after locking the venue, the hall and the layout.
SELECT * FROM sections
WHERE id = @id
FOR UPDATE;

-- name: ListSections :many
-- ListSections returns every section of a layout in the layout's order: by position, then by
-- name ignoring case for equal positions, and an empty slice when there are none (or the layout
-- id is unknown: check the layout to tell them apart). Names are unique per layout ignoring case,
-- so the order is the same on every call. There is no paging, since a layout has at most a few
-- hundred sections; the sections_layout_id_name_key index finds them, and sorting needs no index.
SELECT * FROM sections
WHERE layout_id = @layout_id
ORDER BY position, lower(name);

-- name: UpdateSection :one
-- UpdateSection replaces every changeable column of a section and adds 1 to its version, if the
-- section still has the version the change was based on (compare and set). A section stays in
-- its layout and keeps its kind, so layout_id and kind can't change. pgx.ErrNoRows means no row
-- matched: no section has this id, or its version is different. Run it after
-- GetSectionForUpdate in the same transaction, which tells those apart and checks the layout is a
-- draft. A name another section of the layout uses breaks sections_layout_id_name_key, and a
-- capacity that doesn't match the kind breaks sections_capacity_only_standing.
UPDATE sections SET
    name = @name,
    capacity = @capacity,
    position = @position,
    version = version + 1,
    updated_at = now()
WHERE id = @id AND version = @version
RETURNING *;

-- name: DeleteSection :execrows
-- DeleteSection deletes the section if it still has the expected version and returns the number
-- of deleted rows: 0 means no section has this id or its version is different. Its seats are
-- deleted with it (ON DELETE CASCADE). A section a ticket uses can't be deleted (tickets have no
-- ON DELETE, a foreign key error). Unlike deleting a hall or a layout, this changes a layout's
-- definition, so run it after locking the venue, the hall and the layout (FOR SHARE), which
-- check the layout is a draft.
DELETE FROM sections
WHERE id = @id AND version = @version;
