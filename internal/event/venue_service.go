package event

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event/eventdb"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres"
)

// VenueService holds the rules for venues, like a Spring @Service: handlers call it, and it
// calls the generated queries. It has no Gin and no HTTP in it, so it can be used and tested
// without a web server.
//
// There is no repository layer in between: eventdb.Queries already is one, generated from SQL.
// The service is safe for concurrent use; one instance serves every request.
type VenueService struct {
	pool    *pgxpool.Pool    // for transactions (postgres.WithTx)
	queries *eventdb.Queries // for single queries, each in its own transaction
}

// NewVenueService creates the service on a connection pool. cmd/ticketing calls it once at
// startup and hands the service to whoever needs it (constructor injection, wired by hand).
func NewVenueService(pool *pgxpool.Pool) *VenueService {
	return &VenueService{pool: pool, queries: eventdb.New(pool)}
}

// Get returns the venue with this id, or ErrVenueNotFound. Any other error means the database
// couldn't answer, which is not the caller's fault.
//
// A single query needs no transaction: Postgres runs it in one of its own (autocommit).
func (s *VenueService) Get(ctx context.Context, id uuid.UUID) (Venue, error) {
	row, err := s.queries.GetVenue(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Venue{}, ErrVenueNotFound // pgx's error stays here; callers see the module's own
	}
	if err != nil {
		return Venue{}, fmt.Errorf("get venue %s: %w", id, err)
	}
	return toVenue(row), nil
}

// Create checks the input and stores it as a new venue. Invalid input returns a
// *ValidationError listing every problem, and nothing is stored.
func (s *VenueService) Create(ctx context.Context, in VenueInput) (Venue, error) {
	// in is replaced by its cleaned version (trimmed, blank optional text as nil). := is allowed
	// because err is new; in is the parameter, assigned again.
	in, err := in.clean()
	if err != nil {
		return Venue{}, err
	}

	row, err := s.queries.CreateVenue(ctx, toCreateParams(in))
	if err != nil {
		// clean checks every rule the table's CHECKs enforce. If Postgres still rejects the
		// input, clean has a bug, which is our fault (500), not the caller's, so it is not turned
		// into a ValidationError.
		return Venue{}, fmt.Errorf("create venue: %w", err)
	}
	return toVenue(row), nil
}

// The page sizes of List. The maximum keeps one request from reading the whole table.
const (
	DefaultVenuePageSize = 20
	MaxVenuePageSize     = 100
)

// VenueCursor is a position in the venue list, which is sorted by name and then id: a page
// starts right after it. It holds the values of the last venue of the previous page, not its
// row number, so venues added or removed meanwhile don't shift the pages, and the cursor still
// works if that venue itself is renamed or deleted.
type VenueCursor struct {
	Name string
	ID   uuid.UUID
}

// VenuePage is one page of venues. Next is where the next page starts, nil on the last page.
type VenuePage struct {
	Venues []Venue
	Next   *VenueCursor
}

// List returns up to limit venues with this status, sorted by name (then id), starting after
// the cursor; a nil cursor starts at the beginning. A status other than VenueActive or
// VenueArchived, or a limit outside 1..MaxVenuePageSize, is a *ValidationError.
//
// This is keyset pagination ("seek method"). OFFSET would make Postgres read and throw away every
// row before the page, so deep pages get slower, and a venue added meanwhile would shift every
// later page by one (a venue shown twice or skipped). Here Postgres jumps to the cursor's
// position in the (status, name, id) index and reads only the page.
func (s *VenueService) List(ctx context.Context, status VenueStatus, after *VenueCursor, limit int) (VenuePage, error) {
	var problems fieldErrors
	if status != VenueActive && status != VenueArchived {
		problems.add("status", fmt.Sprintf("must be %s or %s", VenueActive, VenueArchived))
	}
	if limit < 1 || limit > MaxVenuePageSize {
		problems.add("limit", fmt.Sprintf("must be between 1 and %d", MaxVenuePageSize))
	}
	if len(problems) > 0 {
		return VenuePage{}, &ValidationError{Fields: problems}
	}

	// One row more than asked for: if it comes back, another page follows. That avoids a
	// separate count query, and the client is never sent a cursor that leads to an empty page.
	params := eventdb.ListVenuesParams{
		Status:   string(status),
		PageSize: int32(limit + 1), // at most 101, fits in an int32
	}
	// Without a cursor the zero values are used: ("", zero UUID) comes before every venue,
	// because a venue's name can't be empty (CHECK in the migration), that is the first page.
	if after != nil {
		params.AfterName, params.AfterID = after.Name, after.ID
	}
	rows, err := s.queries.ListVenues(ctx, params)
	if err != nil {
		return VenuePage{}, fmt.Errorf("list venues: %w", err)
	}

	var page VenuePage
	if len(rows) > limit {
		rows = rows[:limit] // drop the extra row; it only told us there is more pages so we set the cursor.
		last := rows[limit-1]
		page.Next = &VenueCursor{Name: last.Name, ID: last.ID} // cursor not set if there is no next page.
	}
	// make with a length gives a non-nil slice even for no rows, so an empty page is [] in JSON.
	page.Venues = make([]Venue, len(rows))
	for i, row := range rows {
		page.Venues[i] = toVenue(row)
	}
	return page, nil
}

