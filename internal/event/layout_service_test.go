package event_test

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
)

// countLayouts returns how many layouts are stored, for the tests that check nothing was.
func countLayouts(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM layouts").Scan(&count); err != nil {
		t.Fatalf("count layouts: %v", err)
	}
	return count
}

// layoutTree is a venue with one hall, and the services to work on them.
type layoutTree struct {
	pool    *pgxpool.Pool
	venues  *event.VenueService
	halls   *event.HallService
	layouts *event.LayoutService
	hall    event.Hall
}

// newLayoutTree creates a venue (with fullInput) and one hall in it, "Main Hall".
func newLayoutTree(t *testing.T) layoutTree {
	t.Helper()
	pool := newPool(t)
	tree := layoutTree{
		pool:    pool,
		venues:  event.NewVenueService(pool),
		halls:   event.NewHallService(pool),
		layouts: event.NewLayoutService(pool),
	}
	tree.hall = addHall(t, tree.halls, createFull(t, tree.venues).ID, "Main Hall")
	return tree
}

func TestLayoutServiceCreate_StoresTheCleanedInputAsADraft(t *testing.T) {
	tree := newLayoutTree(t)

	created, err := tree.layouts.Create(t.Context(), tree.hall.ID, event.LayoutInput{
		Name:        "  Concert – standing floor ",
		Description: new(" Floor seats removed. "),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	want := event.Layout{
		ID:          created.ID,
		HallID:      tree.hall.ID,
		Name:        "Concert – standing floor",
		Description: new("Floor seats removed."),
		Status:      event.LayoutDraft,
		Version:     1,
		CreatedAt:   created.CreatedAt,
		UpdatedAt:   created.CreatedAt,
	}
	if !reflect.DeepEqual(created, want) {
		t.Errorf("Create returned\n %+v\nwant\n %+v", created, want)
	}
	if created.ID == uuid.Nil() || created.CreatedAt.IsZero() {
		t.Errorf("id %v, created_at %v: want both filled in by Postgres", created.ID, created.CreatedAt)
	}
}

func TestLayoutServiceCreate_DescriptionCanBeLeftOut(t *testing.T) {
	tree := newLayoutTree(t)

	// A blank description is the same as none, as for venues and halls.
	created, err := tree.layouts.Create(t.Context(), tree.hall.ID,
		event.LayoutInput{Name: "Basketball", Description: new("   ")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Description != nil {
		t.Errorf("description %v: want nil", created.Description)
	}
}

func TestLayoutServiceCreate_InvalidInputIsAValidationErrorAndStoresNothing(t *testing.T) {
	tree := newLayoutTree(t)
	long := func(n int) string { return strings.Repeat("a", n) }

	tests := []struct {
		name string
		in   event.LayoutInput
		want []event.FieldError
	}{
		{"blank name", event.LayoutInput{Name: "  "},
			[]event.FieldError{{Field: "name", Message: "is required"}}},
		{"every field wrong at once", event.LayoutInput{Name: long(201), Description: new(long(5001))},
			[]event.FieldError{
				{Field: "name", Message: "must be at most 200 characters"},
				{Field: "description", Message: "must be at most 5000 characters"},
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tree.layouts.Create(t.Context(), tree.hall.ID, tt.in)

			var invalid *event.ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("got %v, want a *event.ValidationError", err)
			}
			if !reflect.DeepEqual(invalid.Fields, tt.want) {
				t.Errorf("problems:\ngot  %v\nwant %v", invalid.Fields, tt.want)
			}
		})
	}
	if n := countLayouts(t, tree.pool); n != 0 {
		t.Errorf("layouts stored: got %d, want 0", n)
	}
}

func TestLayoutServiceCreate_InvalidInputIsReportedBeforeTheHallIsLookedUp(t *testing.T) {
	layouts := event.NewLayoutService(newPool(t))

	_, err := layouts.Create(t.Context(), uuid.New(), event.LayoutInput{}) // unknown hall and no name

	var invalid *event.ValidationError
	if !errors.As(err, &invalid) {
		t.Errorf("got %v, want a *event.ValidationError", err)
	}
}

// Each refusal stores nothing. An archived venue is reported before an archived hall: the hall
// can't be restored before its venue, so "restore the venue first" is the step that helps.
func TestLayoutServiceCreate_Refusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, tree layoutTree) uuid.UUID // returns the hall id to create in
		want  error
	}{
		{"unknown hall", func(*testing.T, layoutTree) uuid.UUID { return uuid.New() }, event.ErrHallNotFound},
		{"archived hall", func(t *testing.T, tree layoutTree) uuid.UUID {
			archiveHall(t, tree.halls, tree.hall)
			return tree.hall.ID
		}, event.ErrHallArchived},
		{"active hall of an archived venue", func(t *testing.T, tree layoutTree) uuid.UUID {
			archiveVenue(t, tree.venues, tree.hall.VenueID)
			return tree.hall.ID
		}, event.ErrVenueArchived},
		{"archived hall of an archived venue", func(t *testing.T, tree layoutTree) uuid.UUID {
			archiveHall(t, tree.halls, tree.hall)
			archiveVenue(t, tree.venues, tree.hall.VenueID)
			return tree.hall.ID
		}, event.ErrVenueArchived},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree := newLayoutTree(t)
			hallID := tt.setup(t, tree)

			_, err := tree.layouts.Create(t.Context(), hallID, event.LayoutInput{Name: "Basketball"})
			if !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
			if n := countLayouts(t, tree.pool); n != 0 {
				t.Errorf("layouts stored: got %d, want 0", n)
			}
		})
	}
}

