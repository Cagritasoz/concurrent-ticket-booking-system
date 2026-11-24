package event_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
)

// The tests of HallService's reads and changes; Create's are in hall_service_test.go.

// newHall creates a venue (with fullInput) and one hall in it through the services, and returns
// the services and the hall.
func newHall(t *testing.T, name string) (*event.VenueService, *event.HallService, event.Hall) {
	t.Helper()
	pool := newPool(t)
	venues, halls := event.NewVenueService(pool), event.NewHallService(pool)
	hall := addHall(t, halls, createFull(t, venues).ID, name)
	return venues, halls, hall
}

// addHall creates a hall with this name in the venue.
func addHall(t *testing.T, service *event.HallService, venueID uuid.UUID, name string) event.Hall {
	t.Helper()
	hall, err := service.Create(t.Context(), venueID, event.HallInput{Name: name})
	if err != nil {
		t.Fatalf("Create %q: %v", name, err)
	}
	return hall
}

// archiveVenue archives the venue at its current version.
func archiveVenue(t *testing.T, venues *event.VenueService, venueID uuid.UUID) {
	t.Helper()
	venue, err := venues.Get(t.Context(), venueID)
	if err != nil {
		t.Fatalf("Get venue: %v", err)
	}
	if _, err := venues.Archive(t.Context(), venueID, venue.Version); err != nil {
		t.Fatalf("Archive venue: %v", err)
	}
}

// checkHallUnchanged fails the test unless the stored hall is still want.
func checkHallUnchanged(t *testing.T, service *event.HallService, want event.Hall) {
	t.Helper()
	got, err := service.Get(t.Context(), want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stored hall:\n got  %+v\n want %+v", got, want)
	}
}

// renameTo is a change function that sets the name.
func renameTo(name string) func(*event.HallInput) error {
	return func(in *event.HallInput) error { in.Name = name; return nil }
}

func hallNames(halls []event.Hall) []string {
	names := make([]string, len(halls))
	for i, h := range halls {
		names[i] = h.Name
	}
	return names
}

func TestHallServiceGet_ReturnsTheStoredHallOrErrHallNotFound(t *testing.T) {
	_, service, hall := newHall(t, "Studio")

	got, err := service.Get(t.Context(), hall.ID)
	if err != nil || !reflect.DeepEqual(got, hall) {
		t.Errorf("Get: got %+v, %v; want %+v", got, err, hall)
	}
	if _, err := service.Get(t.Context(), uuid.New()); !errors.Is(err, event.ErrHallNotFound) {
		t.Errorf("unknown id: got %v, want ErrHallNotFound", err)
	}
}

func TestHallServiceList_ReturnsTheVenuesHallsOfOneStatusByName(t *testing.T) {
	venues, service, first := newHall(t, "studio")
	venueID := first.VenueID
	addHall(t, service, venueID, "Screen 2")
	annex := addHall(t, service, venueID, "Annex")
	if _, err := service.Archive(t.Context(), annex.ID, annex.Version); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	addHall(t, service, createFull(t, venues).ID, "Other") // another venue's hall is never listed

	active, err := service.List(t.Context(), venueID, event.HallActive)
	if err != nil {
		t.Fatalf("List active: %v", err)
	}
	if got, want := hallNames(active), []string{"Screen 2", "studio"}; !reflect.DeepEqual(got, want) {
		t.Errorf("active: got %q, want %q (by name, ignoring case)", got, want)
	}
	archived, err := service.List(t.Context(), venueID, event.HallArchived)
	if err != nil {
		t.Fatalf("List archived: %v", err)
	}
	if got, want := hallNames(archived), []string{"Annex"}; !reflect.DeepEqual(got, want) {
		t.Errorf("archived: got %q, want %q", got, want)
	}

	// An archived venue's halls are still listed, by their own status.
	archiveVenue(t, venues, venueID)
	if active, err := service.List(t.Context(), venueID, event.HallActive); err != nil || len(active) != 2 {
		t.Errorf("archived venue: got %d halls, %v; want 2", len(active), err)
	}
}

