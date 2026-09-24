package booking

type MemoryStore struct {
	bookings map[string]Booking
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		bookings: map[string]Booking{}, // Initialized empty map
	}
}

func (s *MemoryStore) Book(b Booking) error {

}

func (s *MemoryStore) ListBookings(movieID string) []Booking {

}
