// The tests in this folder check the queries sqlc generates from internal/event/queries against a
// real Postgres. They are written by hand; sqlc only replaces the files it generates (db.go,
// models.go and one .sql.go file per queries file).
package eventdb_test

import (
	"testing"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres/pgtest"
)

// TestMain runs the tests of this package and removes pgtest's Postgres container afterwards.
func TestMain(m *testing.M) { pgtest.Main(m) }
