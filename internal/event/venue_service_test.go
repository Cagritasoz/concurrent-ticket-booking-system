package event_test

import (
	"errors"
	"reflect"
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

// insertVenue stores a venue directly with the generated query, because the service can't
// create venues yet. It returns the stored row.
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
