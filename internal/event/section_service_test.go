package event_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"uuid"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
)

// sectionTree is a venue with a hall and a draft layout, and the services to work on them.
type sectionTree struct {
	layoutTree
	sections *event.SectionService
	layout   event.Layout
}

// newSectionTree creates a venue, a hall and a draft layout "Concert" in it.
func newSectionTree(t *testing.T) sectionTree {
	t.Helper()
	tree, layout := newLayout(t, "Concert")
	return sectionTree{layoutTree: tree, sections: event.NewSectionService(tree.pool), layout: layout}
}

// floor is a valid standing section's input.
func floor(name string) event.SectionInput {
	return event.SectionInput{Name: name, Kind: event.SectionStanding, Capacity: new(500)}
}

// block is a valid seated section's input.
func block(name string) event.SectionInput {
	return event.SectionInput{Name: name, Kind: event.SectionSeated}
}

// createSection creates a section in the tree's layout through the service.
func createSection(t *testing.T, tree sectionTree, in event.SectionInput) event.Section {
	t.Helper()
	s, err := tree.sections.Create(t.Context(), tree.layout.ID, in)
	if err != nil {
		t.Fatalf("Create %q: %v", in.Name, err)
	}
	return s
}

// checkSectionUnchanged fails the test unless the stored section is still want.
func checkSectionUnchanged(t *testing.T, service *event.SectionService, want event.Section) {
	t.Helper()
	got, err := service.Get(t.Context(), want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stored section:\n got  %+v\n want %+v", got, want)
	}
}

// countSections returns how many sections are stored.
func countSections(t *testing.T, tree sectionTree) int {
	t.Helper()
	var n int
	if err := tree.pool.QueryRow(t.Context(), "SELECT count(*) FROM sections").Scan(&n); err != nil {
		t.Fatalf("count sections: %v", err)
	}
	return n
}

// activate activates the tree's layout (it needs a section first) and returns it.
func activate(t *testing.T, tree sectionTree) event.Layout {
	t.Helper()
	layout, err := tree.layouts.Activate(t.Context(), tree.layout.ID, tree.layout.Version)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	return layout
}

