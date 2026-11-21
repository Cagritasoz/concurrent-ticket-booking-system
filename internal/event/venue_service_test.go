package event_test

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event/eventdb"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres/pgtest"
)

// newPool returns a pool on a fresh database with every migration applied.
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := postgres.NewPool(t.Context(), pgtest.NewMigratedDatabase(t))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close) // runs before pgtest drops the database
	return pool
}

// insertVenue stores a venue directly with the generated query, so the Get tests don't depend on
// Create. It returns the stored row.
func insertVenue(t *testing.T, pool *pgxpool.Pool, params eventdb.CreateVenueParams) eventdb.Venue {
	t.Helper()
	row, err := eventdb.New(pool).CreateVenue(t.Context(), params)
	if err != nil {
		t.Fatalf("CreateVenue: %v", err)
	}
	return row
}

func TestVenueServiceGet_ReturnsTheStoredVenue(t *testing.T) {
	pool := newPool(t)
	service := event.NewVenueService(pool)

	tests := []struct {
		name   string
		params eventdb.CreateVenueParams
		want   event.Venue // without ID and timestamps, which Postgres fills in
	}{
		{
			name: "every field set",
			params: eventdb.CreateVenueParams{
				Name:         "Volkswagen Arena",
				Description:  new("Box office opens two hours before each event."),
				AddressLine1: "Huzur Mah. Azerbaycan Cad. 1",
				AddressLine2: new("Gate B"),
				City:         "Istanbul",
				Region:       new("Sariyer"),
				PostalCode:   new("34396"),
				CountryCode:  "TR",
				Latitude:     new(41.108934),
				Longitude:    new(29.010547),
				Timezone:     "Europe/Istanbul",
				Capacity:     new(int32(5000)),
			},
			want: event.Venue{
				Name:        "Volkswagen Arena",
				Description: new("Box office opens two hours before each event."),
				Address: event.Address{
					Line1:       "Huzur Mah. Azerbaycan Cad. 1",
					Line2:       new("Gate B"),
					City:        "Istanbul",
					Region:      new("Sariyer"),
					PostalCode:  new("34396"),
					CountryCode: "TR",
				},
				Location: &event.Location{Latitude: 41.108934, Longitude: 29.010547},
				TimeZone: "Europe/Istanbul",
				Capacity: new(5000),
			},
		},
		{
			name: "only required fields: the optional ones are nil",
			params: eventdb.CreateVenueParams{
				Name:         "Club",
				AddressLine1: "Kemankes Cad. 5",
				City:         "Istanbul",
				CountryCode:  "TR",
				Timezone:     "Europe/Istanbul",
			},
			want: event.Venue{
				Name: "Club",
				Address: event.Address{
					Line1:       "Kemankes Cad. 5",
					City:        "Istanbul",
					CountryCode: "TR",
				},
				TimeZone: "Europe/Istanbul",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stored := insertVenue(t, pool, tt.params)

			got, err := service.Get(t.Context(), stored.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}

			want := tt.want
			want.ID, want.CreatedAt, want.UpdatedAt = stored.ID, stored.CreatedAt, stored.UpdatedAt
			// DeepEqual follows pointers and compares the values behind them, like
			// assertEquals on two objects with a field-by-field equals in Java.
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestVenueServiceGet_UnknownIDIsErrVenueNotFound(t *testing.T) {
	service := event.NewVenueService(newPool(t))

	_, err := service.Get(t.Context(), uuid.New())
	if !errors.Is(err, event.ErrVenueNotFound) {
		t.Errorf("got %v, want ErrVenueNotFound", err)
	}
}

func TestVenueServiceGet_DatabaseFailureIsNotNotFound(t *testing.T) {
	pool := newPool(t)
	service := event.NewVenueService(pool)
	pool.Close() // every query now fails; Close may be called again by the cleanup

	_, err := service.Get(t.Context(), uuid.New())
	if err == nil || errors.Is(err, event.ErrVenueNotFound) {
		t.Errorf("got %v, want a database error that is not ErrVenueNotFound", err)
	}
}

func TestVenueServiceCreate_StoresTheCleanedInput(t *testing.T) {
	service := event.NewVenueService(newPool(t))
	in := event.VenueInput{
		Name:        " Volkswagen Arena ",
		Description: new(""), // blank optional text is stored as NULL
		Address: event.Address{
			Line1:       "Huzur Mah. Azerbaycan Cad. 1",
			City:        "Istanbul",
			PostalCode:  new("34396"),
			CountryCode: "TR",
		},
		Location: &event.Location{Latitude: 41.108934, Longitude: 29.010547},
		TimeZone: "Europe/Istanbul",
		Capacity: new(5000),
	}

	created, err := service.Create(t.Context(), in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	want := event.Venue{
		ID:   created.ID,
		Name: "Volkswagen Arena",
		Address: event.Address{
			Line1:       "Huzur Mah. Azerbaycan Cad. 1",
			City:        "Istanbul",
			PostalCode:  new("34396"),
			CountryCode: "TR",
		},
		Location:  &event.Location{Latitude: 41.108934, Longitude: 29.010547},
		TimeZone:  "Europe/Istanbul",
		Capacity:  new(5000),
		CreatedAt: created.CreatedAt,
		UpdatedAt: created.CreatedAt, // a new venue has never been updated
	}
	if !reflect.DeepEqual(created, want) {
		t.Errorf("Create returned\n %+v\nwant\n %+v", created, want)
	}
	if created.ID == uuid.Nil() || created.CreatedAt.IsZero() {
		t.Errorf("id %v, created_at %v: want both filled in by Postgres", created.ID, created.CreatedAt)
	}

	// Read back separately: what Create returned is what was stored.
	got, err := service.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, created) {
		t.Errorf("Get returned\n %+v\nCreate returned\n %+v", got, created)
	}
}

func TestVenueServiceCreate_InvalidInputIsAValidationErrorAndStoresNothing(t *testing.T) {
	pool := newPool(t)
	service := event.NewVenueService(pool)

	_, err := service.Create(t.Context(), event.VenueInput{Name: "Arena"}) // no address, no time zone

	var invalid *event.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("got %v, want a *event.ValidationError", err)
	}
	if len(invalid.Fields) != 4 { // address.line1, address.city, address.country_code, time_zone
		t.Errorf("got %d problems, want 4: %v", len(invalid.Fields), invalid.Fields)
	}
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM venues").Scan(&count); err != nil {
		t.Fatalf("count venues: %v", err)
	}
	if count != 0 { // invalid should not be inserted.
		t.Errorf("venues stored: got %d, want 0", count)
	}
}

func TestVenueServiceCreate_DatabaseFailureIsNotAValidationError(t *testing.T) {
	pool := newPool(t)
	service := event.NewVenueService(pool)
	pool.Close() // every query now fails; Close may be called again by the cleanup

	_, err := service.Create(t.Context(), event.VenueInput{
		Name:     "Arena",
		Address:  event.Address{Line1: "Huzur Mah. 1", City: "Istanbul", CountryCode: "TR"},
		TimeZone: "Europe/Istanbul",
	})

	var invalid *event.ValidationError
	if err == nil || errors.As(err, &invalid) {
		t.Errorf("got %v, want a database error that is not a *event.ValidationError", err)
	}
}

// createVenues stores one venue per name through the service and returns them in the list's
// order: by name, then by id, the way Postgres compares them.
func createVenues(t *testing.T, service *event.VenueService, names ...string) []event.Venue {
	t.Helper()
	venues := make([]event.Venue, 0, len(names)) // 0 = length, len(names) = capacity.
	for _, name := range names {
		v, err := service.Create(t.Context(), event.VenueInput{
			Name:     name,
			Address:  event.Address{Line1: "Huzur Mah. 1", City: "Istanbul", CountryCode: "TR"},
			TimeZone: "Europe/Istanbul",
		})
		if err != nil {
			t.Fatalf("Create %q: %v", name, err)
		}
		venues = append(venues, v)
	}
	// Like a Comparator in Java: negative if a comes first. The names are plain ASCII words, so
	// Go's byte order and the database's collation agree on them.
	slices.SortFunc(venues, func(a, b event.Venue) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return a.ID.Compare(b.ID) // names are the same, compare ids
	})
	return venues
}

func TestVenueServiceList_PagesThroughEveryVenueInOrder(t *testing.T) {
	service := event.NewVenueService(newPool(t))
	// Two venues share a name, so the id decides their order, even across a page boundary.
	want := createVenues(t, service, "Hall", "Arena", "Club", "Arena", "Dome")

	// Walk the pages like a client: each request starts at the previous page's Next.
	var got []event.Venue
	var sizes []int
	var after *event.VenueCursor
	for {
		page, err := service.List(t.Context(), after, 2)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		got = append(got, page.Venues...)
		sizes = append(sizes, len(page.Venues))
		if page.Next == nil { // there is no next page
			break
		}
		after = page.Next
	}

	if !slices.Equal(sizes, []int{2, 2, 1}) {
		t.Errorf("page sizes: got %v, want [2 2 1]", sizes)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("venues in this order:\n%s\nwant:\n%s", names(got), names(want))
	}
}

// A last page that is exactly full has no Next, so the client isn't sent to an empty page.
func TestVenueServiceList_FullLastPageHasNoNext(t *testing.T) {
	service := event.NewVenueService(newPool(t))
	createVenues(t, service, "Arena", "Club")

	page, err := service.List(t.Context(), nil, 2)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Venues) != 2 || page.Next != nil {
		t.Errorf("got %d venues and Next %+v, want 2 venues and no Next", len(page.Venues), page.Next)
	}
}

