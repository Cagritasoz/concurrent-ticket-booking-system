package event_test

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"uuid"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
)

// The tests of LayoutService's reads and changes; Create's are in layout_service_test.go.

// newLayout creates the tree of newLayoutTree and one draft layout in its hall.
func newLayout(t *testing.T, name string) (layoutTree, event.Layout) {
	t.Helper()
	tree := newLayoutTree(t)
	return tree, addLayout(t, tree.layouts, tree.hall.ID, name)
}

// addSection stores a standing section in the layout. There is no section service yet (M1 step
// 6d), so it is plain SQL.
func addSection(t *testing.T, tree layoutTree, layoutID uuid.UUID, name string) {
	t.Helper()
	_, err := tree.pool.Exec(t.Context(),
		`INSERT INTO sections (layout_id, name, kind, capacity, position) VALUES ($1, $2, 'standing', 100, 1)`,
		layoutID, name)
	if err != nil {
		t.Fatalf("add section %q: %v", name, err)
	}
}

// checkLayoutUnchanged fails the test unless the stored layout is still want.
func checkLayoutUnchanged(t *testing.T, service *event.LayoutService, want event.Layout) {
	t.Helper()
	got, err := service.Get(t.Context(), want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stored layout:\n got  %+v\n want %+v", got, want)
	}
}

// renameLayoutTo is a change function that sets the name.
func renameLayoutTo(name string) func(*event.LayoutInput) error {
	return func(in *event.LayoutInput) error { in.Name = name; return nil }
}

func layoutNames(layouts []event.Layout) []string {
	names := make([]string, len(layouts))
	for i, l := range layouts {
		names[i] = l.Name
	}
	return names
}

func TestLayoutServiceGet_ReturnsTheStoredLayoutOrErrLayoutNotFound(t *testing.T) {
	tree, layout := newLayout(t, "Concert")

	got, err := tree.layouts.Get(t.Context(), layout.ID)
	if err != nil || !reflect.DeepEqual(got, layout) {
		t.Errorf("Get: got %+v, %v; want %+v", got, err, layout)
	}
	if _, err := tree.layouts.Get(t.Context(), uuid.New()); !errors.Is(err, event.ErrLayoutNotFound) {
		t.Errorf("unknown id: got %v, want ErrLayoutNotFound", err)
	}
}

func TestLayoutServiceList_ReturnsTheHallsLayoutsOfOneStatusByName(t *testing.T) {
	tree, first := newLayout(t, "theatre setup")
	hallID := tree.hall.ID
	addLayout(t, tree.layouts, hallID, "Concert")
	addLayout(t, tree.layouts, hallID, "basketball")
	ice := addLayout(t, tree.layouts, hallID, "Ice show")
	addSection(t, tree, ice.ID, "Rink side")
	if _, err := tree.layouts.Activate(t.Context(), ice.ID, ice.Version); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	old := addLayout(t, tree.layouts, hallID, "Old stage")
	if _, err := tree.layouts.Archive(t.Context(), old.ID, old.Version); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	other := addHall(t, tree.halls, tree.hall.VenueID, "Second Hall")
	addLayout(t, tree.layouts, other.ID, "Arena") // another hall's layout is never listed

	for status, want := range map[event.LayoutStatus][]string{
		event.LayoutDraft:    {"basketball", "Concert", first.Name},
		event.LayoutActive:   {"Ice show"},
		event.LayoutArchived: {"Old stage"},
	} {
		layouts, err := tree.layouts.List(t.Context(), hallID, status)
		if err != nil {
			t.Fatalf("List %s: %v", status, err)
		}
		if got := layoutNames(layouts); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %q, want %q", status, got, want)
		}
	}

	// An archived hall's layouts are still listed, by their own status.
	archiveHall(t, tree.halls, tree.hall)
	if drafts, err := tree.layouts.List(t.Context(), hallID, event.LayoutDraft); err != nil || len(drafts) != 3 {
		t.Errorf("archived hall: got %d drafts, %v; want 3", len(drafts), err)
	}
}

