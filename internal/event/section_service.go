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

// SectionService holds the rules for sections, like LayoutService for layouts. It is safe for
// concurrent use; one instance serves every request.
//
// A section is part of its layout's definition, so every change to one (Create, Update and,
// unlike halls and layouts, Delete too) needs its layout to be a draft: an active layout is
// always one that passed Activate's checks, and these changes could undo them (deleting the last
// section). Each change locks the venue, the hall and the layout with shared locks (FOR SHARE),
// the parent before the child as everywhere (see HallService), and the layout's lock is what
// keeps it a draft until the commit: activating it waits for the change, and a change that comes
// after an activation sees the layout active and is refused. Shared locks don't block each
// other, so two admins can change different sections of one draft at the same time.
type SectionService struct {
	pool    *pgxpool.Pool
	queries *eventdb.Queries
}

// NewSectionService creates the service on a connection pool. cmd/ticketing calls it once at
// startup.
func NewSectionService(pool *pgxpool.Pool) *SectionService {
	return &SectionService{pool: pool, queries: eventdb.New(pool)}
}

// Get returns the section with this id, or ErrSectionNotFound.
func (s *SectionService) Get(ctx context.Context, id uuid.UUID) (Section, error) {
	row, err := s.queries.GetSection(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Section{}, ErrSectionNotFound
	}
	if err != nil {
		return Section{}, fmt.Errorf("get section %s: %w", id, err)
	}
	return toSection(row), nil
}

// Create checks the input and stores it as a new section of the layout, at the given position or,
// without one, after the layout's last section. It fails, storing nothing, with a
// *ValidationError for invalid input, ErrLayoutNotFound if no layout has the id,
// ErrVenueArchived, ErrHallArchived or ErrLayoutArchived if the venue, the hall or the layout is
// archived, ErrLayoutNotDraft if the layout is active, or ErrSectionNameTaken if another section
// of the layout has the name.
func (s *SectionService) Create(ctx context.Context, layoutID uuid.UUID, in SectionInput) (Section, error) {
	in, err := in.clean()
	if err != nil {
		return Section{}, err
	}

	var created Section
	err = postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)
		if err := lockLayout(ctx, q, layoutID); err != nil {
			return err
		}

		position, err := sectionPosition(ctx, q, layoutID, in.Position)
		if err != nil {
			return err
		}
		row, err := q.CreateSection(ctx, eventdb.CreateSectionParams{
			LayoutID: layoutID,
			Name:     in.Name,
			Kind:     string(in.Kind),
			Capacity: toCapacity(in.Capacity),
			Position: position,
		})
		switch {
		case postgres.IsUniqueViolation(err, "sections_layout_id_name_key"):
			// The unique index is the real check, as for halls and layouts.
			return ErrSectionNameTaken
		case err != nil:
			// sections_layout_id_fkey can't break (the layout is locked), nor can the CHECKs
			// (clean checked them): either would be our bug.
			return fmt.Errorf("create section in layout %s: %w", layoutID, err)
		}
		created = toSection(row)
		return nil
	})
	if err != nil {
		return Section{}, err
	}
	return created, nil
}

// sectionPosition returns the position a new section gets: the given one, or one after the layout's
// last section. NextSectionPosition can't go past maxSectionPosition+1, since positions are at
// most maxSectionPosition; the CHECK and the int32 column hold that easily.
func sectionPosition(ctx context.Context, q *eventdb.Queries, layoutID uuid.UUID, given *int) (int32, error) {
	if given != nil {
		return int32(*given), nil // clean checked it is between 1 and maxSectionPosition
	}
	next, err := q.NextSectionPosition(ctx, layoutID)
	if err != nil {
		return 0, fmt.Errorf("next section position in layout %s: %w", layoutID, err)
	}
	return next, nil
}

// List returns every section of the layout in the layout's order: by position, then by name
// ignoring case. There is no paging: a layout has at most a few hundred sections. An unknown
// layout is ErrLayoutNotFound. The sections of any layout can be listed, whatever its status or
// its hall's and venue's.
func (s *SectionService) List(ctx context.Context, layoutID uuid.UUID) ([]Section, error) {
	// As in LayoutService.List: the layout is read first to tell "no sections" from "no such
	// layout".
	if _, err := s.queries.GetLayout(ctx, layoutID); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLayoutNotFound
	} else if err != nil {
		return nil, fmt.Errorf("get layout %s: %w", layoutID, err)
	}

	rows, err := s.queries.ListSections(ctx, layoutID)
	if err != nil {
		return nil, fmt.Errorf("list sections of layout %s: %w", layoutID, err)
	}
	sections := make([]Section, len(rows)) // [] in JSON when there are none, not null
	for i, row := range rows {
		sections[i] = toSection(row)
	}
	return sections, nil
}

