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

// LayoutService holds the rules for layouts, like HallService for halls. It is safe for
// concurrent use; one instance serves every request.
//
// Every change to a layout (Create, Update, Activate, Deactivate, Archive, Restore) locks the
// layout's venue and then its hall with shared locks (FOR SHARE) and checks both are active: a
// layout of an archived hall or venue counts as archived (see LayoutStatus). The locks follow the
// tree, venue before hall before layout, for the reason given at HallService: the same order
// everywhere is what keeps two transactions from deadlocking.
type LayoutService struct {
	pool    *pgxpool.Pool
	queries *eventdb.Queries
}

// NewLayoutService creates the service on a connection pool. cmd/ticketing calls it once at
// startup.
func NewLayoutService(pool *pgxpool.Pool) *LayoutService {
	return &LayoutService{pool: pool, queries: eventdb.New(pool)}
}

// Get returns the layout with this id, archived ones too, or ErrLayoutNotFound.
func (s *LayoutService) Get(ctx context.Context, id uuid.UUID) (Layout, error) {
	row, err := s.queries.GetLayout(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Layout{}, ErrLayoutNotFound
	}
	if err != nil {
		return Layout{}, fmt.Errorf("get layout %s: %w", id, err)
	}
	return toLayout(row), nil
}

// Create checks the input and stores it as a new draft layout of the hall. It fails, storing
// nothing, with a *ValidationError for invalid input, ErrHallNotFound if no hall has the id,
// ErrVenueArchived if the hall's venue is archived, ErrHallArchived if the hall is, or
// ErrLayoutNameTaken if another layout of the hall that isn't archived has the name.
//
// As in HallService.Create, the input is checked before the database is asked anything. Then,
// in one transaction, lockHall checks the venue and the hall and keeps them so until the
// commit, and the layout is inserted.
func (s *LayoutService) Create(ctx context.Context, hallID uuid.UUID, in LayoutInput) (Layout, error) {
	in, err := in.clean()
	if err != nil {
		return Layout{}, err
	}

	var created Layout
	err = postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)
		if err := lockHall(ctx, q, hallID); err != nil {
			return err
		}

		row, err := q.CreateLayout(ctx, eventdb.CreateLayoutParams{
			HallID:      hallID,
			Name:        in.Name,
			Description: in.Description,
		})
		switch {
		case postgres.IsUniqueViolation(err, "layouts_hall_id_name_key"):
			// The unique index is the real check, as for halls: of two creates with one name at
			// the same moment, only one gets past it.
			return ErrLayoutNameTaken
		case err != nil:
			// layouts_hall_id_fkey can't break here: the hall is locked, so it can't be deleted
			// before the commit.
			return fmt.Errorf("create layout in hall %s: %w", hallID, err)
		}
		created = toLayout(row)
		return nil
	})
	if err != nil {
		return Layout{}, err
	}
	return created, nil
}

// List returns every layout of the hall with this status, sorted by name ignoring case (then
// id). There is no paging: a hall has only a few layouts. A status other than LayoutDraft,
// LayoutActive or LayoutArchived is a *ValidationError, and an unknown hall ErrHallNotFound. The
// layouts of an archived hall (or venue) can be listed too, by their own status, as for halls.
func (s *LayoutService) List(ctx context.Context, hallID uuid.UUID, status LayoutStatus) ([]Layout, error) {
	if status != LayoutDraft && status != LayoutActive && status != LayoutArchived {
		return nil, &ValidationError{Fields: []FieldError{{
			Field:   "status",
			Message: fmt.Sprintf("must be %s, %s or %s", LayoutDraft, LayoutActive, LayoutArchived),
		}}}
	}
	// As in HallService.List: the hall is read first to tell "no layouts" from "no such hall".
	if _, err := s.queries.GetHall(ctx, hallID); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrHallNotFound
	} else if err != nil {
		return nil, fmt.Errorf("get hall %s: %w", hallID, err)
	}

	rows, err := s.queries.ListLayouts(ctx, eventdb.ListLayoutsParams{HallID: hallID, Status: string(status)})
	if err != nil {
		return nil, fmt.Errorf("list layouts of hall %s: %w", hallID, err)
	}
	layouts := make([]Layout, len(rows)) // [] in JSON when there are none, not null
	for i, row := range rows {
		layouts[i] = toLayout(row)
	}
	return layouts, nil
}

