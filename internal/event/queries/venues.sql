-- name: CreateVenue :one
-- CreateVenue inserts a venue and returns it. Postgres fills in id, status (active), version (1),
-- created_at and updated_at.
INSERT INTO venues (
    name, description,
    address_line1, address_line2, city, region, postal_code, country_code,
    latitude, longitude, timezone
) VALUES (
    @name, @description,
    @address_line1, @address_line2, @city, @region, @postal_code, @country_code,
    @latitude, @longitude, @timezone
)
RETURNING *;

-- name: GetVenue :one
-- GetVenue returns pgx.ErrNoRows if no venue has this id. It returns archived venues too.
SELECT * FROM venues
WHERE id = @id;

-- name: GetVenueForUpdate :one
-- GetVenueForUpdate is GetVenue that also locks the row until the transaction ends, so a
-- read-change-write in a transaction can't lose another one's change (like JPA's
-- PESSIMISTIC_WRITE). Outside a transaction the lock ends right away, so use it in
-- postgres.WithTx.
SELECT * FROM venues
WHERE id = @id
FOR UPDATE;

-- name: ListVenues :many
-- ListVenues returns one page of the venues with this status, sorted by name, with id deciding
-- between equal names. It is keyset pagination: pass the name and id of the last venue of the
-- previous page, or an empty name and the zero UUID for the first page. Unlike OFFSET, a page
-- costs the same however far in it is, and venues added meanwhile don't shift the pages. The
-- (status, name, id) index serves it.
SELECT * FROM venues
WHERE status = @status
  AND (name, id) > (@after_name::text, @after_id::uuid)
ORDER BY name, id
LIMIT @page_size;

-- name: UpdateVenue :one
-- UpdateVenue replaces every changeable column of an active venue and adds 1 to its version, if
-- the venue still has the version the change was based on (compare and set). pgx.ErrNoRows
-- means no row matched: no venue has this id, it is archived, or its version is different. Run
-- it after GetVenueForUpdate in the same transaction, which tells those cases apart.
UPDATE venues SET
    name = @name,
    description = @description,
    address_line1 = @address_line1,
    address_line2 = @address_line2,
    city = @city,
    region = @region,
    postal_code = @postal_code,
    country_code = @country_code,
    latitude = @latitude,
    longitude = @longitude,
    timezone = @timezone,
    version = version + 1,
    updated_at = now()
WHERE id = @id AND version = @version AND status = 'active'
RETURNING *;

-- name: SetVenueStatus :one
-- SetVenueStatus moves a venue from one status to another (archive: active → archived, restore:
-- archived → active) and adds 1 to its version, in one statement: it changes the row only if
-- the venue still has the expected version and the from status (compare and set), so two
-- admins can't both archive or restore from the same view. pgx.ErrNoRows means no row matched;
-- read the venue to find out why.
UPDATE venues SET
    status = @to_status,
    version = version + 1,
    updated_at = now()
WHERE id = @id AND version = @version AND status = @from_status
RETURNING *;

-- name: DeleteVenue :execrows
-- DeleteVenue deletes the venue if it still has the expected version and returns the number of
-- deleted rows: 0 means no venue has this id or its version is different. Its halls, their
-- layouts and sections are deleted with it (ON DELETE CASCADE). Once events exist (M1 step 8),
-- a venue they use can't be deleted (a foreign key error); archive it instead.
DELETE FROM venues
WHERE id = @id AND version = @version;