// Update changes the section with this id and returns it as stored, like LayoutService.Update:
// change gets the section's current values while the row is locked, and the result is checked
// with the rules of Create, plus two: the kind stays as it is, and the position is set. It fails,
// storing nothing, with ErrSectionNotFound, ErrVenueArchived, ErrHallArchived, ErrLayoutArchived,
// ErrLayoutNotDraft, ErrVersionMismatch, an error from change, a *ValidationError, or
// ErrSectionNameTaken.
func (s *SectionService) Update(ctx context.Context, id uuid.UUID, version int, change func(in *SectionInput) error) (Section, error) {
	var updated Section
	err := s.change(ctx, id, version, func(q *eventdb.Queries, row eventdb.Section) error {
		current := toSection(row)
		in := current.input()
		if err := change(&in); err != nil {
			return err
		}
		in, err := in.cleanUpdate(current.Kind)
		if err != nil {
			return err
		}
		stored, err := q.UpdateSection(ctx, eventdb.UpdateSectionParams{
			ID:       id,
			Version:  row.Version,
			Name:     in.Name,
			Capacity: toCapacity(in.Capacity),
			Position: int32(*in.Position), // cleanUpdate checked it is set and in range
		})
		if postgres.IsUniqueViolation(err, "sections_layout_id_name_key") {
			return ErrSectionNameTaken
		}
		if err != nil {
			return fmt.Errorf("update section %s: %w", id, err)
		}
		updated = toSection(stored)
		return nil
	})
	if err != nil {
		return Section{}, err
	}
	return updated, nil
}

// Delete removes the section with this id, and with it its seats. It fails with
// ErrSectionNotFound, ErrVenueArchived, ErrHallArchived, ErrLayoutArchived, ErrLayoutNotDraft or
// ErrVersionMismatch. Unlike deleting a hall or a layout, it needs the layout to be a draft: it
// changes the layout's definition, and on an active layout it could remove the last section that
// Activate counted. Once tickets exist (M1 step 9), a section a ticket uses can't be deleted.
func (s *SectionService) Delete(ctx context.Context, id uuid.UUID, version int) error {
	return s.change(ctx, id, version, func(q *eventdb.Queries, row eventdb.Section) error {
		deleted, err := q.DeleteSection(ctx, eventdb.DeleteSectionParams{ID: id, Version: row.Version})
		if err != nil {
			return fmt.Errorf("delete section %s: %w", id, err)
		}
		if deleted != 1 {
			// The row is locked and its version was just checked, so it can't be gone.
			return fmt.Errorf("delete section %s: deleted %d rows, want 1", id, deleted)
		}
		return nil
	})
}

// change runs the checks every change to an existing section shares, in one transaction, and
// then fn, like LayoutService.change one level down. In order:
//
//  1. Read the section without a lock, only to learn its layout. A section never changes layout.
//  2. lockLayout: lock the venue, the hall and the layout (FOR SHARE), stopping at an archived
//     one or at a layout that isn't a draft.
//  3. Lock the section (FOR UPDATE) and read it again: it may have changed before the lock.
//  4. Check the version (ErrVersionMismatch).
//
// The refusals of step 2 come before the version, for the reason given at HallService.change:
// they don't change the section's version (its ETag), so reading the section again wouldn't help.
func (s *SectionService) change(ctx context.Context, id uuid.UUID, version int,
	fn func(q *eventdb.Queries, row eventdb.Section) error) error {
	expected := toVersion(version)
	return postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)

		row, err := q.GetSection(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSectionNotFound
		}
		if err != nil {
			return fmt.Errorf("get section %s: %w", id, err)
		}
		// row.LayoutID was read from the section itself, and sections_layout_id_fkey keeps the
		// layout in existence for as long as the section exists. So a layout that is gone now was
		// deleted after the read above (or its hall or venue was), and ON DELETE CASCADE deleted
		// the section with it: for the caller, the section is not found.
		if err := lockLayout(ctx, q, row.LayoutID); errors.Is(err, ErrLayoutNotFound) {
			return ErrSectionNotFound
		} else if err != nil {
			return err
		}

		row, err = q.GetSectionForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSectionNotFound // deleted between the first read and the lock
		}
		if err != nil {
			return fmt.Errorf("lock section %s: %w", id, err)
		}
		if row.Version != expected {
			return ErrVersionMismatch
		}
		return fn(q, row)
	})
}

// lockLayout is how every section change checks the section's layout, its hall and its venue, in
// the transaction of q: lockHall locks the venue and then the hall, and the layout is then locked
// too, all with shared locks (FOR SHARE), the parent before the child. It returns
// ErrLayoutNotFound, ErrVenueArchived, ErrHallArchived, ErrLayoutArchived or ErrLayoutNotDraft,
// in that order: the topmost refusal is the step that helps first (a layout can't be restored
// before its hall, nor deactivated in an archived hall).
func lockLayout(ctx context.Context, q *eventdb.Queries, layoutID uuid.UUID) error {
	layout, err := q.GetLayout(ctx, layoutID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLayoutNotFound
	}
	if err != nil {
		return fmt.Errorf("get layout %s: %w", layoutID, err)
	}
	// As in LayoutService.change: the hall id comes from the layout, so a missing hall was
	// deleted after the read above, and the layout with it.
	if err := lockHall(ctx, q, layout.HallID); errors.Is(err, ErrHallNotFound) {
		return ErrLayoutNotFound
	} else if err != nil {
		return err
	}

	layout, err = q.GetLayoutForShare(ctx, layoutID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLayoutNotFound // deleted between the first read and the lock
	}
	if err != nil {
		return fmt.Errorf("lock layout %s: %w", layoutID, err)
	}
	switch LayoutStatus(layout.Status) {
	case LayoutArchived:
		return ErrLayoutArchived
	case LayoutActive:
		return ErrLayoutNotDraft
	}
	return nil
}

// toCapacity converts a capacity into the column's type. clean checked it fits in an int32.
func toCapacity(capacity *int) *int32 {
	if capacity == nil {
		return nil
	}
	return new(int32(*capacity))
}
