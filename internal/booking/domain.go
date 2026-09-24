package booking

type Booking struct {
	ID      string // Uppercase means accessible within the package.
	MovieID string // Lowercase means not accessible (like private in Java)
	SeatID  string
	UserID  string
	Status  string
}

type BookingStore interface {
	Book(b Booking) error
	ListBookings(movieID string) []Booking
}
