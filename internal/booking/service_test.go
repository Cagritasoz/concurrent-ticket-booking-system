package booking

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"uuid"

	"concurrent-ticket-booking-system/internal/adapters/redis"
)

func TestConcurrentBooking_ExactlyOneWins(t *testing.T) {

	// DI (Dependency Injection): the adapter creates the client, the store receives it, the service receives the store.
	rdb := redis.NewClient("localhost:6379")
	t.Cleanup(func() { rdb.Close() }) // t.Cleanup runs after the test finishes, like @AfterEach in JUnit.

	redisStore := NewRedisStore(rdb)
	svc := NewService(redisStore)

	// Unique movie per run, so a hold left over from a previous run (TTL 3 minutes) can't make the seat look taken.
	movieID := "test-movie-" + uuid.New().String()
	t.Cleanup(func() { rdb.Del(context.Background(), seatKey(movieID, "A1")) })

	// 10k instead of 100k: every booking is now a network call, and goroutines that wait too long for one of the
	// client's pooled connections fail with a pool timeout error instead of ErrSeatAlreadyBooked.
	const numGoroutines = 10_000 // 10k users trying to book a seat at the same time

	var (
		successes atomic.Int64 // Atomic integers that many goroutines can add to at once.
		failures  atomic.Int64
		other     atomic.Int64 // Errors that are not ErrSeatAlreadyBooked, e.g. Redis being unreachable.
		otherOnce sync.Once    // Runs its function only once, used to keep the first unexpected error.
		otherErr  error
		wg        sync.WaitGroup // Countdown
	)

	// .Add(numGoroutines): set it to the number of goroutines which is 10K
	// Each goroutine calls Done(), Done() subtracts 1. Wait() blocks until it reaches 0. (All goroutines are done)
	// Goroutines are lightweight threads that are managed by the Go Runtime (not the OS) and are very cheap to create.
	wg.Add(numGoroutines)
	for range numGoroutines { // Loops 10_000 times, no loop variable needed.
		go func() { // "go" starts this function as a new go routine.
			defer wg.Done()          // "defer" runs this when the function returns. calls Done() which subtracts one.
			err := svc.Book(Booking{ // Call service function Book, which uses the injected BookingStore implementation.
				ID:      uuid.New().String(), // Session ID, RedisStore rejects bookings without one.
				MovieID: movieID,
				SeatID:  "A1",
				UserID:  uuid.New().String(),
			})
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, ErrSeatAlreadyBooked): // Expected: someone else got the seat first.
				failures.Add(1)
			default: // Unexpected: the test should fail instead of counting this as a normal failure.
				other.Add(1)
				otherOnce.Do(func() { otherErr = err })
			}
		}() // the () calls the anonymous function immediately in a new goroutine. No parameters
	}
	wg.Wait() // block until all 10k goroutines have finished. The test function is blocked.

	if got := other.Load(); got != 0 {
		t.Errorf("expected no unexpected errors, got %d, first: %v", got, otherErr)
	}
	if got := successes.Load(); got != 1 { // Check that successes were exactly one.
		t.Errorf("expected exactly 1 success, got %d", got) // Error format
	}
	if got := failures.Load(); got != int64(numGoroutines-1) { // Check that failures were 9_999
		t.Errorf("expected %d failures, got %d", numGoroutines-1, got)
	}
}
