// The tests of package postgres are in package postgres_test, outside package postgres, because
// they use pgtest, and pgtest imports package postgres. Inside package postgres that would be an
// import cycle, which Go doesn't allow.
package postgres_test

import (
	"testing"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres/pgtest"
)

// TestMain runs instead of Go's default test runner and wraps all tests of this package, like a
// JUnit @BeforeAll/@AfterAll pair for the whole package: pgtest.Main runs the tests with m.Run and
// then removes the Postgres container.
func TestMain(m *testing.M) { pgtest.Main(m) }
