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
//
// Every change to a hall (Create, Update, Archive, Restore) locks the hall's venue with a shared
// lock (FOR SHARE) and checks the venue is active: a hall of an archived venue counts as archived
// (see HallStatus). Locks are always taken in the same order, the venue before the hall, as the
// tree goes. That order is what keeps two transactions from each holding a lock the other one
// waits for (a deadlock): deleting a venue locks the venue first and then, through ON DELETE
// CASCADE, its halls, so a hall change that locked the hall first and then waited for the venue
// could deadlock with it.
type HallService struct {
	pool    *pgxpool.Pool
	queries *eventdb.Queries
}

// NewHallService creates the service on a connection pool. cmd/ticketing calls it once at startup.
func NewHallService(pool *pgxpool.Pool) *HallService {
	return &HallService{pool: pool, queries: eventdb.New(pool)}
}

// Get returns the hall with this id, archived ones too, or ErrHallNotFound.
func (s *HallService) Get(ctx context.Context, id uuid.UUID) (Hall, error) {
	row, err := s.queries.GetHall(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Hall{}, ErrHallNotFound
	}
	if err != nil {
		return Hall{}, fmt.Errorf("get hall %s: %w", id, err)
	}
	return toHall(row), nil
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

// List returns every hall of the venue with this status, sorted by name ignoring case (then id).
// There is no paging: a venue has only a few halls. A status other than HallActive or
// HallArchived is a *ValidationError, and an unknown venue ErrVenueNotFound. The halls of an
// archived venue can be listed too.
//
// "Archived" here is the hall's own status: listing an archived venue's active halls returns the
// halls that come back when the venue is restored.
func (s *HallService) List(ctx context.Context, venueID uuid.UUID, status HallStatus) ([]Hall, error) {
	if status != HallActive && status != HallArchived {
		return nil, &ValidationError{Fields: []FieldError{
			{Field: "status", Message: fmt.Sprintf("must be %s or %s", HallActive, HallArchived)},
		}}
	}
	// ListHalls returns no rows both for a venue without halls and for an unknown venue, so the
	// venue is read first to tell them apart. Two queries without a transaction: if the venue is
	// deleted in between, the answer is an empty list, which was true a moment ago.
	if _, err := s.queries.GetVenue(ctx, venueID); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrVenueNotFound
	} else if err != nil {
		return nil, fmt.Errorf("get venue %s: %w", venueID, err)
	}

	rows, err := s.queries.ListHalls(ctx, eventdb.ListHallsParams{VenueID: venueID, Status: string(status)})
	if err != nil {
		return nil, fmt.Errorf("list halls of venue %s: %w", venueID, err)
	}
	halls := make([]Hall, len(rows)) // [] in JSON when there are none, not null
	for i, row := range rows {
		halls[i] = toHall(row)
	}
	return halls, nil
}

// Update changes the hall with this id and returns it as stored, like VenueService.Update:
// change gets the hall's current values while the row is locked, and the result is checked with
// the same rules as Create. It fails, storing nothing, with ErrHallNotFound, ErrVersionMismatch,
// ErrVenueArchived (the venue is archived), ErrHallArchived, an error from change, a
// *ValidationError, or ErrHallNameTaken (the new name is another active hall's in the venue).
func (s *HallService) Update(ctx context.Context, id uuid.UUID, version int, change func(in *HallInput) error) (Hall, error) {
	return s.change(ctx, id, version, func(q *eventdb.Queries, row eventdb.Hall) (eventdb.Hall, error) {
		if HallStatus(row.Status) == HallArchived {
			return eventdb.Hall{}, ErrHallArchived
		}
		in := toHall(row).input()
		if err := change(&in); err != nil {
			return eventdb.Hall{}, err
		}
		in, err := in.clean()
		if err != nil {
			return eventdb.Hall{}, err
		}
		p := toCreateHallParams(row.VenueID, in)
		updated, err := q.UpdateHall(ctx, eventdb.UpdateHallParams{
			ID: id, Version: row.Version, Name: p.Name, Description: p.Description, Capacity: p.Capacity,
		})
		if postgres.IsUniqueViolation(err, "halls_venue_id_name_key") {
			return eventdb.Hall{}, ErrHallNameTaken
		}
		if err != nil {
			return eventdb.Hall{}, fmt.Errorf("update hall %s: %w", id, err)
		}
		return updated, nil
	})
}

// Archive moves an active hall to archived and returns it as stored. It fails with
// ErrHallNotFound, ErrVersionMismatch, ErrVenueArchived, or ErrHallArchived if it already is.
//
// In the future, archiving will also check that no upcoming event uses the hall (M1 step 8).
func (s *HallService) Archive(ctx context.Context, id uuid.UUID, version int) (Hall, error) {
	return s.setStatus(ctx, id, version, HallActive, HallArchived)
}

