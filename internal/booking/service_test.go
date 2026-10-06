package booking

import (
	"sync"
	"sync/atomic"
	"testing"
	"uuid"
)

func TestConcurrentBooking_ExactlyOneWins(t *testing.T) {

	// DI (Dependency Injection)
	concurrentStore := NewConcurrentStore()
	svc := NewService(concurrentStore)

	const numGoroutines = 100_000 // 100k users trying to book a seat at the same time

	var (
		successes atomic.Int64 // Atomic integers that many goroutines can add to at once.
		failures  atomic.Int64
		wg        sync.WaitGroup // Countdown
	)

	// .Add(numGoroutines): set it to the number of goroutines which is 100K
	// Each goroutine calls Done(), Done() subtracts 1. Wait() blocks until it reaches 0. (All goroutines are done)
	// Goroutines are lightweight threads that are managed by the Go Runtime (not the OS) and are very cheap to create.
	wg.Add(numGoroutines)
	for i := range numGoroutines { // "for i := 0; i<numGoroutines;i++" Loops 0 to 99_999
		go func(userNum int) { // "go" starts this function as a new go routine.
			defer wg.Done()          // "defer" runs this when the function returns. calls Done() which subtracts one.
			err := svc.Book(Booking{ // Call service function Book, which uses the injected BookingStore implementation.
				MovieID: "screen-1",
				SeatID:  "A1",
				UserID:  uuid.New().String(),
			})
			if err == nil { // Error handling.
				successes.Add(1)
			} else {
				failures.Add(1)
			}
		}(i) // the (i) calls the anonymous function immediately with parameter "i" passed in as userNum which is not used.
		// "i" is the variable used in the for loop. Anonymous function is "func(userNum int)" declared and run with "i" passed
		// as a parameter in a goroutine.
	}
	wg.Wait() // block until all 100k goroutines have finished. The test function is blocked.

	if got := successes.Load(); got != 1 { // Check that successes were exactly one.
		t.Errorf("expected exactly 1 success, got %d", got) // Error format
	}
	if got := failures.Load(); got != int64(numGoroutines-1) { // Check that failures were 99_999
		t.Errorf("expected %d failures, got %d", numGoroutines-1, got)
	}
}
