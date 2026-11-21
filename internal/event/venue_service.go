package event

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event/eventdb"
)

// VenueService holds the rules for venues, like a Spring @Service: handlers call it, and it
// calls the generated queries. It has no Gin and no HTTP in it, so it can be used and tested
// without a web server.
//
// There is no repository layer in between: eventdb.Queries already is one, generated from SQL.
// The service is safe for concurrent use; one instance serves every request.
type VenueService struct {
	queries *eventdb.Queries
	// The pool itself joins here when a method needs a transaction (postgres.WithTx).
}

// NewVenueService creates the service on a connection pool. cmd/ticketing calls it once at
// startup and hands the service to whoever needs it (constructor injection, wired by hand).
func NewVenueService(pool *pgxpool.Pool) *VenueService {
	return &VenueService{queries: eventdb.New(pool)}
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

// List returns up to limit venues sorted by name (then id), starting after the cursor; a nil
// cursor starts at the beginning. A limit outside 1..MaxVenuePageSize is a *ValidationError.
//
// This is keyset pagination ("seek method"). OFFSET would make Postgres read and throw away every
// row before the page, so deep pages get slower, and a venue added meanwhile would shift every
// later page by one (a venue shown twice or skipped). Here Postgres jumps to the cursor's
// position in the (name, id) index and reads only the page.
func (s *VenueService) List(ctx context.Context, after *VenueCursor, limit int) (VenuePage, error) {
	if limit < 1 || limit > MaxVenuePageSize {
		return VenuePage{}, &ValidationError{Fields: []FieldError{
			{Field: "limit", Message: fmt.Sprintf("must be between 1 and %d", MaxVenuePageSize)},
		}}
	}

	// One row more than asked for: if it comes back, another page follows. That avoids a
	// separate count query, and the client is never sent a cursor that leads to an empty page.
	params := eventdb.ListVenuesParams{PageSize: int32(limit + 1)} // at most 101, fits in an int32
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
	if in.Capacity != nil {
		p.Capacity = new(int32(*in.Capacity)) // clean checked that it fits in an int32
	}
	return p
}
