package event

import (
	"errors"
	"time"
	"uuid"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event/eventdb"
)

// The hall's errors. httpapi turns ErrHallNotFound into 404 Not Found and the others into 409
// Conflict.
var (
	// ErrHallNotFound: no hall has the requested id.
	ErrHallNotFound = errors.New("hall not found")
	// ErrHallArchived: the hall is archived, so it can't be changed or archived again; restore
	// it first.
	ErrHallArchived = errors.New("hall is archived")
	// ErrHallNotArchived: the hall is active, so there is nothing to restore.
	ErrHallNotArchived = errors.New("hall is not archived")
	// ErrHallNameTaken: another hall of the venue that isn't archived already has this name,
	// ignoring case ("Screen 1" and "screen 1" are the same name).
	ErrHallNameTaken = errors.New("hall name is taken")
)

// HallStatus is where a hall is in its life, the same two statuses as a venue's. Halls are
// created active.
//
//	active -> Archive -> archived -> Restore -> active
//
// A hall of an archived venue counts as archived whatever its own status says: it can be read,
// listed and deleted, but not changed, archived or restored until the venue is restored (the
// service answers ErrVenueArchived). Its own status isn't written when the venue is archived, so
// restoring the venue brings back exactly the halls that were active before.
type HallStatus string

const (
	HallActive   HallStatus = "active"
	HallArchived HallStatus = "archived"
)

// Hall is a room of a venue (a cinema's Screen 1, a convention center's Hall A). A venue with one
// room has one hall. Like Venue, it is filled from its table's row by toHall.
type Hall struct {
	ID          uuid.UUID
	VenueID     uuid.UUID // a hall never moves to another venue
	Name        string
	Description *string // e.g. which entrance leads to it
	Capacity    *int    // the room's maximum occupancy; nil if it isn't known
	Status      HallStatus
	Version     int // as Venue.Version
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// toHall converts a row of the halls table into a Hall.
func toHall(row eventdb.Hall) Hall {
	h := Hall{
		ID:          row.ID,
		VenueID:     row.VenueID,
		Name:        row.Name,
		Description: row.Description,
		Status:      HallStatus(row.Status), // the table's CHECK allows only our two values
		Version:     int(row.Version),
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if row.Capacity != nil {
		h.Capacity = new(int(*row.Capacity)) // int32 → int always fits
	}
	return h
}