func TestHallServiceList_NoHallsIsAnEmptySlice(t *testing.T) {
	pool := newPool(t)
	venue := createFull(t, event.NewVenueService(pool))

	halls, err := event.NewHallService(pool).List(t.Context(), venue.ID, event.HallActive)
	if err != nil || halls == nil || len(halls) != 0 {
		t.Errorf("got %#v, %v; want an empty, non-nil slice", halls, err)
	}
}

func TestHallServiceList_UnknownVenueOrStatusIsRefused(t *testing.T) {
	_, service, hall := newHall(t, "Studio")

	if _, err := service.List(t.Context(), uuid.New(), event.HallActive); !errors.Is(err, event.ErrVenueNotFound) {
		t.Errorf("unknown venue: got %v, want ErrVenueNotFound", err)
	}
	_, err := service.List(t.Context(), hall.VenueID, "deleted")
	var invalid *event.ValidationError
	if !errors.As(err, &invalid) || len(invalid.Fields) != 1 || invalid.Fields[0].Field != "status" {
		t.Errorf("unknown status: got %v, want a *ValidationError for status", err)
	}
}

func TestHallServiceUpdate_StoresTheChangedAndCleanedValues(t *testing.T) {
	_, service, hall := newHall(t, "Studio")

	updated, err := service.Update(t.Context(), hall.ID, hall.Version, func(in *event.HallInput) error {
		in.Name = " Studio A "
		in.Capacity = new(300)
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "Studio A" || updated.Capacity == nil || *updated.Capacity != 300 ||
		updated.Version != 2 || !updated.UpdatedAt.After(hall.UpdatedAt) {
		t.Errorf("got %+v; want name Studio A, capacity 300, version 2, a later updated_at", updated)
	}
	checkHallUnchanged(t, service, updated) // stored as returned
}

func TestHallServiceUpdate_RefusalsStoreNothing(t *testing.T) {
	venues, service, hall := newHall(t, "Studio")
	addHall(t, service, hall.VenueID, "Screen 1")
	errChange := errors.New("change refused")

	tests := []struct {
		name    string
		id      uuid.UUID
		version int
		change  func(*event.HallInput) error
		want    error
	}{
		{"unknown id", uuid.New(), 1, renameTo("X"), event.ErrHallNotFound},
		{"other version", hall.ID, 2, renameTo("X"), event.ErrVersionMismatch},
		{"name of another hall, ignoring case", hall.ID, 1, renameTo("SCREEN 1"), event.ErrHallNameTaken},
		{"error from change", hall.ID, 1, func(*event.HallInput) error { return errChange }, errChange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := service.Update(t.Context(), tt.id, tt.version, tt.change); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
		})
	}

	_, err := service.Update(t.Context(), hall.ID, 1, renameTo(" "))
	var invalid *event.ValidationError
	if !errors.As(err, &invalid) {
		t.Errorf("blank name: got %v, want a *ValidationError", err)
	}
	checkHallUnchanged(t, service, hall)

	// A hall of an archived venue counts as archived.
	archiveVenue(t, venues, hall.VenueID)
	if _, err := service.Update(t.Context(), hall.ID, 1, renameTo("X")); !errors.Is(err, event.ErrVenueArchived) {
		t.Errorf("archived venue: got %v, want ErrVenueArchived", err)
	}
	checkHallUnchanged(t, service, hall)
}

func TestHallServiceUpdate_ArchivedHallIsErrHallArchived(t *testing.T) {
	_, service, hall := newHall(t, "Studio")
	archived, err := service.Archive(t.Context(), hall.ID, hall.Version)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}

	if _, err := service.Update(t.Context(), hall.ID, archived.Version, renameTo("X")); !errors.Is(err, event.ErrHallArchived) {
		t.Errorf("got %v, want ErrHallArchived", err)
	}
	checkHallUnchanged(t, service, archived)
}

