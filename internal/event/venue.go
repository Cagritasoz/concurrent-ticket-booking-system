// Package event is the admin side of the system: venues so far; their layouts, events, ticket
// tiers and tickets follow in M1. Other packages (httpapi, later booking) use its services and
// types, never its generated eventdb package.
package event

import (
	"errors"
	"time"
	"uuid"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event/eventdb"
)

// ErrVenueNotFound is returned when no venue has the requested id. Callers check it with
// errors.Is; httpapi turns it into 404 Not Found.
var ErrVenueNotFound = errors.New("venue not found")

// Venue is a place where events happen, as the rest of the app sees it. It is filled from
// eventdb.Venue (one row of the venues table) by toVenue, so a change in the table changes
// toVenue, not every user of Venue. Optional values are pointers: nil means "not set".
type Venue struct {
	ID          uuid.UUID
	Name        string
	Description *string // visitor information: box office hours, parking, accessibility
	Address     Address
	Location    *Location // nil if the map position isn't known
	TimeZone    string    // IANA name, e.g. Europe/Istanbul
	Capacity    *int      // maximum occupancy, nil if not known
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Address is an international postal address, the same shape Stripe uses. Not every country has
// regions or postal codes, so those are optional.
type Address struct {
	Line1       string
	Line2       *string
	City        string
	Region      *string
	PostalCode  *string
	CountryCode string // ISO 3166-1 alpha-2, e.g. TR or DE
}

// Location is a map position. The table stores latitude and longitude in two columns that are
// set together (a CHECK makes sure); here they are one optional value, so "only one of them is
// set" can't even be written down.
type Location struct {
	Latitude  float64
	Longitude float64
}

// toVenue converts a row of the venues table into a Venue.
func toVenue(row eventdb.Venue) Venue {
	v := Venue{
		ID:          row.ID,
		Name:        row.Name,
		Description: row.Description,
		Address: Address{
			Line1:       row.AddressLine1,
			Line2:       row.AddressLine2,
			City:        row.City,
			Region:      row.Region,
			PostalCode:  row.PostalCode,
			CountryCode: row.CountryCode,
		},
		TimeZone:  row.Timezone,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
	// The CHECK venues_coordinates_together guarantees both are set or neither.
	if row.Latitude != nil && row.Longitude != nil {
		v.Location = &Location{Latitude: *row.Latitude, Longitude: *row.Longitude}
	}
	// The column is a Postgres integer (int32); Go code uses int unless there is a reason not to.
	// Every int32 fits in an int, so this conversion is always safe.
	if row.Capacity != nil {
		v.Capacity = new(int(*row.Capacity))
	}
	return v
}
