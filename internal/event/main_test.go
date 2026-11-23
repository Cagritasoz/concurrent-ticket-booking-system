package event_test

import (
	"testing"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres/pgtest"
)

// TestMain runs the tests of this package and removes pgtest's Postgres container afterwards.
func TestMain(m *testing.M) { pgtest.Main(m) }
