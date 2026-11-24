package event_test

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
)

// countHalls returns how many halls are stored, for the tests that check nothing was.
func countHalls(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM halls").Scan(&count); err != nil {
		t.Fatalf("count halls: %v", err)
	}
	return count
}

func TestHallServiceCreate_StoresTheCleanedInput(t *testing.T) {
	pool := newPool(t)
	venue := createFull(t, event.NewVenueService(pool))
	service := event.NewHallService(pool)

	created, err := service.Create(t.Context(), venue.ID, event.HallInput{
		Name:        "  Turkcell Stage ",
		Description: new(" Entrance through Gate B. "),
		Capacity:    new(2500),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	want := event.Hall{
		ID:          created.ID,
		VenueID:     venue.ID,
		Name:        "Turkcell Stage",
		Description: new("Entrance through Gate B."),
		Capacity:    new(2500),
		Status:      event.HallActive,
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

func TestHallServiceCreate_OptionalFieldsCanBeLeftOut(t *testing.T) {
	pool := newPool(t)
	venue := createFull(t, event.NewVenueService(pool))

	// A blank description is the same as none, as for venues.
	created, err := event.NewHallService(pool).Create(t.Context(), venue.ID,
		event.HallInput{Name: "Studio", Description: new("   ")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Description != nil || created.Capacity != nil {
		t.Errorf("description %v, capacity %v: want both nil", created.Description, created.Capacity)
	}
}

func TestHallServiceCreate_InvalidInputIsAValidationErrorAndStoresNothing(t *testing.T) {
	pool := newPool(t)
	venue := createFull(t, event.NewVenueService(pool))
	service := event.NewHallService(pool)
	long := func(n int) *string { return new(strings.Repeat("a", n)) }

	tests := []struct {
		name string
		in   event.HallInput
		want []event.FieldError
	}{
		{"blank name", event.HallInput{Name: "  "},
			[]event.FieldError{{Field: "name", Message: "is required"}}},
		{"capacity zero", event.HallInput{Name: "Studio", Capacity: new(0)},
			[]event.FieldError{{Field: "capacity", Message: "must be between 1 and 2147483647"}}},
		{"capacity above the column's range", event.HallInput{Name: "Studio", Capacity: new(math.MaxInt32 + 1)},
			[]event.FieldError{{Field: "capacity", Message: "must be between 1 and 2147483647"}}},
		{"every field wrong at once", event.HallInput{Name: "", Description: long(5001), Capacity: new(-1)},
			[]event.FieldError{
				{Field: "name", Message: "is required"},
				{Field: "description", Message: "must be at most 5000 characters"},
				{Field: "capacity", Message: "must be between 1 and 2147483647"},
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.Create(t.Context(), venue.ID, tt.in)

			var invalid *event.ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("got %v, want a *event.ValidationError", err)
			}
			if !reflect.DeepEqual(invalid.Fields, tt.want) {
				t.Errorf("problems:\ngot  %v\nwant %v", invalid.Fields, tt.want)
			}
		})
	}
	if n := countHalls(t, pool); n != 0 {
		t.Errorf("halls stored: got %d, want 0", n)
	}
}

func TestHallServiceCreate_InvalidInputIsReportedBeforeTheVenueIsLookedUp(t *testing.T) {
	service := event.NewHallService(newPool(t))

	_, err := service.Create(t.Context(), uuid.New(), event.HallInput{}) // unknown venue and no name

	var invalid *event.ValidationError
	if !errors.As(err, &invalid) {
		t.Errorf("got %v, want a *event.ValidationError", err)
	}
}

func TestHallServiceCreate_UnknownVenueIsErrVenueNotFound(t *testing.T) {
	pool := newPool(t)

	_, err := event.NewHallService(pool).Create(t.Context(), uuid.New(), event.HallInput{Name: "Studio"})

	if !errors.Is(err, event.ErrVenueNotFound) {
		t.Errorf("got %v, want ErrVenueNotFound", err)
	}
	if n := countHalls(t, pool); n != 0 {
		t.Errorf("halls stored: got %d, want 0", n)
	}
}

func TestHallServiceCreate_ArchivedVenueIsErrVenueArchived(t *testing.T) {
	pool := newPool(t)
	venues := event.NewVenueService(pool)
	venue := createFull(t, venues)
	if _, err := venues.Archive(t.Context(), venue.ID, venue.Version); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	_, err := event.NewHallService(pool).Create(t.Context(), venue.ID, event.HallInput{Name: "Studio"})

	if !errors.Is(err, event.ErrVenueArchived) {
		t.Errorf("got %v, want ErrVenueArchived", err)
	}
	if n := countHalls(t, pool); n != 0 {
		t.Errorf("halls stored: got %d, want 0", n)
	}
}

func TestHallServiceCreate_NameTakenInTheVenueIsErrHallNameTaken(t *testing.T) {
	pool := newPool(t)
	venues := event.NewVenueService(pool)
	venue := createFull(t, venues)
	service := event.NewHallService(pool)
	if _, err := service.Create(t.Context(), venue.ID, event.HallInput{Name: "Screen 1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err := service.Create(t.Context(), venue.ID, event.HallInput{Name: "SCREEN 1"}) // case is ignored
	if !errors.Is(err, event.ErrHallNameTaken) {
		t.Errorf("same name in the same venue: got %v, want ErrHallNameTaken", err)
	}

	// Another venue may use the name.
	other, err := venues.Create(t.Context(), fullInput())
	if err != nil {
		t.Fatalf("create the other venue: %v", err)
	}
	if _, err := service.Create(t.Context(), other.ID, event.HallInput{Name: "Screen 1"}); err != nil {
		t.Errorf("same name in another venue: %v, want it stored", err)
	}
}

// Many requests create a hall with the same name at the same moment. Each passes the venue check;
// the unique index lets exactly one of them insert, and every other one gets ErrHallNameTaken,
// not a database error (a 500).
func TestHallServiceCreate_ConcurrentCreatesWithOneNameHaveOneWinner(t *testing.T) {
	pool := newPool(t)
	venue := createFull(t, event.NewVenueService(pool))
	service := event.NewHallService(pool)

	const n = 10
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_, errs[i] = service.Create(t.Context(), venue.ID, event.HallInput{Name: "Screen 1"})
		})
	}
	wg.Wait()

	var won int
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, event.ErrHallNameTaken):
			t.Errorf("got %v, want nil or ErrHallNameTaken", err)
		}
	}
	if won != 1 {
		t.Errorf("%d creates succeeded, want 1", won)
	}
	if got := countHalls(t, pool); got != 1 {
		t.Errorf("halls stored: got %d, want 1", got)
	}
}

func TestHallServiceCreate_DatabaseFailureIsNoneOfTheModulesErrors(t *testing.T) {
	pool := newPool(t)
	venue := createFull(t, event.NewVenueService(pool))
	service := event.NewHallService(pool)
	pool.Close() // every query now fails; Close may be called again by the cleanup

	_, err := service.Create(t.Context(), venue.ID, event.HallInput{Name: "Studio"})

	var invalid *event.ValidationError
	if err == nil || errors.As(err, &invalid) || errors.Is(err, event.ErrVenueNotFound) ||
		errors.Is(err, event.ErrVenueArchived) || errors.Is(err, event.ErrHallNameTaken) {
		t.Errorf("got %v, want a database error", err)
	}
}