// Update changes the name or description of the layout with this id and returns it as stored,
// like HallService.Update: change gets the layout's current values while the row is locked, and
// the result is checked with the same rules as Create. Drafts and active layouts can be changed
// (their name and description are labels; what an event sells is in the sections). It fails,
// storing nothing, with ErrLayoutNotFound, ErrVenueArchived, ErrHallArchived,
// ErrVersionMismatch, ErrLayoutArchived, an error from change, a *ValidationError, or
// ErrLayoutNameTaken.
func (s *LayoutService) Update(ctx context.Context, id uuid.UUID, version int, change func(in *LayoutInput) error) (Layout, error) {
	return s.change(ctx, id, version, func(q *eventdb.Queries, row eventdb.Layout) (eventdb.Layout, error) {
		if LayoutStatus(row.Status) == LayoutArchived {
			return eventdb.Layout{}, ErrLayoutArchived
		}
		in := toLayout(row).input()
		if err := change(&in); err != nil {
			return eventdb.Layout{}, err
		}
		in, err := in.clean()
		if err != nil {
			return eventdb.Layout{}, err
		}
		updated, err := q.UpdateLayout(ctx, eventdb.UpdateLayoutParams{
			ID: id, Version: row.Version, Name: in.Name, Description: in.Description,
		})
		if postgres.IsUniqueViolation(err, "layouts_hall_id_name_key") {
			return eventdb.Layout{}, ErrLayoutNameTaken
		}
		if err != nil {
			return eventdb.Layout{}, fmt.Errorf("update layout %s: %w", id, err)
		}
		return updated, nil
	})
}

// Activate moves a draft layout to active, so events can be created in it, and returns it as
// stored. It fails with ErrLayoutNotFound, ErrVenueArchived, ErrHallArchived,
// ErrVersionMismatch, ErrLayoutArchived, ErrLayoutAlreadyActive, or ErrLayoutHasNoSections: a
// layout needs at least one section, since an event is sold in its sections.
//
// The section check runs with the layout locked (FOR UPDATE), and adding or deleting a section
// will lock its layout first (M1 step 6d), so the last section can't be deleted between the
// check and the commit.
func (s *LayoutService) Activate(ctx context.Context, id uuid.UUID, version int) (Layout, error) {
	return s.change(ctx, id, version, func(q *eventdb.Queries, row eventdb.Layout) (eventdb.Layout, error) {
		switch LayoutStatus(row.Status) {
		case LayoutArchived:
			return eventdb.Layout{}, ErrLayoutArchived
		case LayoutActive:
			return eventdb.Layout{}, ErrLayoutAlreadyActive
		}
		hasSections, err := q.LayoutHasSections(ctx, id)
		if err != nil {
			return eventdb.Layout{}, fmt.Errorf("check sections of layout %s: %w", id, err)
		}
		if !hasSections {
			return eventdb.Layout{}, ErrLayoutHasNoSections
		}
		return setLayoutStatus(ctx, q, row, LayoutActive)
	})
}