// Update changes the venue with this id and returns it as stored. version is the version the
// caller's change is based on (the one it read). In one transaction Update reads and locks the
// venue, calls change with the venue's current values, checks the changed values with the same
// rules as Create, and stores them with the version raised by 1.
//
// Nothing is stored, and the error is returned, when: no venue has the id (ErrVenueNotFound);
// the venue's version isn't version (ErrVersionMismatch: someone changed it meanwhile); it is
// archived (ErrVenueArchived); change returns an error; or the changed values break a rule (a
// *ValidationError).
//
// change gets the current values because a partial update needs them: "set the city to Ankara"
// keeps every other field as it is. It runs while the row is locked, so it should only edit in
// and not do slow work such as I/O.
//
// Two mechanisms work together here. The version check (optimistic locking, JPA's @Version)
// protects a person's edit: an admin who read version 3 can't overwrite a change made after it.
// The lock (SELECT ... FOR UPDATE, JPA's PESSIMISTIC_WRITE) protects the read-change-write
// inside this method: two requests at the same moment can't both read version 3 and both write.
func (s *VenueService) Update(ctx context.Context, id uuid.UUID, version int, change func(in *VenueInput) error) (Venue, error) {
	expected := toVersion(version)
	var updated Venue
	err := postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx) // the same queries, run in this transaction

		row, err := q.GetVenueForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVenueNotFound
		}
		if err != nil {
			return fmt.Errorf("lock venue %s: %w", id, err)
		}
		// The version first: if the venue was archived after the caller read it, its version
		// has changed too, and "read it again" is the right answer; reading it again then shows
		// it archived.
		if row.Version != expected {
			return ErrVersionMismatch
		}
		if VenueStatus(row.Status) == VenueArchived {
			return ErrVenueArchived
		}

		in := toVenue(row).input()
		if err := change(&in); err != nil {
			return err
		}
		in, err = in.clean()
		if err != nil {
			return err
		}

		// The row is locked and was just checked, so UpdateVenue's own WHERE (id, version,
		// active) matches; it stays as a guard that doesn't rely on this code.
		row, err = q.UpdateVenue(ctx, toUpdateParams(id, expected, in))
		if err != nil {
			return fmt.Errorf("update venue %s: %w", id, err) // like Create: a failing CHECK is our bug
		}
		updated = toVenue(row)
		return nil
	})
	if err != nil {
		return Venue{}, err // WithTx passes fn's error on unchanged, so errors.Is and errors.As work
	}
	return updated, nil
}

// Archive moves an active venue to archived and returns it as stored. It fails with
// ErrVenueNotFound, ErrVersionMismatch, or ErrVenueArchived if it already is.
func (s *VenueService) Archive(ctx context.Context, id uuid.UUID, version int) (Venue, error) {
	return s.setStatus(ctx, id, version, VenueActive, VenueArchived)
}

// Restore moves an archived venue back to active and returns it as stored. It fails with
// ErrVenueNotFound, ErrVersionMismatch, or ErrVenueNotArchived if the venue is active.
func (s *VenueService) Restore(ctx context.Context, id uuid.UUID, version int) (Venue, error) {
	return s.setStatus(ctx, id, version, VenueArchived, VenueActive)
}

