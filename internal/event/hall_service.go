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

// HallService holds the rules for halls, like VenueService for venues. It is safe for concurrent
// use; one instance serves every request.
type HallService struct {
	pool    *pgxpool.Pool
	queries *eventdb.Queries
}

// NewHallService creates the service on a connection pool. cmd/ticketing calls it once at startup.
func NewHallService(pool *pgxpool.Pool) *HallService {
	return &HallService{pool: pool, queries: eventdb.New(pool)}
}

// Create checks the input and stores it as a new, active hall of the venue. It fails, storing
// nothing, with a *ValidationError for invalid input, ErrVenueNotFound if no venue has the id,
// ErrVenueArchived if the venue is archived, or ErrHallNameTaken if another active hall of the
// venue has the name.
//
// The input is checked first, before the database is asked anything, like Spring's @Valid on a
// @RequestBody: a request that is wrong in itself is a 400 whatever the venue's state.
//
// Then, in one transaction, the venue is read with a shared lock (FOR SHARE) to check it is
// active, and the hall is inserted. The lock is what makes the check hold until the commit:
// without it, another request could archive the venue between the check and the insert, leaving
// an active hall in an archived venue. A shared lock (unlike FOR UPDATE) doesn't block other
// shared locks, so halls of one venue can still be created at the same time; only changes to the
// venue itself (archive, update, delete) wait for this transaction to end.
func (s *HallService) Create(ctx context.Context, venueID uuid.UUID, in HallInput) (Hall, error) {
	in, err := in.clean()
	if err != nil {
		return Hall{}, err
	}

	var created Hall
	err = postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)

		venue, err := q.GetVenueForShare(ctx, venueID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVenueNotFound
		}
		if err != nil {
			return fmt.Errorf("lock venue %s: %w", venueID, err)
		}
		if VenueStatus(venue.Status) == VenueArchived {
			return ErrVenueArchived
		}

		row, err := q.CreateHall(ctx, toCreateHallParams(venueID, in))
		switch {
		case postgres.IsUniqueViolation(err, "halls_venue_id_name_key"):
			// The unique index is the real check, not a SELECT before the insert: two requests
			// with the same name at the same moment would both pass a SELECT, but only one of
			// them gets past the index.
			return ErrHallNameTaken
		case err != nil:
			// halls_venue_id_fkey can't break here: the venue was just read and is locked, so it
			// can't be deleted before the commit. Like a failing CHECK, it would be our bug.
			return fmt.Errorf("create hall in venue %s: %w", venueID, err)
		}
		created = toHall(row)
		return nil
	})
	if err != nil {
		return Hall{}, err
	}
	return created, nil
}

// toCreateHallParams is toHall in reverse: the input as the generated query's parameters.
func toCreateHallParams(venueID uuid.UUID, in HallInput) eventdb.CreateHallParams {
	p := eventdb.CreateHallParams{
		VenueID:     venueID,
		Name:        in.Name,
		Description: in.Description,
	}
	if in.Capacity != nil {
		p.Capacity = new(int32(*in.Capacity)) // clean checked it fits
	}
	return p
}