func TestSectionServiceCreate_StoresTheCleanedInput(t *testing.T) {
	tree := newSectionTree(t)

	created, err := tree.sections.Create(t.Context(), tree.layout.ID, event.SectionInput{
		Name: "  Golden Circle ", Kind: event.SectionStanding, Capacity: new(800),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := event.Section{
		ID:        created.ID,
		LayoutID:  tree.layout.ID,
		Name:      "Golden Circle",
		Kind:      event.SectionStanding,
		Capacity:  new(800),
		Position:  1, // the first section
		Version:   1,
		CreatedAt: created.CreatedAt,
		UpdatedAt: created.CreatedAt,
	}
	if !reflect.DeepEqual(created, want) {
		t.Errorf("Create returned\n %+v\nwant\n %+v", created, want)
	}
	checkSectionUnchanged(t, tree.sections, created) // stored as returned

	// The layout's own version doesn't change: a section has its own.
	if layout, err := tree.layouts.Get(t.Context(), tree.layout.ID); err != nil || layout.Version != 1 {
		t.Errorf("layout after a section was added: %+v, %v; want version 1", layout, err)
	}
}

// Without a position, a section goes after the layout's last one; with one, it goes there.
func TestSectionServiceCreate_Position(t *testing.T) {
	tree := newSectionTree(t)

	steps := []struct {
		in   event.SectionInput
		want int
	}{
		{floor("Floor"), 1},
		{event.SectionInput{Name: "Balcony", Kind: event.SectionSeated, Position: new(5)}, 5},
		{block("Stalls"), 6},
		{event.SectionInput{Name: "Pit", Kind: event.SectionStanding, Capacity: new(50), Position: new(1)}, 1},
	}
	for _, step := range steps {
		if got := createSection(t, tree, step.in); got.Position != step.want {
			t.Errorf("%s: position %d, want %d", step.in.Name, got.Position, step.want)
		}
	}
}

func TestSectionServiceCreate_InvalidInputIsAValidationErrorAndStoresNothing(t *testing.T) {
	tree := newSectionTree(t)
	capacityRange := "must be between 1 and 2147483647"

	tests := []struct {
		name string
		in   event.SectionInput
		want []event.FieldError
	}{
		{"blank name", event.SectionInput{Name: " ", Kind: event.SectionSeated},
			[]event.FieldError{{Field: "name", Message: "is required"}}},
		{"name over 100 characters", event.SectionInput{Name: strings.Repeat("a", 101), Kind: event.SectionSeated},
			[]event.FieldError{{Field: "name", Message: "must be at most 100 characters"}}},
		{"unknown kind", event.SectionInput{Name: "Box", Kind: "box"},
			[]event.FieldError{{Field: "kind", Message: "must be seated or standing"}}},
		{"standing without a capacity", event.SectionInput{Name: "Floor", Kind: event.SectionStanding},
			[]event.FieldError{{Field: "capacity", Message: "is required for a standing section"}}},
		{"standing with capacity 0", event.SectionInput{Name: "Floor", Kind: event.SectionStanding, Capacity: new(0)},
			[]event.FieldError{{Field: "capacity", Message: capacityRange}}},
		{"seated with a capacity", event.SectionInput{Name: "Block", Kind: event.SectionSeated, Capacity: new(10)},
			[]event.FieldError{{Field: "capacity", Message: "must be left out for a seated section: its seats are its capacity"}}},
		{"position 0", event.SectionInput{Name: "Block", Kind: event.SectionSeated, Position: new(0)},
			[]event.FieldError{{Field: "position", Message: "must be between 1 and 10000"}}},
		{"position over the limit", event.SectionInput{Name: "Block", Kind: event.SectionSeated, Position: new(10001)},
			[]event.FieldError{{Field: "position", Message: "must be between 1 and 10000"}}},
		{"every field wrong at once", event.SectionInput{Name: "", Kind: event.SectionStanding, Position: new(-1)},
			[]event.FieldError{
				{Field: "name", Message: "is required"},
				{Field: "capacity", Message: "is required for a standing section"},
				{Field: "position", Message: "must be between 1 and 10000"},
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tree.sections.Create(t.Context(), tree.layout.ID, tt.in)
			var invalid *event.ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("got %v, want a *event.ValidationError", err)
			}
			if !reflect.DeepEqual(invalid.Fields, tt.want) {
				t.Errorf("problems:\ngot  %v\nwant %v", invalid.Fields, tt.want)
			}
		})
	}
	if n := countSections(t, tree); n != 0 {
		t.Errorf("sections stored: got %d, want 0", n)
	}
}

func TestSectionServiceCreate_InvalidInputIsReportedBeforeTheLayoutIsLookedUp(t *testing.T) {
	sections := event.NewSectionService(newPool(t))

	_, err := sections.Create(t.Context(), uuid.New(), event.SectionInput{}) // unknown layout, no name
	var invalid *event.ValidationError
	if !errors.As(err, &invalid) {
		t.Errorf("got %v, want a *event.ValidationError", err)
	}
}

// A section is created only in a draft layout of an active hall of an active venue. The topmost
// refusal wins: it is the step that helps first.
func TestSectionServiceCreate_Refusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, tree sectionTree) uuid.UUID // returns the layout id to create in
		want  error
	}{
		{"unknown layout", func(*testing.T, sectionTree) uuid.UUID { return uuid.New() }, event.ErrLayoutNotFound},
		{"active layout", func(t *testing.T, tree sectionTree) uuid.UUID {
			addSection(t, tree.layoutTree, tree.layout.ID, "Floor")
			activate(t, tree)
			return tree.layout.ID
		}, event.ErrLayoutNotDraft},
		{"archived layout", func(t *testing.T, tree sectionTree) uuid.UUID {
			if _, err := tree.layouts.Archive(t.Context(), tree.layout.ID, tree.layout.Version); err != nil {
				t.Fatalf("Archive: %v", err)
			}
			return tree.layout.ID
		}, event.ErrLayoutArchived},
		{"archived hall", func(t *testing.T, tree sectionTree) uuid.UUID {
			archiveHall(t, tree.halls, tree.hall)
			return tree.layout.ID
		}, event.ErrHallArchived},
		{"archived venue and hall", func(t *testing.T, tree sectionTree) uuid.UUID {
			archiveHall(t, tree.halls, tree.hall)
			archiveVenue(t, tree.venues, tree.hall.VenueID)
			return tree.layout.ID
		}, event.ErrVenueArchived},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree := newSectionTree(t)
			layoutID := tt.setup(t, tree)
			before := countSections(t, tree)

			if _, err := tree.sections.Create(t.Context(), layoutID, block("Block 101")); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
			if n := countSections(t, tree); n != before {
				t.Errorf("sections stored: got %d, want %d", n, before)
			}
		})
	}
}

