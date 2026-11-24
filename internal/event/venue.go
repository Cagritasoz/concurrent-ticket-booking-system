// Package event is the admin side of the system: venues and their halls so far; layouts,
// events, ticket tiers and tickets follow in M1. Other packages (httpapi, later booking) use its services
// and types, never its generated eventdb package.
package event

import (
	"errors"
	"time"
	"uuid"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event/eventdb"
)

// The venue's errors. Callers check them with errors.Is; httpapi turns ErrVenueNotFound into
// 404 Not Found and the other two into 409 Conflict.
var (
	// ErrVenueNotFound: no venue has the requested id.
	ErrVenueNotFound = errors.New("venue not found")
	// ErrVenueArchived: the venue is archived, so it can't be changed or archived again; restore
	// it first.
	ErrVenueArchived = errors.New("venue is archived")
	// ErrVenueNotArchived: the venue is active, so there is nothing to restore.
	ErrVenueNotArchived = errors.New("venue is not archived")
)

// VenueStatus is where a venue is in its life. Venues are created active.
//
//	active -> Archive -> archived -> Restore -> active
//
// An archived venue stays in the database for history (once events exist, past events point at
// it) but is hidden from the default list and can't be changed. A venue nothing ever used can be
// deleted instead.
type VenueStatus string

const (
	VenueActive   VenueStatus = "active"
	VenueArchived VenueStatus = "archived"
)

// Venue is a building where events happen, as the rest of the app sees it. It is filled from
// eventdb.Venue (one row of the venues table) by toVenue, so a change in the table changes
// toVenue, not every user of Venue. Optional values are pointers: nil means "not set".
type Venue struct {
	ID          uuid.UUID
	Name        string
	Description *string // visitor information: box office hours, parking, accessibility
	Address     Address
	Location    *Location // nil if the map position isn't known
	TimeZone    string    // IANA name, e.g. Europe/Istanbul
	Status      VenueStatus
	// Version is 1 for a new venue and grows by 1 on every change. Every change names the
	// version it was based on, and is refused with ErrVersionMismatch if the venue has moved on.
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
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
		TimeZone: row.Timezone,
		Status:   VenueStatus(row.Status), // the table's CHECK allows only our two values
		// The column is a Postgres integer (int32); Go code uses int unless there is a reason not
		// to. Every int32 fits in an int, so this conversion is always safe.
		Version:   int(row.Version),
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
	// The CHECK venues_coordinates_together guarantees both are set or neither.
	if row.Latitude != nil && row.Longitude != nil {
		v.Location = &Location{Latitude: *row.Latitude, Longitude: *row.Longitude}
	}
	return v
}
