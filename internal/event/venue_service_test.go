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
		_,_ = fmt.Fprintf(&b, "  %s %s\n", v.Name, v.ID)
	}
	return b.String()
}