func TestVenueServiceList_NoVenuesIsAnEmptyPage(t *testing.T) {
	service := event.NewVenueService(newPool(t))

	page, err := service.List(t.Context(), nil, event.DefaultVenuePageSize)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.Venues == nil || len(page.Venues) != 0 || page.Next != nil {
		t.Errorf("got %+v, want an empty, non-nil slice and no Next", page)
	}
}

// The cursor holds values, not a row number or a reference to the row, so it keeps working when
// the venue it was taken from is deleted.
func TestVenueServiceList_CursorSurvivesDeletingItsVenue(t *testing.T) {
	pool := newPool(t)
	service := event.NewVenueService(pool)
	venues := createVenues(t, service, "Arena", "Club", "Dome")

	first, err := service.List(t.Context(), nil, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := pool.Exec(t.Context(), "DELETE FROM venues WHERE id = $1", first.Venues[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	next, err := service.List(t.Context(), first.Next, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !reflect.DeepEqual(next.Venues, venues[1:]) {
		t.Errorf("got\n%s\nwant\n%s", names(next.Venues), names(venues[1:]))
	}
}

func TestVenueServiceList_LimitOutOfRangeIsAValidationError(t *testing.T) {
	service := event.NewVenueService(newPool(t))

	for _, limit := range []int{0, -1, event.MaxVenuePageSize + 1} {
		_, err := service.List(t.Context(), nil, limit)

		var invalid *event.ValidationError
		want := []event.FieldError{{Field: "limit", Message: "must be between 1 and 100"}}
		if !errors.As(err, &invalid) || !reflect.DeepEqual(invalid.Fields, want) {
			t.Errorf("limit %d: got %v, want a *event.ValidationError with %v", limit, err, want)
		}
	}
}

func TestVenueServiceList_DatabaseFailureIsNotAValidationError(t *testing.T) {
	pool := newPool(t)
	service := event.NewVenueService(pool)
	pool.Close() // every query now fails; Close may be called again by the cleanup

	_, err := service.List(t.Context(), nil, event.DefaultVenuePageSize)

	var invalid *event.ValidationError
	if err == nil || errors.As(err, &invalid) {
		t.Errorf("got %v, want a database error that is not a *event.ValidationError", err)
	}
}

// names lists venues as "name id" lines, for readable failure messages.
func names(venues []event.Venue) string {
	var b strings.Builder
	for _, v := range venues {
		_, _ = fmt.Fprintf(&b, "  %s %s\n", v.Name, v.ID)
	}
	return b.String()
}

// fullInput returns a valid input with every field set.
func fullInput() event.VenueInput {
	return event.VenueInput{
		Name:        "Volkswagen Arena",
		Description: new("Box office opens two hours before each event."),
		Address: event.Address{
			Line1:       "Huzur Mah. Azerbaycan Cad. 1",
			Line2:       new("Gate B"),
			City:        "Istanbul",
			Region:      new("Sariyer"),
			PostalCode:  new("34396"),
			CountryCode: "TR",
		},
		Location: &event.Location{Latitude: 41.108934, Longitude: 29.010547},
		TimeZone: "Europe/Istanbul",
		Capacity: new(5000),
	}
}

func TestVenueServiceUpdate_StoresTheChangedAndCleanedValues(t *testing.T) {
	service := event.NewVenueService(newPool(t))
	created, err := service.Create(t.Context(), fullInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := service.Update(t.Context(), created.ID, func(in *event.VenueInput) error {
		// change sees the current values...
		if in.Name != "Volkswagen Arena" || *in.Capacity != 5000 {
			t.Errorf("change got %+v, want the stored venue's values", *in)
		}
		// ...and edits some of them. The rest stay as they are.
		in.Name = "  Volkswagen Arena Istanbul "
		in.Description = nil
		in.Address.City = "Ankara"
		in.Capacity = new(6000)
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	want := created
	want.Name = "Volkswagen Arena Istanbul" // cleaned: trimmed
	want.Description = nil
	want.Address.City = "Ankara"
	want.Capacity = new(6000)
	want.UpdatedAt = updated.UpdatedAt
	if !reflect.DeepEqual(updated, want) {
		t.Errorf("Update returned\n %+v\nwant\n %+v", updated, want)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Errorf("updated_at %v is not after %v", updated.UpdatedAt, created.UpdatedAt)
	}
	got, err := service.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, updated) {
		t.Errorf("Get returned\n %+v\nUpdate returned\n %+v", got, updated)
	}
}

func TestVenueServiceUpdate_InvalidResultIsAValidationErrorAndStoresNothing(t *testing.T) {
	service := event.NewVenueService(newPool(t))
	created, err := service.Create(t.Context(), fullInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = service.Update(t.Context(), created.ID, func(in *event.VenueInput) error {
		in.Name = "Arena Istanbul" // valid, but must not be stored either
		in.TimeZone = "Mars/Olympus_Mons"
		in.Capacity = new(0)
		return nil
	})

	var invalid *event.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("got %v, want a *event.ValidationError", err)
	}
	want := []event.FieldError{
		{Field: "time_zone", Message: "must be an IANA time zone name, e.g. Europe/Istanbul"},
		{Field: "capacity", Message: "must be between 1 and 2147483647"},
	}
	if !reflect.DeepEqual(invalid.Fields, want) {
		t.Errorf("problems: got %v, want %v", invalid.Fields, want)
	}
	assertUnchanged(t, service, created)
}

func TestVenueServiceUpdate_ErrorFromChangeIsReturnedAndStoresNothing(t *testing.T) {
	service := event.NewVenueService(newPool(t))
	created, err := service.Create(t.Context(), fullInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	errStop := errors.New("stop")

	_, err = service.Update(t.Context(), created.ID, func(in *event.VenueInput) error {
		in.Name = "Arena Istanbul"
		return errStop
	})

	if !errors.Is(err, errStop) {
		t.Errorf("got %v, want the error change returned", err)
	}
	assertUnchanged(t, service, created)
}

func TestVenueServiceUpdate_UnknownIDIsErrVenueNotFound(t *testing.T) {
	service := event.NewVenueService(newPool(t))

	_, err := service.Update(t.Context(), uuid.New(), func(*event.VenueInput) error {
		t.Error("change was called for a venue that doesn't exist")
		return nil
	})

	if !errors.Is(err, event.ErrVenueNotFound) {
		t.Errorf("got %v, want ErrVenueNotFound", err)
	}
}

// Many updates at the same time, each adding 1 to the capacity it reads. Every one must see the
// result of the one before, which is what the row lock in Update guarantees. Without it, two
// updates could read the same capacity, and one increment would be lost.
func TestVenueServiceUpdate_ConcurrentUpdatesLoseNothing(t *testing.T) {
	service := event.NewVenueService(newPool(t))
	in := fullInput()
	in.Capacity = new(1000)
	created, err := service.Create(t.Context(), in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const updates = 20
	errs := make(chan error, updates) // buffered, so no goroutine blocks on sending
	for range updates {
		go func() {
			_, err := service.Update(t.Context(), created.ID, func(in *event.VenueInput) error {
				in.Capacity = new(*in.Capacity + 1)
				return nil
			})
			errs <- err
		}()
	}
	for range updates {
		if err := <-errs; err != nil {
			t.Errorf("Update: %v", err)
		}
	}

	got, err := service.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if *got.Capacity != 1000+updates {
		t.Errorf("capacity: got %d, want %d (%d updates lost)", *got.Capacity, 1000+updates, 1000+updates-*got.Capacity)
	}
}

func TestVenueServiceUpdate_DatabaseFailureIsNeitherNotFoundNorInvalid(t *testing.T) {
	pool := newPool(t)
	service := event.NewVenueService(pool)
	pool.Close() // every query now fails; Close may be called again by the cleanup

	_, err := service.Update(t.Context(), uuid.New(), func(*event.VenueInput) error { return nil })

	var invalid *event.ValidationError
	if err == nil || errors.Is(err, event.ErrVenueNotFound) || errors.As(err, &invalid) {
		t.Errorf("got %v, want a database error", err)
	}
}

func TestVenueServiceDelete_RemovesTheVenue(t *testing.T) {
	service := event.NewVenueService(newPool(t))
	created, err := service.Create(t.Context(), fullInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := service.Delete(t.Context(), created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := service.Get(t.Context(), created.ID); !errors.Is(err, event.ErrVenueNotFound) {
		t.Errorf("Get after Delete: got %v, want ErrVenueNotFound", err)
	}
	if err := service.Delete(t.Context(), created.ID); !errors.Is(err, event.ErrVenueNotFound) {
		t.Errorf("second Delete: got %v, want ErrVenueNotFound", err)
	}
}

func TestVenueServiceDelete_UnknownIDIsErrVenueNotFound(t *testing.T) {
	service := event.NewVenueService(newPool(t))

	if err := service.Delete(t.Context(), uuid.New()); !errors.Is(err, event.ErrVenueNotFound) {
		t.Errorf("got %v, want ErrVenueNotFound", err)
	}
}

func TestVenueServiceDelete_DatabaseFailureIsNotNotFound(t *testing.T) {
	pool := newPool(t)
	service := event.NewVenueService(pool)
	pool.Close() // every query now fails; Close may be called again by the cleanup

	err := service.Delete(t.Context(), uuid.New())
	if err == nil || errors.Is(err, event.ErrVenueNotFound) {
		t.Errorf("got %v, want a database error that is not ErrVenueNotFound", err)
	}
}

// assertUnchanged fails the test unless the stored venue is still want.
func assertUnchanged(t *testing.T, service *event.VenueService, want event.Venue) {
	t.Helper()
	got, err := service.Get(t.Context(), want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the venue changed:\n got  %+v\n want %+v", got, want)
	}
}
