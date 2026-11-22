-- name: CreateLayout :one
-- CreateLayout inserts a layout of a hall and returns it. Postgres fills in id, created_at and
-- updated_at. An unknown hall breaks layouts_hall_id_fkey, and a name the hall already uses
-- (ignoring case) breaks layouts_hall_id_name_key; the caller recognises both by name.
INSERT INTO layouts (hall_id, name, description)
VALUES (@hall_id, @name, @description)
RETURNING *;

-- name: GetLayout :one
-- GetLayout returns pgx.ErrNoRows if no layout has this id.
SELECT * FROM layouts
WHERE id = @id;

-- name: GetLayoutForUpdate :one
-- GetLayoutForUpdate is GetLayout that also locks the row until the transaction ends, like
-- GetVenueForUpdate. Use it in postgres.WithTx.
SELECT * FROM layouts
WHERE id = @id
FOR UPDATE;

-- name: ListLayouts :many
-- ListLayouts returns every layout of a hall, sorted by name ignoring case, and an empty slice
-- for a hall without layouts (or with an unknown id: check the hall to tell them apart). There
-- is no paging, since a hall has only a few layouts. Archived layouts can repeat a name, so id
-- breaks ties and keeps the order the same on every call. The layouts_hall_id_idx index finds
-- the hall's layouts; sorting a few rows needs no index.
SELECT * FROM layouts
WHERE hall_id = @hall_id
ORDER BY lower(name), id;

-- name: UpdateLayout :one
-- UpdateLayout replaces every changeable column and returns pgx.ErrNoRows if no layout has this
-- id. A layout stays in its hall, so hall_id can't change. A name another layout of the hall
-- uses breaks layouts_hall_id_name_key.
UPDATE layouts SET
    name = @name,
    description = @description,
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteLayout :execrows
-- DeleteLayout returns the number of deleted rows: 0 means no layout had this id. The layout's
-- sections are deleted with it (ON DELETE CASCADE).
DELETE FROM layouts
WHERE id = @id;
