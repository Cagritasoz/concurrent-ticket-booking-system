package httpapi

import (
	"testing"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres/pgtest"
)

// TestMain runs the tests of this package and removes pgtest's Postgres container afterwards.
// Only the venue tests use Postgres; the others never start the container.
func TestMain(m *testing.M) { pgtest.Main(m) }