func TestSectionServiceCreate_NameTakenInTheLayoutIsErrSectionNameTaken(t *testing.T) {
	tree := newSectionTree(t)
	createSection(t, tree, floor("Floor"))

	// Every section of the layout counts, whatever its kind; case is ignored.
	if _, err := tree.sections.Create(t.Context(), tree.layout.ID, block("FLOOR")); !errors.Is(err, event.ErrSectionNameTaken) {
		t.Errorf("same name in the same layout: got %v, want ErrSectionNameTaken", err)
	}

	other := addLayout(t, tree.layouts, tree.hall.ID, "Basketball")
	if _, err := tree.sections.Create(t.Context(), other.ID, floor("Floor")); err != nil {
		t.Errorf("same name in another layout: %v, want it stored", err)
	}
}

// Many requests create a section with one name at once: the shared locks let them all through
// the checks, and the unique index lets exactly one insert.
func TestSectionServiceCreate_ConcurrentCreatesWithOneNameHaveOneWinner(t *testing.T) {
	tree := newSectionTree(t)

	const n = 10
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_, errs[i] = tree.sections.Create(t.Context(), tree.layout.ID, floor("Floor"))
		})
	}
	wg.Wait()

	var won int
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, event.ErrSectionNameTaken):
			t.Errorf("got %v, want nil or ErrSectionNameTaken", err)
		}
	}
	if won != 1 || countSections(t, tree) != 1 {
		t.Errorf("%d creates succeeded and %d sections stored, want 1 and 1", won, countSections(t, tree))
	}
}

func TestSectionServiceGet_ReturnsTheStoredSectionOrErrSectionNotFound(t *testing.T) {
	tree := newSectionTree(t)
	section := createSection(t, tree, block("Balcony"))

	got, err := tree.sections.Get(t.Context(), section.ID)
	if err != nil || !reflect.DeepEqual(got, section) {
		t.Errorf("Get: got %+v, %v; want %+v", got, err, section)
	}
	if _, err := tree.sections.Get(t.Context(), uuid.New()); !errors.Is(err, event.ErrSectionNotFound) {
		t.Errorf("unknown id: got %v, want ErrSectionNotFound", err)
	}
}