func TestLayoutServiceList_NoLayoutsIsAnEmptySlice(t *testing.T) {
	tree := newLayoutTree(t)

	layouts, err := tree.layouts.List(t.Context(), tree.hall.ID, event.LayoutDraft)
	if err != nil || layouts == nil || len(layouts) != 0 {
		t.Errorf("got %#v, %v; want an empty slice, not nil", layouts, err)
	}
}

func TestLayoutServiceList_Refusals(t *testing.T) {
	tree := newLayoutTree(t)

	_, err := tree.layouts.List(t.Context(), tree.hall.ID, "frozen")
	var invalid *event.ValidationError
	want := []event.FieldError{{Field: "status", Message: "must be draft, active or archived"}}
	if !errors.As(err, &invalid) || !reflect.DeepEqual(invalid.Fields, want) {
		t.Errorf("unknown status: got %v, want a *ValidationError with %v", err, want)
	}
	if _, err := tree.layouts.List(t.Context(), uuid.New(), event.LayoutDraft); !errors.Is(err, event.ErrHallNotFound) {
		t.Errorf("unknown hall: got %v, want ErrHallNotFound", err)
	}
}

func TestLayoutServiceUpdate_StoresTheCleanedValuesAndBumpsTheVersion(t *testing.T) {
	tree, layout := newLayout(t, "Concert")

	updated, err := tree.layouts.Update(t.Context(), layout.ID, 1, func(in *event.LayoutInput) error {
		in.Name = "  Concert – standing floor "
		in.Description = new(" Floor seats removed. ")
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "Concert – standing floor" || updated.Description == nil ||
		*updated.Description != "Floor seats removed." || updated.Version != 2 ||
		updated.Status != event.LayoutDraft || !updated.UpdatedAt.After(layout.UpdatedAt) {
		t.Errorf("got %+v; want the cleaned values, still a draft, version 2, a later updated_at", updated)
	}
	checkLayoutUnchanged(t, tree.layouts, updated) // stored as returned

	// An active layout's labels can be changed too.
	addSection(t, tree, layout.ID, "Floor")
	active, err := tree.layouts.Activate(t.Context(), layout.ID, updated.Version)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if renamed, err := tree.layouts.Update(t.Context(), layout.ID, active.Version, renameLayoutTo("Concert 2027")); err != nil ||
		renamed.Status != event.LayoutActive {
		t.Errorf("rename the active layout: got %+v, %v; want it renamed and still active", renamed, err)
	}
}

func TestLayoutServiceUpdate_RefusalsStoreNothing(t *testing.T) {
	tree, layout := newLayout(t, "Concert")
	addLayout(t, tree.layouts, tree.hall.ID, "Basketball")
	errChange := errors.New("change refused")

	tests := []struct {
		name    string
		id      uuid.UUID
		version int
		change  func(*event.LayoutInput) error
		want    error
	}{
		{"unknown id", uuid.New(), 1, renameLayoutTo("X"), event.ErrLayoutNotFound},
		{"other version", layout.ID, 2, renameLayoutTo("X"), event.ErrVersionMismatch},
		{"name of another layout, ignoring case", layout.ID, 1, renameLayoutTo("BASKETBALL"), event.ErrLayoutNameTaken},
		{"error from change", layout.ID, 1, func(*event.LayoutInput) error { return errChange }, errChange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tree.layouts.Update(t.Context(), tt.id, tt.version, tt.change); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
		})
	}

	_, err := tree.layouts.Update(t.Context(), layout.ID, 1, renameLayoutTo(" "))
	var invalid *event.ValidationError
	if !errors.As(err, &invalid) {
		t.Errorf("blank name: got %v, want a *ValidationError", err)
	}
	checkLayoutUnchanged(t, tree.layouts, layout)
}

func TestLayoutServiceUpdate_ArchivedLayoutHallOrVenueIsRefused(t *testing.T) {
	tests := []struct {
		name    string
		archive func(t *testing.T, tree layoutTree, layout event.Layout) event.Layout // returns the layout as it is now
		want    error
	}{
		{"archived layout", func(t *testing.T, tree layoutTree, layout event.Layout) event.Layout {
			archived, err := tree.layouts.Archive(t.Context(), layout.ID, layout.Version)
			if err != nil {
				t.Fatalf("Archive: %v", err)
			}
			return archived
		}, event.ErrLayoutArchived},
		{"archived hall", func(t *testing.T, tree layoutTree, layout event.Layout) event.Layout {
			archiveHall(t, tree.halls, tree.hall)
			return layout
		}, event.ErrHallArchived},
		{"archived venue", func(t *testing.T, tree layoutTree, layout event.Layout) event.Layout {
			archiveVenue(t, tree.venues, tree.hall.VenueID)
			return layout
		}, event.ErrVenueArchived},
		{"archived hall and venue", func(t *testing.T, tree layoutTree, layout event.Layout) event.Layout {
			archiveHall(t, tree.halls, tree.hall)
			archiveVenue(t, tree.venues, tree.hall.VenueID)
			return layout
		}, event.ErrVenueArchived},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree, layout := newLayout(t, "Concert")
			layout = tt.archive(t, tree, layout)

			if _, err := tree.layouts.Update(t.Context(), layout.ID, layout.Version, renameLayoutTo("X")); !errors.Is(err, tt.want) {
				t.Errorf("current version: got %v, want %v", err, tt.want)
			}
			checkLayoutUnchanged(t, tree.layouts, layout)
		})
	}

	// An archived hall or venue is the answer even with an old version: reading the layout
	// again wouldn't help. The layout's own archived status comes after the version, since
	// archiving the layout changed it.
	tree, layout := newLayout(t, "Concert")
	archiveHall(t, tree.halls, tree.hall)
	if _, err := tree.layouts.Update(t.Context(), layout.ID, 9, renameLayoutTo("X")); !errors.Is(err, event.ErrHallArchived) {
		t.Errorf("archived hall, other version: got %v, want ErrHallArchived", err)
	}
}