// Deactivate moves an active layout back to a draft, so its sections and seats can be changed,
// and returns it as stored; Activate then checks it again. It fails with ErrLayoutNotFound,
// ErrVenueArchived, ErrHallArchived, ErrVersionMismatch, ErrLayoutArchived, or
// ErrLayoutNotActive if it is a draft already.
//
// In the future, deactivating will be refused while events use the layout (M1 step 8): no
// event can be in a draft layout.
func (s *LayoutService) Deactivate(ctx context.Context, id uuid.UUID, version int) (Layout, error) {
	return s.change(ctx, id, version, func(q *eventdb.Queries, row eventdb.Layout) (eventdb.Layout, error) {
		switch LayoutStatus(row.Status) {
		case LayoutArchived:
			return eventdb.Layout{}, ErrLayoutArchived
		case LayoutDraft:
			return eventdb.Layout{}, ErrLayoutNotActive
		}
		return setLayoutStatus(ctx, q, row, LayoutDraft)
	})
}

// Archive moves a draft or active layout to archived and returns it as stored. It fails with
// ErrLayoutNotFound, ErrVenueArchived, ErrHallArchived, ErrVersionMismatch, or
// ErrLayoutArchived if it already is.
//
// In the future, archiving will also check that no upcoming event uses the layout (M1 step 8).
func (s *LayoutService) Archive(ctx context.Context, id uuid.UUID, version int) (Layout, error) {
	return s.change(ctx, id, version, func(q *eventdb.Queries, row eventdb.Layout) (eventdb.Layout, error) {
		if LayoutStatus(row.Status) == LayoutArchived {
			return eventdb.Layout{}, ErrLayoutArchived
		}
		return setLayoutStatus(ctx, q, row, LayoutArchived)
	})
}

// Restore moves an archived layout back, always as a draft (see LayoutStatus), and returns it as
// stored. It fails with ErrLayoutNotFound, ErrVenueArchived, ErrHallArchived (restore them
// first), ErrVersionMismatch, ErrLayoutNotArchived, or ErrLayoutNameTaken if another layout of
// the hall took its name while it was archived.
func (s *LayoutService) Restore(ctx context.Context, id uuid.UUID, version int) (Layout, error) {
	return s.change(ctx, id, version, func(q *eventdb.Queries, row eventdb.Layout) (eventdb.Layout, error) {
		if LayoutStatus(row.Status) != LayoutArchived {
			return eventdb.Layout{}, ErrLayoutNotArchived
		}
		return setLayoutStatus(ctx, q, row, LayoutDraft)
	})
}

// setLayoutStatus is the shared end of Activate, Deactivate, Archive and Restore: the compare-and-set status
// change, from the status the locked row has. The statement's own WHERE (id, version, from
// status) stays as a guard that doesn't rely on this code.
func setLayoutStatus(ctx context.Context, q *eventdb.Queries, row eventdb.Layout, to LayoutStatus) (eventdb.Layout, error) {
	updated, err := q.SetLayoutStatus(ctx, eventdb.SetLayoutStatusParams{
		ID: row.ID, Version: row.Version, FromStatus: row.Status, ToStatus: string(to),
	})
	if postgres.IsUniqueViolation(err, "layouts_hall_id_name_key") { // only when restoring
		return eventdb.Layout{}, ErrLayoutNameTaken
	}
	if err != nil {
		return eventdb.Layout{}, fmt.Errorf("set layout %s to %s: %w", row.ID, to, err)
	}
	return updated, nil
}