func TestSectionServiceList_ReturnsTheLayoutsSectionsInOrder(t *testing.T) {
	tree := newSectionTree(t)
	createSection(t, tree, event.SectionInput{Name: "Table 10", Kind: event.SectionSeated, Position: new(3)})
	createSection(t, tree, event.SectionInput{Name: "Table 2", Kind: event.SectionSeated, Position: new(2)})
	createSection(t, tree, event.SectionInput{Name: "Floor", Kind: event.SectionStanding, Capacity: new(500), Position: new(1)})
	createSection(t, tree, event.SectionInput{Name: "Box", Kind: event.SectionSeated, Position: new(4)})
	createSection(t, tree, event.SectionInput{Name: "balcony", Kind: event.SectionSeated, Position: new(4)})
	other := addLayout(t, tree.layouts, tree.hall.ID, "Basketball")
	if _, err := tree.sections.Create(t.Context(), other.ID, floor("Court side")); err != nil {
		t.Fatalf("Create in the other layout: %v", err)
	}

	sections, err := tree.sections.List(t.Context(), tree.layout.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var got []string
	for _, s := range sections {
		got = append(got, s.Name)
	}
	if want := []string{"Floor", "Table 2", "Table 10", "balcony", "Box"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}

	// The sections of an active layout can be listed too.
	activate(t, tree)
	if sections, err := tree.sections.List(t.Context(), tree.layout.ID); err != nil || len(sections) != 5 {
		t.Errorf("active layout: got %d sections, %v; want 5", len(sections), err)
	}
}

func TestSectionServiceList_NoSectionsOrNoLayout(t *testing.T) {
	tree := newSectionTree(t)

	sections, err := tree.sections.List(t.Context(), tree.layout.ID)
	if err != nil || sections == nil || len(sections) != 0 {
		t.Errorf("no sections: got %#v, %v; want an empty slice, not nil", sections, err)
	}
	if _, err := tree.sections.List(t.Context(), uuid.New()); !errors.Is(err, event.ErrLayoutNotFound) {
		t.Errorf("unknown layout: got %v, want ErrLayoutNotFound", err)
	}
}

func TestSectionServiceUpdate_StoresTheCleanedValuesAndBumpsTheVersion(t *testing.T) {
	tree := newSectionTree(t)
	section := createSection(t, tree, floor("Floor"))

	updated, err := tree.sections.Update(t.Context(), section.ID, 1, func(in *event.SectionInput) error {
		in.Name = " Golden Circle "
		in.Capacity = new(800)
		in.Position = new(4)
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "Golden Circle" || *updated.Capacity != 800 || updated.Position != 4 ||
		updated.Kind != event.SectionStanding || updated.Version != 2 || !updated.UpdatedAt.After(section.UpdatedAt) {
		t.Errorf("got %+v; want the cleaned values, still standing, version 2, a later updated_at", updated)
	}
	checkSectionUnchanged(t, tree.sections, updated)
}

func TestSectionServiceUpdate_RefusalsStoreNothing(t *testing.T) {
	tree := newSectionTree(t)
	section := createSection(t, tree, floor("Floor"))
	createSection(t, tree, block("Balcony"))
	errChange := errors.New("change refused")
	set := func(f func(in *event.SectionInput)) func(*event.SectionInput) error {
		return func(in *event.SectionInput) error { f(in); return nil }
	}

	tests := []struct {
		name    string
		id      uuid.UUID
		version int
		change  func(*event.SectionInput) error
		want    error
	}{
		{"unknown id", uuid.New(), 1, set(func(in *event.SectionInput) {}), event.ErrSectionNotFound},
		{"other version", section.ID, 2, set(func(in *event.SectionInput) {}), event.ErrVersionMismatch},
		{"name of another section", section.ID, 1, set(func(in *event.SectionInput) { in.Name = "BALCONY" }), event.ErrSectionNameTaken},
		{"error from change", section.ID, 1, func(*event.SectionInput) error { return errChange }, errChange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tree.sections.Update(t.Context(), tt.id, tt.version, tt.change); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
		})
	}

	invalid := []struct {
		name   string
		change func(in *event.SectionInput)
		want   []event.FieldError
	}{
		{"kind changed", func(in *event.SectionInput) { in.Kind = event.SectionSeated; in.Capacity = nil },
			[]event.FieldError{
				{Field: "kind", Message: "can't be changed: delete the section and create another"},
				// The capacity is checked against the kind the section keeps.
				{Field: "capacity", Message: "is required for a standing section"},
			}},
		{"capacity cleared", func(in *event.SectionInput) { in.Capacity = nil },
			[]event.FieldError{{Field: "capacity", Message: "is required for a standing section"}}},
		{"position cleared", func(in *event.SectionInput) { in.Position = nil },
			[]event.FieldError{{Field: "position", Message: "is required"}}},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tree.sections.Update(t.Context(), section.ID, 1, set(tt.change))
			var problems *event.ValidationError
			if !errors.As(err, &problems) || !reflect.DeepEqual(problems.Fields, tt.want) {
				t.Errorf("got %v, want a *ValidationError with %v", err, tt.want)
			}
		})
	}
	checkSectionUnchanged(t, tree.sections, section)
}