// Updates based on the same version at the same moment: the row lock makes each wait for the one
// before it, after which its version no longer matches. Exactly one is stored.
func TestHallServiceUpdate_ConcurrentUpdatesFromTheSameVersionHaveOneWinner(t *testing.T) {
	_, service, hall := newHall(t, "Studio")

	const n = 10
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_, errs[i] = service.Update(t.Context(), hall.ID, hall.Version, func(in *event.HallInput) error {
				in.Capacity = new(100 + i)
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
	stored, err := service.Get(t.Context(), hall.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if won != 1 || stored.Version != 2 {
		t.Errorf("%d updates succeeded and the version is %d; want 1 and 2", won, stored.Version)
	}
}

// A hall change must lock the venue before the hall. Deleting a venue locks the venue and then,
// through ON DELETE CASCADE, its halls; a change that locked the hall first and then waited for
// the venue would hold what the delete needs while waiting for what the delete holds: a deadlock.
//
// Racing the two only rarely hits the narrow moment where that happens, so this test makes it
// certain: another transaction holds the venue, as a delete in progress would; the change must
// then be waiting for the venue without holding the hall.
func TestHallServiceChange_LocksTheVenueBeforeTheHall(t *testing.T) {
	pool := newPool(t)
	ctx := t.Context()
	venue := createFull(t, event.NewVenueService(pool))
	service := event.NewHallService(pool)
	hall := addHall(t, service, venue.ID, "Studio")

	held, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = held.Rollback(ctx) }() // a no-op once rolled back below
	if _, err := held.Exec(ctx, "SELECT 1 FROM venues WHERE id = $1 FOR UPDATE", venue.ID); err != nil {
		t.Fatalf("lock venue: %v", err)
	}

	done := make(chan error, 1) // buffered, so the goroutine never blocks on sending
	go func() {
		_, err := service.Update(ctx, hall.ID, hall.Version, func(in *event.HallInput) error {
			in.Capacity = new(100)
			return nil
		})
		done <- err
	}()
	waitForALockWait(t, pool)

	// The change is waiting for the venue. NOWAIT fails at once (SQLSTATE 55P03) instead of
	// waiting if anyone holds the hall's row.
	if _, err := pool.Exec(ctx, "SELECT 1 FROM halls WHERE id = $1 FOR UPDATE NOWAIT", hall.ID); err != nil {
		t.Errorf("the hall is locked while the change waits for its venue: %v", err)
	}

	if err := held.Rollback(ctx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := <-done; err != nil {
		t.Errorf("Update after the venue was released: %v", err)
	}
}

// waitForALockWait returns once a connection to the test's database is waiting for a lock, and
// fails the test if none does within 5 seconds. pg_stat_activity lists every connection and
// what it is waiting for.
func waitForALockWait(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting)
		if err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no connection started waiting for a lock")
}

func TestHallServiceArchiveAndRestore_MoveTheStatusAndBumpTheVersion(t *testing.T) {
	_, service, hall := newHall(t, "Studio")

	archived, err := service.Archive(t.Context(), hall.ID, 1)
	if err != nil || archived.Status != event.HallArchived || archived.Version != 2 {
		t.Fatalf("Archive: got %+v, %v; want archived, version 2", archived, err)
	}
	restored, err := service.Restore(t.Context(), hall.ID, 2)
	if err != nil || restored.Status != event.HallActive || restored.Version != 3 {
		t.Fatalf("Restore: got %+v, %v; want active, version 3", restored, err)
	}
	checkHallUnchanged(t, service, restored)
}

func TestHallServiceArchiveAndRestore_RefusalsSayWhy(t *testing.T) {
	venues, service, hall := newHall(t, "Studio")
	annex := addHall(t, service, hall.VenueID, "Annex")
	archived, err := service.Archive(t.Context(), annex.ID, annex.Version)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}

	tests := []struct {
		name    string
		op      func(context.Context, uuid.UUID, int) (event.Hall, error)
		id      uuid.UUID
		version int
		want    error
	}{
		{"archive unknown", service.Archive, uuid.New(), 1, event.ErrHallNotFound},
		{"archive other version", service.Archive, hall.ID, 7, event.ErrVersionMismatch},
		{"archive archived", service.Archive, archived.ID, archived.Version, event.ErrHallArchived},
		{"restore active", service.Restore, hall.ID, hall.Version, event.ErrHallNotArchived},
		{"restore other version", service.Restore, archived.ID, 1, event.ErrVersionMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.op(t.Context(), tt.id, tt.version); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
		})
	}

	// In an archived venue neither is allowed: restore the venue first.
	archiveVenue(t, venues, hall.VenueID)
	if _, err := service.Archive(t.Context(), hall.ID, hall.Version); !errors.Is(err, event.ErrVenueArchived) {
		t.Errorf("archive in an archived venue: got %v, want ErrVenueArchived", err)
	}
	if _, err := service.Restore(t.Context(), archived.ID, archived.Version); !errors.Is(err, event.ErrVenueArchived) {
		t.Errorf("restore in an archived venue: got %v, want ErrVenueArchived", err)
	}
	checkHallUnchanged(t, service, hall)
	checkHallUnchanged(t, service, archived)
}

