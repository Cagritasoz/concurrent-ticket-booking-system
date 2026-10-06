package booking

type MemoryStore struct { // MemoryStore implements BookingStore interface
	bookings map[string]Booking // Key is SeatID, value is the Booking struct, go maps are not thread safe.
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		bookings: map[string]Booking{}, // Initialized empty map, alternatively make function can be used.
	}
}

func (s *MemoryStore) Book(b Booking) error {

	if _, exists := s.bookings[b.SeatID]; exists {
		return ErrSeatAlreadyBooked
	}

	s.bookings[b.SeatID] = b

	return nil

}

func (s *MemoryStore) ListBookings(movieID string) []Booking {

	var result []Booking

	for _, b := range s.bookings {
		if b.MovieID == movieID {
			result = append(result, b)
		}
	}
	return result
}
