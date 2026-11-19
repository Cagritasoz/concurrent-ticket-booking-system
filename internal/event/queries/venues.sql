-- name: CreateVenue :one
-- CreateVenue inserts a venue and returns it. Postgres fills in id, created_at and updated_at.
INSERT INTO venues (
    name, description,
    address_line1, address_line2, city, region, postal_code, country_code,
    latitude, longitude, timezone, capacity
) VALUES (
    @name, @description,
    @address_line1, @address_line2, @city, @region, @postal_code, @country_code,
    @latitude, @longitude, @timezone, @capacity
)
RETURNING *;

-- name: GetVenue :one
-- GetVenue returns pgx.ErrNoRows if no venue has this id.
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
-- ListVenues returns one page of venues sorted by name, with id deciding between equal names.
-- It is keyset pagination: pass the name and id of the last venue of the previous page, or an
-- empty name and the zero UUID for the first page. Unlike OFFSET, a page costs the same however
-- far in it is, and venues added meanwhile don't shift the pages. The (name, id) index serves it.
SELECT * FROM venues
WHERE (name, id) > (@after_name::text, @after_id::uuid)
ORDER BY name, id
LIMIT @page_size;

-- name: UpdateVenue :one
-- UpdateVenue replaces every changeable column and returns pgx.ErrNoRows if no venue has this id.
-- To change only some, read the venue with GetVenueForUpdate in the same transaction, change
-- those fields and write it all back.
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
    capacity = @capacity,
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteVenue :execrows
-- DeleteVenue returns the number of deleted rows: 0 means no venue had this id.
DELETE FROM venues
WHERE id = @id;
