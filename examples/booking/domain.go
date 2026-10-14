package booking

import (
	"errors"
	"time"
)

var (
	ErrSeatAlreadyBooked = errors.New("seat already booked")
	ErrMissingSessionID  = errors.New("missing session id")
)

type Booking struct {
	ID        string // Uppercase means accessible within the package.
	MovieID   string // Lowercase means not accessible (like private in Java)
	SeatID    string
	UserID    string
	Status    string
	ExpiresAt time.Time
}

type BookingStore interface {
	Book(b Booking) error
	ListBookings(movieID string) []Booking
}