// Sections change only while their layout is a draft, and an archived layout, hall or venue
// refuses before the version is checked: reading the section again wouldn't help.
func TestSectionServiceChanges_NeedADraftLayout(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, tree sectionTree)
		want  error
	}{
		{"active layout", func(t *testing.T, tree sectionTree) { activate(t, tree) }, event.ErrLayoutNotDraft},
		{"archived layout", func(t *testing.T, tree sectionTree) {
			if _, err := tree.layouts.Archive(t.Context(), tree.layout.ID, tree.layout.Version); err != nil {
				t.Fatalf("Archive: %v", err)
			}
		}, event.ErrLayoutArchived},
		{"archived hall", func(t *testing.T, tree sectionTree) { archiveHall(t, tree.halls, tree.hall) }, event.ErrHallArchived},
		{"archived venue", func(t *testing.T, tree sectionTree) { archiveVenue(t, tree.venues, tree.hall.VenueID) }, event.ErrVenueArchived},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree := newSectionTree(t)
			section := createSection(t, tree, floor("Floor"))
			tt.setup(t, tree)

			for _, version := range []int{1, 9} {
				t.Run(fmt.Sprintf("version %d", version), func(t *testing.T) {
					_, err := tree.sections.Update(t.Context(), section.ID, version, func(in *event.SectionInput) error {
						in.Name = "Pit"
						return nil
					})
					if !errors.Is(err, tt.want) {
						t.Errorf("update: got %v, want %v", err, tt.want)
					}
					if err := tree.sections.Delete(t.Context(), section.ID, version); !errors.Is(err, tt.want) {
						t.Errorf("delete: got %v, want %v", err, tt.want)
					}
				})
			}
			checkSectionUnchanged(t, tree.sections, section)
		})
	}
}

func TestSectionServiceDelete_DeletesTheExpectedVersion(t *testing.T) {
	tree := newSectionTree(t)
	section := createSection(t, tree, floor("Floor"))

	if err := tree.sections.Delete(t.Context(), section.ID, 2); !errors.Is(err, event.ErrVersionMismatch) {
		t.Errorf("other version: got %v, want ErrVersionMismatch", err)
	}
	checkSectionUnchanged(t, tree.sections, section)

	if err := tree.sections.Delete(t.Context(), section.ID, 1); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := tree.sections.Get(t.Context(), section.ID); !errors.Is(err, event.ErrSectionNotFound) {
		t.Errorf("Get after delete: got %v, want ErrSectionNotFound", err)
	}
	if err := tree.sections.Delete(t.Context(), section.ID, 1); !errors.Is(err, event.ErrSectionNotFound) {
		t.Errorf("delete again: got %v, want ErrSectionNotFound", err)
	}
}

// The race the layout lock is for: an activation is in progress (it holds the layout FOR UPDATE
// and has made it active, not yet committed) while the layout's last section is deleted. The
// delete must wait for the activation, then see the layout active and refuse, so an active
// layout never ends up without sections.
func TestSectionServiceDelete_WaitsForAnActivationInProgressAndIsRefused(t *testing.T) {
	tree := newSectionTree(t)
	ctx := t.Context()
	section := createSection(t, tree, floor("Floor"))

	activation, err := tree.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = activation.Rollback(ctx) }() // a no-op once committed
	if _, err := activation.Exec(ctx, "SELECT 1 FROM layouts WHERE id = $1 FOR UPDATE", tree.layout.ID); err != nil {
		t.Fatalf("lock layout: %v", err)
	}
	if _, err := activation.Exec(ctx,
		"UPDATE layouts SET status = 'active', version = version + 1 WHERE id = $1", tree.layout.ID); err != nil {
		t.Fatalf("activate: %v", err)
	}

	done := make(chan error, 1) // buffered, so the goroutine never blocks on sending
	go func() { done <- tree.sections.Delete(ctx, section.ID, section.Version) }()
	waitForALockWait(t, tree.pool)

	if err := activation.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := <-done; !errors.Is(err, event.ErrLayoutNotDraft) {
		t.Errorf("delete after the activation: got %v, want ErrLayoutNotDraft", err)
	}
	checkSectionUnchanged(t, tree.sections, section)
}