// setStatus changes the status with one compare-and-set statement: it matches only a venue that
// still has this version and the from status. That is enough on its own, without a transaction
// or a lock, because Postgres runs a single UPDATE atomically. Only when nothing matched does it
// read the venue, to tell the caller why.
//
// In the future, archiving will also check that no upcoming event uses the venue (M1 step 8).
func (s *VenueService) setStatus(ctx context.Context, id uuid.UUID, version int, from, to VenueStatus) (Venue, error) {
	row, err := s.queries.SetVenueStatus(ctx, eventdb.SetVenueStatusParams{
		ID: id, Version: toVersion(version), FromStatus: string(from), ToStatus: string(to),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Venue{}, s.whyNoMatch(ctx, id, version, from)
	}
	if err != nil {
		return Venue{}, fmt.Errorf("set venue %s to %s: %w", id, to, err)
	}
	return toVenue(row), nil
}

// whyNoMatch reads the venue after a compare-and-set matched nothing and returns the reason:
// ErrVenueNotFound, ErrVersionMismatch, or the status error for a venue that isn't in the
// wanted status. The venue may change between the failed statement and this read, but a version
// only ever grows, so the answer still describes a real state the venue was in.
func (s *VenueService) whyNoMatch(ctx context.Context, id uuid.UUID, version int, wantStatus VenueStatus) error {
	venue, err := s.Get(ctx, id)
	if err != nil {
		return err // ErrVenueNotFound, or the database failed
	}
	if venue.Version != version {
		return ErrVersionMismatch
	}
	if venue.Status != wantStatus {
		if venue.Status == VenueArchived {
			return ErrVenueArchived
		}
		return ErrVenueNotArchived
	}
	// The version and status match now, so they matched when the statement ran: a version
	// never comes back. Reaching this line would be a bug.
	return fmt.Errorf("venue %s: compare and set matched nothing, but version %d and status %s match", id, version, wantStatus)
}

// Delete removes the venue with this id, and with it its halls, their layouts and sections. It
// fails with ErrVenueNotFound, or ErrVersionMismatch if the venue changed since the caller read
// it. Archived venues can be deleted too. Once events exist (M1 step 8), a venue that any event
// uses can't be deleted, only archived.
func (s *VenueService) Delete(ctx context.Context, id uuid.UUID, version int) error {
	deleted, err := s.queries.DeleteVenue(ctx, eventdb.DeleteVenueParams{ID: id, Version: toVersion(version)})
	if err != nil {
		return fmt.Errorf("delete venue %s: %w", id, err)
	}
	if deleted == 0 {
		return s.whyNotDeleted(ctx, id)
	}
	return nil
}

// whyNotDeleted tells a delete that matched nothing apart: no such venue, or another version.
func (s *VenueService) whyNotDeleted(ctx context.Context, id uuid.UUID) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err // ErrVenueNotFound, or the database failed
	}
	return ErrVersionMismatch
}

// toCreateParams is toVenue in reverse: the input as the generated query's parameters.
func toCreateParams(in VenueInput) eventdb.CreateVenueParams {
	p := eventdb.CreateVenueParams{
		Name:         in.Name,
		Description:  in.Description,
		AddressLine1: in.Address.Line1,
		AddressLine2: in.Address.Line2,
		City:         in.Address.City,
		Region:       in.Address.Region,
		PostalCode:   in.Address.PostalCode,
		CountryCode:  in.Address.CountryCode,
		Timezone:     in.TimeZone,
	}
	if in.Location != nil {
		p.Latitude = new(in.Location.Latitude)
		p.Longitude = new(in.Location.Longitude)
	}
	return p
}

// toUpdateParams is toCreateParams plus the id of the venue to update and the version the
// change is based on. The columns are the same, so it reuses toCreateParams and the conversions
// stay in one place.
func toUpdateParams(id uuid.UUID, version int32, in VenueInput) eventdb.UpdateVenueParams {
	p := toCreateParams(in)
	return eventdb.UpdateVenueParams{
		ID:           id,
		Version:      version,
		Name:         p.Name,
		Description:  p.Description,
		AddressLine1: p.AddressLine1,
		AddressLine2: p.AddressLine2,
		City:         p.City,
		Region:       p.Region,
		PostalCode:   p.PostalCode,
		CountryCode:  p.CountryCode,
		Latitude:     p.Latitude,
		Longitude:    p.Longitude,
		Timezone:     p.Timezone,
	}
}
