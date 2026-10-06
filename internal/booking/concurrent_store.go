package booking

import "sync"

type ConcurrentStore struct {
	bookings map[string]Booking

	// NO name, just a type → "embedded field"
	// With embedded fields go does two things:
	// The field is still created. Its name is the type name, so it's called RWMutex.
	// It promotes that type's methods onto the outer struct. sync.RWMutex has Lock, Unlock, RLock and RUnlock, so
	// ConcurrentStore gets them too. The shortcut (s.Lock()) just makes it look like the struct has a Lock method.
	// s.Lock() = s.RWMutex.Lock()
	// A better alternative: mu sync.RWMutex, make it lowercase and this avoids exposing mutex related functions publicly.
	// s.mu.Lock(), s.mu.RLock()
	sync.RWMutex
}

// NewConcurrentStore never initializes the mutex, and it doesn't have to. In Go, the zero value of
// sync.RWMutex is an unlocked mutex that's ready to use.
// No setup needed.
func NewConcurrentStore() *ConcurrentStore {
	return &ConcurrentStore{
		bookings: map[string]Booking{},
	}
}

func (s *ConcurrentStore) Book(b Booking) error {

	// Exactly one goroutine can hold the lock at a given time. No other writers or readers are allowed.
	// But while any goroutines have called ListBookings and any holds the key with s.RLock(), Book call waits at s.Lock()
	s.Lock()

	defer s.Unlock() // releases the lock on every return, and also if the function panics.

	if _, exists := s.bookings[b.SeatID]; exists {
		return ErrSeatAlreadyBooked
	}

	s.bookings[b.SeatID] = b

	return nil

}

func (s *ConcurrentStore) ListBookings(movieID string) []Booking {

	// RLock() allows any number of reader goroutines through, no writers are allowed.
	// Ten goroutines can list bookings at the same time.
	// If a writer goroutine has called Book and holds the lock with s.Lock(), readers wait at s.RLock().
	// A reader never sees the map halfway through a write.
	s.RLock()
	defer s.RUnlock()
	var result []Booking

	for _, b := range s.bookings {
		if b.MovieID == movieID {
			result = append(result, b)
		}
	}
	return result
}
