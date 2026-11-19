-- The admin venue list is sorted by name and paged with a cursor (ListVenues). This index lets
-- Postgres read one page straight from the index instead of sorting the whole table. id makes
-- the order unique when two venues have the same name.
CREATE INDEX venues_name_id_idx ON venues (name, id);