// While a hall is archived its name is free. Restoring it after another hall took the name is
// refused with ErrHallNameTaken, not a database error (a 500).
func TestHallServiceRestore_NameTakenMeanwhileIsErrHallNameTaken(t *testing.T) {
	_, service, hall := newHall(t, "Studio")
	archived, err := service.Archive(t.Context(), hall.ID, hall.Version)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	addHall(t, service, hall.VenueID, "studio")

	if _, err := service.Restore(t.Context(), hall.ID, archived.Version); !errors.Is(err, event.ErrHallNameTaken) {
		t.Errorf("got %v, want ErrHallNameTaken", err)
	}
	checkHallUnchanged(t, service, archived)
}

func TestHallServiceDelete(t *testing.T) {
	venues, service, hall := newHall(t, "Studio")
	annex := addHall(t, service, hall.VenueID, "Annex")

	if err := service.Delete(t.Context(), uuid.New(), 1); !errors.Is(err, event.ErrHallNotFound) {
		t.Errorf("unknown id: got %v, want ErrHallNotFound", err)
	}
	if err := service.Delete(t.Context(), hall.ID, 2); !errors.Is(err, event.ErrVersionMismatch) {
		t.Errorf("other version: got %v, want ErrVersionMismatch", err)
	}
	checkHallUnchanged(t, service, hall)

	if err := service.Delete(t.Context(), hall.ID, hall.Version); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := service.Get(t.Context(), hall.ID); !errors.Is(err, event.ErrHallNotFound) {
		t.Errorf("after delete: got %v, want ErrHallNotFound", err)
	}

	// A hall of an archived venue can still be deleted, as an archived hall can.
	archiveVenue(t, venues, hall.VenueID)
	if err := service.Delete(t.Context(), annex.ID, annex.Version); err != nil {
		t.Errorf("delete in an archived venue: %v", err)
	}
}

func TestHallServiceReadsAndChanges_DatabaseFailureIsNoneOfTheModulesErrors(t *testing.T) {
	pool := newPool(t)
	service := event.NewHallService(pool)
	pool.Close() // every query now fails; Close may be called again by the cleanup

	id := uuid.New()
	_, getErr := service.Get(t.Context(), id)
	_, listErr := service.List(t.Context(), id, event.HallActive)
	_, updateErr := service.Update(t.Context(), id, 1, renameTo("X"))
	_, archiveErr := service.Archive(t.Context(), id, 1)
	deleteErr := service.Delete(t.Context(), id, 1)

	results := map[string]error{"Get": getErr, "List": listErr, "Update": updateErr, "Archive": archiveErr, "Delete": deleteErr}
	for name, err := range results {
		if err == nil || errors.Is(err, event.ErrHallNotFound) || errors.Is(err, event.ErrVenueNotFound) ||
			errors.Is(err, event.ErrVersionMismatch) {
			t.Errorf("%s: got %v, want a database error", name, err)
		}
	}
}
