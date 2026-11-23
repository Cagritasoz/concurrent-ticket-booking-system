package event

import (
	"errors"
	"math"
)

// ErrVersionMismatch is returned when a change names a version that is no longer the current
// one: someone else changed the object since the caller read it. The caller should read it again
// and redo its change on the new values. It is optimistic locking, like JPA's @Version and its
// OptimisticLockException; httpapi turns it into 412 Precondition Failed. Every versioned object
// of this module (venues now; halls, layouts and the rest as they are built) returns this one
// error, so httpapi needs one rule for all of them.
var ErrVersionMismatch = errors.New("version mismatch: changed since it was read")

// toVersion converts a version a caller passed into the column's type, a Postgres integer
// (int32). Versions start at 1, so a value outside 1..MaxInt32 can't be the current version of
// anything; it becomes 0, which no row has, and the query's normal "no match" handling reports
// the mismatch (or a 404 for an unknown id).
func toVersion(version int) int32 {
	if version < 1 || version > math.MaxInt32 {
		return 0
	}
	return int32(version)
}