// A section change locks the venue, the hall, the layout and then the section, for the reason
// given at TestHallServiceChange_LocksTheVenueBeforeTheHall. Another transaction holds one level,
// as a delete in progress would; the change must wait for it without holding anything below it.
func TestSectionServiceChange_LocksTheTreeFromTheTop(t *testing.T) {
	tests := []struct {
		held string   // the table whose row the other transaction holds
		free []string // the tables below it, whose rows must be free while the change waits
	}{
		{"venues", []string{"halls", "layouts", "sections"}},
		{"halls", []string{"layouts", "sections"}},
		{"layouts", []string{"sections"}},
	}
	for _, tt := range tests {
		t.Run(tt.held+" held", func(t *testing.T) {
			tree := newSectionTree(t)
			ctx := t.Context()
			section := createSection(t, tree, floor("Floor"))
			idOf := map[string]uuid.UUID{
				"venues": tree.hall.VenueID, "halls": tree.hall.ID, "layouts": tree.layout.ID, "sections": section.ID,
			}

			held, err := tree.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			defer func() { _ = held.Rollback(ctx) }() // a no-op once rolled back below
			// The table names are the test's own constants, never input, so Sprintf is safe here.
			lock := fmt.Sprintf("SELECT 1 FROM %s WHERE id = $1 FOR UPDATE", tt.held)
			if _, err := held.Exec(ctx, lock, idOf[tt.held]); err != nil {
				t.Fatalf("hold %s: %v", tt.held, err)
			}

			done := make(chan error, 1)
			go func() {
				_, err := tree.sections.Update(ctx, section.ID, section.Version, func(in *event.SectionInput) error {
					in.Name = "Pit"
					return nil
				})
				done <- err
			}()
			waitForALockWait(t, tree.pool)

			// NOWAIT fails at once (SQLSTATE 55P03) instead of waiting if anyone holds the row.
			for _, table := range tt.free {
				query := fmt.Sprintf("SELECT 1 FROM %s WHERE id = $1 FOR UPDATE NOWAIT", table)
				if _, err := tree.pool.Exec(ctx, query, idOf[table]); err != nil {
					t.Errorf("%s: locked while the change waits above it: %v", table, err)
				}
			}

			if err := held.Rollback(ctx); err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			if err := <-done; err != nil {
				t.Errorf("Update after the lock was released: %v", err)
			}
		})
	}
}

func TestSectionService_DatabaseFailureIsNoneOfTheModulesErrors(t *testing.T) {
	tree := newSectionTree(t)
	section := createSection(t, tree, floor("Floor"))
	tree.pool.Close() // every query now fails; Close may be called again by the cleanup

	calls := map[string]func() error{
		"Get": func() error { _, err := tree.sections.Get(t.Context(), section.ID); return err },
		"Create": func() error {
			_, err := tree.sections.Create(t.Context(), tree.layout.ID, block("Balcony"))
			return err
		},
		"List": func() error { _, err := tree.sections.List(t.Context(), tree.layout.ID); return err },
		"Update": func() error {
			_, err := tree.sections.Update(t.Context(), section.ID, 1, func(*event.SectionInput) error { return nil })
			return err
		},
		"Delete": func() error { return tree.sections.Delete(t.Context(), section.ID, 1) },
	}
	moduleErrors := []error{
		event.ErrSectionNotFound, event.ErrSectionNameTaken, event.ErrLayoutNotFound,
		event.ErrLayoutNotDraft, event.ErrLayoutArchived, event.ErrHallArchived, event.ErrVenueArchived,
		event.ErrVersionMismatch,
	}
	for name, call := range calls {
		err := call()
		var invalid *event.ValidationError
		if err == nil || errors.As(err, &invalid) {
			t.Errorf("%s: got %v, want a database error", name, err)
			continue
		}
		for _, moduleErr := range moduleErrors {
			if errors.Is(err, moduleErr) {
				t.Errorf("%s: got %v, want a database error", name, err)
			}
		}
	}
}