// Restore moves an archived hall back to active and returns it as stored. It fails with
// ErrHallNotFound, ErrVersionMismatch, ErrVenueArchived (restore the venue first),
// ErrHallNotArchived if the hall is active, or ErrHallNameTaken if an active hall of the venue
// took its name while it was archived.
func (s *HallService) Restore(ctx context.Context, id uuid.UUID, version int) (Hall, error) {
	return s.setStatus(ctx, id, version, HallArchived, HallActive)
}

// setStatus is the shared body of Archive and Restore. Unlike VenueService.setStatus it can't be
// a single compare-and-set statement: it has to check the venue too, so it runs in change's
// transaction, and the row is already locked when SetHallStatus runs. The statement's own WHERE
// (id, version, from status) stays as a guard that doesn't rely on this code.
func (s *HallService) setStatus(ctx context.Context, id uuid.UUID, version int, from, to HallStatus) (Hall, error) {
	return s.change(ctx, id, version, func(q *eventdb.Queries, row eventdb.Hall) (eventdb.Hall, error) {
		if HallStatus(row.Status) != from {
			if from == HallActive {
				return eventdb.Hall{}, ErrHallArchived
			}
			return eventdb.Hall{}, ErrHallNotArchived
		}
		updated, err := q.SetHallStatus(ctx, eventdb.SetHallStatusParams{
			ID: id, Version: row.Version, FromStatus: string(from), ToStatus: string(to),
		})
		if postgres.IsUniqueViolation(err, "halls_venue_id_name_key") { // only when restoring
			return eventdb.Hall{}, ErrHallNameTaken
		}
		if err != nil {
			return eventdb.Hall{}, fmt.Errorf("set hall %s to %s: %w", id, to, err)
		}
		return updated, nil
	})
}

// change runs the checks every change to a hall shares, in one transaction, and then fn, which
// makes the change and returns the stored row. In order:
//
//  1. Read the hall without a lock, only to learn its venue. A hall never changes venue, so the
//     venue id can't go stale.
//  2. Lock the venue (FOR SHARE), the parent before the child (see HallService), and stop with
//     ErrVenueArchived if it is archived. The lock keeps it so until the commit.
//  3. Lock the hall (FOR UPDATE) and read it again: it may have changed before the lock.
//  4. Check the version (ErrVersionMismatch).
//
// An archived venue is refused before the version is checked. The version (the ETag) covers the
// hall's own values only; archiving the venue doesn't change it. So a caller with an old version
// would read the hall again and still be refused: the archived venue is the answer that helps.
// It also stops before the hall is locked. The hall's own status comes after the version, as in
// VenueService.Update: archiving the hall changes its version, so a caller who missed that
// should read it again first.
//
// fn then checks the hall's own status and makes the change.
func (s *HallService) change(ctx context.Context, id uuid.UUID, version int,
	fn func(q *eventdb.Queries, row eventdb.Hall) (eventdb.Hall, error)) (Hall, error) {
	expected := toVersion(version)
	var changed Hall
	err := postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)

		row, err := q.GetHall(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrHallNotFound
		}
		if err != nil {
			return fmt.Errorf("get hall %s: %w", id, err)
		}
		venue, err := q.GetVenueForShare(ctx, row.VenueID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrHallNotFound // the venue was deleted meanwhile, and the hall with it
		}
		if err != nil {
			return fmt.Errorf("lock venue %s: %w", row.VenueID, err)
		}
		if VenueStatus(venue.Status) == VenueArchived {
			return ErrVenueArchived
		}

		row, err = q.GetHallForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrHallNotFound // deleted between the first read and the lock
		}
		if err != nil {
			return fmt.Errorf("lock hall %s: %w", id, err)
		}
		if row.Version != expected {
			return ErrVersionMismatch
		}

		row, err = fn(q, row)
		if err != nil {
			return err
		}
		changed = toHall(row)
		return nil
	})
	if err != nil {
		return Hall{}, err
	}
	return changed, nil
}

// Delete removes the hall with this id, and with it its layouts, their sections and seats. It
// fails with ErrHallNotFound, or ErrVersionMismatch if the hall changed since the caller read it.
// Archived halls, and halls of an archived venue, can be deleted too: like an archived venue,
// they are deleted while nothing uses them. Once events exist (M1 step 8), a hall that any event
// uses can't be deleted, only archived.
//
// It is one compare-and-set statement, like VenueService.Delete, with no venue lock: deleting a
// hall doesn't depend on the venue's state, and the statement locks only the hall's row.
func (s *HallService) Delete(ctx context.Context, id uuid.UUID, version int) error {
	deleted, err := s.queries.DeleteHall(ctx, eventdb.DeleteHallParams{ID: id, Version: toVersion(version)})
	if err != nil {
		return fmt.Errorf("delete hall %s: %w", id, err)
	}
	if deleted == 0 {
		// No such hall, or another version: Get tells them apart.
		if _, err := s.Get(ctx, id); err != nil {
			return err // ErrHallNotFound, or the database failed
		}
		return ErrVersionMismatch
	}
	return nil
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
