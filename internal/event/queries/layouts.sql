-- name: CreateLayout :one
-- CreateLayout inserts a layout of a hall and returns it. Postgres fills in id, status (draft),
-- version (1), created_at and updated_at. An unknown hall breaks layouts_hall_id_fkey, and a name
-- another layout of the hall uses (ignoring case, archived layouts not counted) breaks
-- layouts_hall_id_name_key; the caller recognizes both by name. Run it after GetVenueForShare
-- and GetHallForShare in the same transaction, which check the venue and the hall are active and
-- keep them so until the commit.
INSERT INTO layouts (hall_id, name, description)
VALUES (@hall_id, @name, @description)
RETURNING *;

-- name: GetLayout :one
-- GetLayout returns pgx.ErrNoRows if no layout has this id. It returns archived layouts too.
SELECT * FROM layouts
WHERE id = @id;

-- name: GetLayoutForUpdate :one
-- GetLayoutForUpdate is GetLayout that also locks the row until the transaction ends, like
-- GetVenueForUpdate. Use it in postgres.WithTx, after locking the venue and the hall.
SELECT * FROM layouts
WHERE id = @id
FOR UPDATE;

-- name: GetLayoutForShare :one
-- GetLayoutForShare is GetLayout that also takes a shared lock on the row until the transaction
-- ends, like GetHallForShare. A change to one of the layout's sections reads the layout this way
-- (after the venue and the hall, the parent before the child) to check it is a draft: sections
-- of one layout can still be changed at the same time, but activating, deactivating, archiving,
-- updating or deleting the layout waits until this transaction ends. So the layout can't be
-- activated between the check and the change, e.g. while its last section is deleted. Use it in
-- postgres.WithTx.
SELECT * FROM layouts
WHERE id = @id
FOR SHARE;

-- name: ListLayouts :many
-- ListLayouts returns the layouts of a hall with this status, sorted by name ignoring case, and
-- an empty slice when there are none (or the hall id is unknown: check the hall to tell them
-- apart). There is no paging, since a hall has only a few layouts. Archived layouts can repeat a
-- name, so id breaks ties and keeps the order the same on every call. The layouts_hall_id_idx
-- index finds the hall's layouts; sorting a few rows needs no index.
SELECT * FROM layouts
WHERE hall_id = @hall_id AND status = @status
ORDER BY lower(name), id;

-- name: UpdateLayout :one
-- UpdateLayout replaces every changeable column of a draft or active layout and adds 1 to its
-- version, if the layout still has the version the change was based on (compare and set). A
-- layout stays in its hall, so hall_id can't change. pgx.ErrNoRows means no row matched: no
-- layout has this id, it is archived, or its version is different. Run it after
-- GetLayoutForUpdate in the same transaction, which tells those cases apart. A name another
-- layout of the hall uses breaks layouts_hall_id_name_key.
UPDATE layouts SET
    name = @name,
    description = @description,
    version = version + 1,
    updated_at = now()
WHERE id = @id AND version = @version AND status <> 'archived'
RETURNING *;

-- name: SetLayoutStatus :one
-- SetLayoutStatus moves a layout from one status to another (activate, archive, restore) and adds
-- 1 to its version, if the layout still has the expected version and the from status (compare
-- and set), like SetHallStatus. Which moves are allowed is the service's rule; the query only
-- refuses a row that changed meanwhile. pgx.ErrNoRows means no row matched; read the layout to
-- find out why. Restoring can break layouts_hall_id_name_key: another layout of the hall may
-- have taken the name while this one was archived.
UPDATE layouts SET
    status = @to_status,
    version = version + 1,
    updated_at = now()
WHERE id = @id AND version = @version AND status = @from_status
RETURNING *;

-- name: LayoutHasSections :one
-- LayoutHasSections reports whether the layout has at least one section: activating a layout
-- needs one, since an event is sold in its sections. sections_layout_id_name_key starts with
-- layout_id, so it finds the first section without reading the others. Run it after
-- GetLayoutForUpdate: section changes lock their layout first, so none can be added or deleted
-- between this check and the commit.
SELECT EXISTS (
    SELECT 1 FROM sections WHERE layout_id = @layout_id
);

-- name: DeleteLayout :execrows
-- DeleteLayout deletes the layout if it still has the expected version and returns the number of
-- deleted rows: 0 means no layout has this id or its version is different. Its sections and
-- their seats are deleted with it (ON DELETE CASCADE). A layout an event uses can't be deleted
-- (events_layout_id_fkey, a foreign key error); archive it instead.
DELETE FROM layouts
WHERE id = @id AND version = @version;