func TestLayoutServiceCreate_NameTakenInTheHallIsErrLayoutNameTaken(t *testing.T) {
	tree := newLayoutTree(t)
	addLayout(t, tree.layouts, tree.hall.ID, "Basketball")

	_, err := tree.layouts.Create(t.Context(), tree.hall.ID, event.LayoutInput{Name: "BASKETBALL"}) // case is ignored
	if !errors.Is(err, event.ErrLayoutNameTaken) {
		t.Errorf("same name in the same hall: got %v, want ErrLayoutNameTaken", err)
	}

	// Another hall may use the name.
	other := addHall(t, tree.halls, tree.hall.VenueID, "Second Hall")
	if _, err := tree.layouts.Create(t.Context(), other.ID, event.LayoutInput{Name: "Basketball"}); err != nil {
		t.Errorf("same name in another hall: %v, want it stored", err)
	}
}

// Many requests create a layout with the same name at the same moment. Each passes the venue and
// hall checks (shared locks don't wait for each other); the unique index lets exactly one of them
// insert, and every other one gets ErrLayoutNameTaken, not a database error (a 500).
func TestLayoutServiceCreate_ConcurrentCreatesWithOneNameHaveOneWinner(t *testing.T) {
	tree := newLayoutTree(t)

	const n = 10
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_, errs[i] = tree.layouts.Create(t.Context(), tree.hall.ID, event.LayoutInput{Name: "Basketball"})
		})
	}
	wg.Wait()

	var won int
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, event.ErrLayoutNameTaken):
			t.Errorf("got %v, want nil or ErrLayoutNameTaken", err)
		}
	}
	if won != 1 {
		t.Errorf("%d creates succeeded, want 1", won)
	}
	if got := countLayouts(t, tree.pool); got != 1 {
		t.Errorf("layouts stored: got %d, want 1", got)
	}
}

// Create must lock the venue before the hall, for the reason given at
// TestHallServiceChange_LocksTheVenueBeforeTheHall: deleting a venue locks the venue and then its
// halls, so a create that held the hall while waiting for the venue could deadlock with it. Here
// another transaction holds the venue, as a delete in progress would; the create must then be
// waiting for the venue without holding the hall.
func TestLayoutServiceCreate_LocksTheVenueBeforeTheHall(t *testing.T) {
	tree := newLayoutTree(t)
	ctx := t.Context()

	held, err := tree.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = held.Rollback(ctx) }() // a no-op once rolled back below
	if _, err := held.Exec(ctx, "SELECT 1 FROM venues WHERE id = $1 FOR UPDATE", tree.hall.VenueID); err != nil {
		t.Fatalf("lock venue: %v", err)
	}

	done := make(chan error, 1) // buffered, so the goroutine never blocks on sending
	go func() {
		_, err := tree.layouts.Create(ctx, tree.hall.ID, event.LayoutInput{Name: "Basketball"})
		done <- err
	}()
	waitForALockWait(t, tree.pool)

	// NOWAIT fails at once (SQLSTATE 55P03) instead of waiting if anyone holds the hall's row,
	// even with a shared lock.
	if _, err := tree.pool.Exec(ctx, "SELECT 1 FROM halls WHERE id = $1 FOR UPDATE NOWAIT", tree.hall.ID); err != nil {
		t.Errorf("the hall is locked while the create waits for its venue: %v", err)
	}

	if err := held.Rollback(ctx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := <-done; err != nil {
		t.Errorf("Create after the venue was released: %v", err)
	}
}

func TestLayoutServiceCreate_DatabaseFailureIsNoneOfTheModulesErrors(t *testing.T) {
	tree := newLayoutTree(t)
	tree.pool.Close() // every query now fails; Close may be called again by the cleanup

	_, err := tree.layouts.Create(t.Context(), tree.hall.ID, event.LayoutInput{Name: "Basketball"})

	var invalid *event.ValidationError
	if err == nil || errors.As(err, &invalid) || errors.Is(err, event.ErrHallNotFound) ||
		errors.Is(err, event.ErrHallArchived) || errors.Is(err, event.ErrVenueArchived) ||
		errors.Is(err, event.ErrLayoutNameTaken) {
		t.Errorf("got %v, want a database error", err)
	}
}

// addLayout creates a layout with this name in the hall.
func addLayout(t *testing.T, service *event.LayoutService, hallID uuid.UUID, name string) event.Layout {
	t.Helper()
	layout, err := service.Create(t.Context(), hallID, event.LayoutInput{Name: name})
	if err != nil {
		t.Fatalf("Create %q: %v", name, err)
	}
	return layout
}

// archiveHall archives the hall at the version it was given with.
func archiveHall(t *testing.T, halls *event.HallService, hall event.Hall) {
	t.Helper()
	if _, err := halls.Archive(t.Context(), hall.ID, hall.Version); err != nil {
		t.Fatalf("Archive hall: %v", err)
	}
}