// Ten admins change the same version at once. The layout's row lock lets them in one at a time,
// and the first one's version bump makes every other one a version mismatch.
func TestLayoutServiceUpdate_ConcurrentUpdatesOfOneVersionHaveOneWinner(t *testing.T) {
	tree, layout := newLayout(t, "Concert")

	const n = 10
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_, errs[i] = tree.layouts.Update(t.Context(), layout.ID, layout.Version, func(in *event.LayoutInput) error {
				in.Description = new("changed")
				return nil
			})
		})
	}
	wg.Wait()

	var won int
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, event.ErrVersionMismatch):
			t.Errorf("got %v, want nil or ErrVersionMismatch", err)
		}
	}
	stored, err := tree.layouts.Get(t.Context(), layout.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if won != 1 || stored.Version != 2 {
		t.Errorf("%d updates succeeded and the version is %d; want 1 and 2", won, stored.Version)
	}
}

// A layout change must lock the venue, then the hall, then the layout, for the reason given at
// TestHallServiceChange_LocksTheVenueBeforeTheHall: deleting a venue or a hall locks it and then,
// through ON DELETE CASCADE, everything below it. Here another transaction holds one level, as a
// delete in progress would; the change must then be waiting for it without holding anything
// below it.
func TestLayoutServiceChange_LocksTheTreeFromTheTop(t *testing.T) {
	tests := []struct {
		held string   // the table whose row the other transaction holds
		free []string // the tables below it, whose rows must be free while the change waits
	}{
		{"venues", []string{"halls", "layouts"}},
		{"halls", []string{"layouts"}},
	}
	for _, tt := range tests {
		t.Run(tt.held+" held", func(t *testing.T) {
			tree, layout := newLayout(t, "Concert")
			ctx := t.Context()
			idOf := map[string]uuid.UUID{"venues": tree.hall.VenueID, "halls": tree.hall.ID, "layouts": layout.ID}

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

			done := make(chan error, 1) // buffered, so the goroutine never blocks on sending
			go func() {
				_, err := tree.layouts.Update(ctx, layout.ID, layout.Version, renameLayoutTo("Concert 2027"))
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

func TestLayoutServiceActivate_NeedsADraftWithASection(t *testing.T) {
	tree, layout := newLayout(t, "Concert")

	if _, err := tree.layouts.Activate(t.Context(), layout.ID, 1); !errors.Is(err, event.ErrLayoutHasNoSections) {
		t.Errorf("no sections: got %v, want ErrLayoutHasNoSections", err)
	}
	checkLayoutUnchanged(t, tree.layouts, layout)

	addSection(t, tree, layout.ID, "Floor")
	active, err := tree.layouts.Activate(t.Context(), layout.ID, 1)
	if err != nil || active.Status != event.LayoutActive || active.Version != 2 {
		t.Fatalf("Activate: got %+v, %v; want active, version 2", active, err)
	}
	checkLayoutUnchanged(t, tree.layouts, active)

	if _, err := tree.layouts.Activate(t.Context(), layout.ID, 2); !errors.Is(err, event.ErrLayoutAlreadyActive) {
		t.Errorf("activate again: got %v, want ErrLayoutAlreadyActive", err)
	}
	if _, err := tree.layouts.Activate(t.Context(), layout.ID, 1); !errors.Is(err, event.ErrVersionMismatch) {
		t.Errorf("old version: got %v, want ErrVersionMismatch", err)
	}

	archived, err := tree.layouts.Archive(t.Context(), layout.ID, 2)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if _, err := tree.layouts.Activate(t.Context(), layout.ID, archived.Version); !errors.Is(err, event.ErrLayoutArchived) {
		t.Errorf("archived layout: got %v, want ErrLayoutArchived", err)
	}
	checkLayoutUnchanged(t, tree.layouts, archived)
}

// Deactivate takes an active layout back to a draft, so its sections can be changed; Activate then
// checks it again.
func TestLayoutServiceDeactivate_MovesAnActiveLayoutBackToADraft(t *testing.T) {
	tree, layout := newLayout(t, "Concert")
	addSection(t, tree, layout.ID, "Floor")

	if _, err := tree.layouts.Deactivate(t.Context(), layout.ID, 1); !errors.Is(err, event.ErrLayoutNotActive) {
		t.Errorf("deactivate a draft: got %v, want ErrLayoutNotActive", err)
	}
	checkLayoutUnchanged(t, tree.layouts, layout)

	active, err := tree.layouts.Activate(t.Context(), layout.ID, 1)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if _, err := tree.layouts.Deactivate(t.Context(), layout.ID, 1); !errors.Is(err, event.ErrVersionMismatch) {
		t.Errorf("old version: got %v, want ErrVersionMismatch", err)
	}
	draft, err := tree.layouts.Deactivate(t.Context(), layout.ID, active.Version)
	if err != nil || draft.Status != event.LayoutDraft || draft.Version != 3 {
		t.Fatalf("Deactivate: got %+v, %v; want a draft, version 3", draft, err)
	}
	checkLayoutUnchanged(t, tree.layouts, draft)

	// It can be activated again: Activate's checks run on the draft as on any other.
	if again, err := tree.layouts.Activate(t.Context(), layout.ID, draft.Version); err != nil || again.Status != event.LayoutActive {
		t.Errorf("activate again: got %+v, %v; want active", again, err)
	}

	archived := addLayout(t, tree.layouts, tree.hall.ID, "Old stage")
	archived, err = tree.layouts.Archive(t.Context(), archived.ID, archived.Version)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if _, err := tree.layouts.Deactivate(t.Context(), archived.ID, archived.Version); !errors.Is(err, event.ErrLayoutArchived) {
		t.Errorf("archived layout: got %v, want ErrLayoutArchived", err)
	}
	checkLayoutUnchanged(t, tree.layouts, archived)
}

// Archive works from a draft and from an active layout, and restore always brings it back as a
// draft, so it is offered for events again only after a deliberate Activate.
func TestLayoutServiceArchiveAndRestore_RestoreIsAlwaysADraft(t *testing.T) {
	tree, draft := newLayout(t, "Concert")
	active := addLayout(t, tree.layouts, tree.hall.ID, "Basketball")
	addSection(t, tree, active.ID, "Court side")
	active, err := tree.layouts.Activate(t.Context(), active.ID, active.Version)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}

	for _, layout := range []event.Layout{draft, active} {
		t.Run(string(layout.Status), func(t *testing.T) {
			archived, err := tree.layouts.Archive(t.Context(), layout.ID, layout.Version)
			if err != nil || archived.Status != event.LayoutArchived || archived.Version != layout.Version+1 {
				t.Fatalf("Archive: got %+v, %v; want archived, version %d", archived, err, layout.Version+1)
			}
			if _, err := tree.layouts.Archive(t.Context(), layout.ID, archived.Version); !errors.Is(err, event.ErrLayoutArchived) {
				t.Errorf("archive again: got %v, want ErrLayoutArchived", err)
			}

			restored, err := tree.layouts.Restore(t.Context(), layout.ID, archived.Version)
			if err != nil || restored.Status != event.LayoutDraft || restored.Version != archived.Version+1 {
				t.Fatalf("Restore: got %+v, %v; want a draft, version %d", restored, err, archived.Version+1)
			}
			if _, err := tree.layouts.Restore(t.Context(), layout.ID, restored.Version); !errors.Is(err, event.ErrLayoutNotArchived) {
				t.Errorf("restore a draft: got %v, want ErrLayoutNotArchived", err)
			}
			checkLayoutUnchanged(t, tree.layouts, restored)
		})
	}
}

// While a layout was archived, another one of the hall may have taken its name.
func TestLayoutServiceRestore_OntoANameTakenMeanwhileIsErrLayoutNameTaken(t *testing.T) {
	tree, layout := newLayout(t, "Concert")
	archived, err := tree.layouts.Archive(t.Context(), layout.ID, layout.Version)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	addLayout(t, tree.layouts, tree.hall.ID, "CONCERT")

	if _, err := tree.layouts.Restore(t.Context(), layout.ID, archived.Version); !errors.Is(err, event.ErrLayoutNameTaken) {
		t.Errorf("got %v, want ErrLayoutNameTaken", err)
	}
	checkLayoutUnchanged(t, tree.layouts, archived)
}

// In an archived hall or venue no status change is allowed: restore them first.
func TestLayoutServiceStatusChanges_InAnArchivedHallOrVenueAreRefused(t *testing.T) {
	tree, draft := newLayout(t, "Concert")
	addSection(t, tree, draft.ID, "Floor")
	old := addLayout(t, tree.layouts, tree.hall.ID, "Old stage")
	old, err := tree.layouts.Archive(t.Context(), old.ID, old.Version)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	active := addLayout(t, tree.layouts, tree.hall.ID, "Basketball")
	addSection(t, tree, active.ID, "Court side")
	if active, err = tree.layouts.Activate(t.Context(), active.ID, active.Version); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	check := func(t *testing.T, want error) {
		t.Helper()
		if _, err := tree.layouts.Activate(t.Context(), draft.ID, draft.Version); !errors.Is(err, want) {
			t.Errorf("activate: got %v, want %v", err, want)
		}
		if _, err := tree.layouts.Deactivate(t.Context(), active.ID, active.Version); !errors.Is(err, want) {
			t.Errorf("deactivate: got %v, want %v", err, want)
		}
		if _, err := tree.layouts.Archive(t.Context(), draft.ID, draft.Version); !errors.Is(err, want) {
			t.Errorf("archive: got %v, want %v", err, want)
		}
		if _, err := tree.layouts.Restore(t.Context(), old.ID, old.Version); !errors.Is(err, want) {
			t.Errorf("restore: got %v, want %v", err, want)
		}
		checkLayoutUnchanged(t, tree.layouts, draft)
		checkLayoutUnchanged(t, tree.layouts, active)
		checkLayoutUnchanged(t, tree.layouts, old)
	}

	archiveHall(t, tree.halls, tree.hall)
	check(t, event.ErrHallArchived)
	archiveVenue(t, tree.venues, tree.hall.VenueID)
	check(t, event.ErrVenueArchived)
}

func TestLayoutServiceDelete_DeletesTheExpectedVersionWithItsSections(t *testing.T) {
	tree, layout := newLayout(t, "Concert")
	addSection(t, tree, layout.ID, "Floor")

	if err := tree.layouts.Delete(t.Context(), layout.ID, 2); !errors.Is(err, event.ErrVersionMismatch) {
		t.Errorf("other version: got %v, want ErrVersionMismatch", err)
	}
	checkLayoutUnchanged(t, tree.layouts, layout)

	if err := tree.layouts.Delete(t.Context(), layout.ID, 1); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := tree.layouts.Get(t.Context(), layout.ID); !errors.Is(err, event.ErrLayoutNotFound) {
		t.Errorf("Get after delete: got %v, want ErrLayoutNotFound", err)
	}
	var sections int
	if err := tree.pool.QueryRow(t.Context(), "SELECT count(*) FROM sections").Scan(&sections); err != nil || sections != 0 {
		t.Errorf("sections after delete: got %d, %v; want 0", sections, err)
	}
	if err := tree.layouts.Delete(t.Context(), layout.ID, 1); !errors.Is(err, event.ErrLayoutNotFound) {
		t.Errorf("delete again: got %v, want ErrLayoutNotFound", err)
	}
}

// Delete is for what nothing uses, whatever the state of the layout, its hall or its venue.
func TestLayoutServiceDelete_WorksInAnArchivedHallAndVenue(t *testing.T) {
	tree, layout := newLayout(t, "Concert")
	archiveHall(t, tree.halls, tree.hall)
	archiveVenue(t, tree.venues, tree.hall.VenueID)

	if err := tree.layouts.Delete(t.Context(), layout.ID, layout.Version); err != nil {
		t.Errorf("delete in an archived hall and venue: %v", err)
	}
}

func TestLayoutService_DatabaseFailureIsNoneOfTheModulesErrors(t *testing.T) {
	tree, layout := newLayout(t, "Concert")
	tree.pool.Close() // every query now fails; Close may be called again by the cleanup

	calls := map[string]func() error{
		"Get": func() error { _, err := tree.layouts.Get(t.Context(), layout.ID); return err },
		"List": func() error {
			_, err := tree.layouts.List(t.Context(), tree.hall.ID, event.LayoutDraft)
			return err
		},
		"Update": func() error {
			_, err := tree.layouts.Update(t.Context(), layout.ID, 1, renameLayoutTo("X"))
			return err
		},
		"Activate": func() error { _, err := tree.layouts.Activate(t.Context(), layout.ID, 1); return err },
		"Deactivate": func() error {
			_, err := tree.layouts.Deactivate(t.Context(), layout.ID, 1)
			return err
		},
		"Archive": func() error { _, err := tree.layouts.Archive(t.Context(), layout.ID, 1); return err },
		"Restore": func() error { _, err := tree.layouts.Restore(t.Context(), layout.ID, 1); return err },
		"Delete":  func() error { return tree.layouts.Delete(t.Context(), layout.ID, 1) },
	}
	moduleErrors := []error{
		event.ErrLayoutNotFound, event.ErrLayoutArchived, event.ErrLayoutNotArchived,
		event.ErrLayoutAlreadyActive, event.ErrLayoutNotActive, event.ErrLayoutHasNoSections,
		event.ErrLayoutNameTaken,
		event.ErrHallNotFound, event.ErrHallArchived, event.ErrVenueArchived, event.ErrVersionMismatch,
	}
	for name, call := range calls {
		err := call()
		if err == nil {
			t.Errorf("%s: got nil, want a database error", name)
			continue
		}
		for _, moduleErr := range moduleErrors {
			if errors.Is(err, moduleErr) {
				t.Errorf("%s: got %v, want a database error", name, err)
			}
		}
	}
}