// change runs the checks every change to a layout shares, in one transaction, and then fn, which
// makes the change and returns the stored row, like HallService.change one level down. In order:
//
//  1. Read the layout without a lock, only to learn its hall. A layout never changes hall.
//  2. lockHall: lock the venue and then the hall (FOR SHARE), stopping at an archived one.
//  3. Lock the layout (FOR UPDATE) and read it again: it may have changed before the lock.
//  4. Check the version (ErrVersionMismatch).
//
// An archived venue or hall is refused before the version is checked, for the reason given at
// HallService.change: archiving them doesn't change the layout's version (its ETag), so reading
// the layout again wouldn't help. fn then checks the layout's own status, after the version,
// since archiving the layout does change its version.
func (s *LayoutService) change(ctx context.Context, id uuid.UUID, version int,
	fn func(q *eventdb.Queries, row eventdb.Layout) (eventdb.Layout, error)) (Layout, error) {
	expected := toVersion(version)
	var changed Layout
	err := postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)

		row, err := q.GetLayout(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLayoutNotFound
		}
		if err != nil {
			return fmt.Errorf("get layout %s: %w", id, err)
		}
		// row.HallID was read from the layout itself, and layouts_hall_id_fkey keeps a layout's
		// hall in existence for as long as the layout exists. So if the hall is gone now, it (or
		// its venue) was deleted after the read above, and ON DELETE CASCADE deleted the layout
		// with it: for the caller, the layout is not found.
		if err := lockHall(ctx, q, row.HallID); errors.Is(err, ErrHallNotFound) {
			return ErrLayoutNotFound
		} else if err != nil {
			return err
		}

		row, err = q.GetLayoutForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLayoutNotFound // deleted between the first read and the lock
		}
		if err != nil {
			return fmt.Errorf("lock layout %s: %w", id, err)
		}
		if row.Version != expected {
			return ErrVersionMismatch
		}

		row, err = fn(q, row)
		if err != nil {
			return err
		}
		changed = toLayout(row)
		return nil
	})
	if err != nil {
		return Layout{}, err
	}
	return changed, nil
}

// lockHall is how every layout change checks the layout's hall and venue, in the transaction of
// q: it locks the venue and then the hall with shared locks (FOR SHARE), the parent before the
// child, and returns ErrHallNotFound, ErrVenueArchived or ErrHallArchived. The locks keep both
// active until the commit: archiving, updating or deleting them waits for the transaction, while
// other layout changes (shared locks too) don't. In order:
//
//  1. Read the hall without a lock, only to learn its venue. A hall never changes venue, so the
//     venue id can't go stale.
//  2. Lock the venue and stop if it is archived.
//  3. Lock the hall and stop if it is archived. The venue is checked first: a hall can't be
//     restored before its venue, so "restore the venue first" is the step that helps.
func lockHall(ctx context.Context, q *eventdb.Queries, hallID uuid.UUID) error {
	hall, err := q.GetHall(ctx, hallID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrHallNotFound
	}
	if err != nil {
		return fmt.Errorf("get hall %s: %w", hallID, err)
	}

	venue, err := q.GetVenueForShare(ctx, hall.VenueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrHallNotFound // the venue was deleted meanwhile, and the hall with it
	}
	if err != nil {
		return fmt.Errorf("lock venue %s: %w", hall.VenueID, err)
	}
	if VenueStatus(venue.Status) == VenueArchived {
		return ErrVenueArchived
	}

	hall, err = q.GetHallForShare(ctx, hallID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrHallNotFound // deleted between the first read and the lock
	}
	if err != nil {
		return fmt.Errorf("lock hall %s: %w", hallID, err)
	}
	if HallStatus(hall.Status) == HallArchived {
		return ErrHallArchived
	}
	return nil
}

// Delete removes the layout with this id, and with it its sections and their seats. It fails
// with ErrLayoutNotFound, or ErrVersionMismatch if the layout changed since the caller read it.
// Drafts, active and archived layouts can be deleted, also in an archived hall or venue: delete
// is for what nothing uses. Once events exist (M1 step 8), a layout that any event uses can't be
// deleted, only archived.
//
// It is one compare-and-set statement, like HallService.Delete, with no hall or venue lock:
// deleting a layout doesn't depend on their state.
func (s *LayoutService) Delete(ctx context.Context, id uuid.UUID, version int) error {
	deleted, err := s.queries.DeleteLayout(ctx, eventdb.DeleteLayoutParams{ID: id, Version: toVersion(version)})
	if err != nil {
		return fmt.Errorf("delete layout %s: %w", id, err)
	}
	if deleted == 0 {
		// No such layout, or another version: Get tells them apart.
		if _, err := s.Get(ctx, id); err != nil {
			return err // ErrLayoutNotFound, or the database failed
		}
		return ErrVersionMismatch
	}
	return nil
}
