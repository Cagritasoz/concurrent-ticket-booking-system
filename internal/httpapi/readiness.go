package httpapi

import "sync/atomic"

// Readiness tracks whether the application can take traffic right now.
//
// It starts as "not ready". main marks it ready once the server is listening, and (from step 5)
// marks it not ready again when shutdown begins, so load balancers stop sending new requests.
// In M1, checks such as "can we reach Postgres?" are added here.
//
// The zero value is ready to use, like sync.Mutex: no constructor needed.
type Readiness struct {
	// atomic.Bool is safe to read and write from many goroutines at once, like Java's
	// AtomicBoolean. Every request runs in its own goroutine, so a plain bool would be a data race.
	ready atomic.Bool
}

// SetReady marks the application as ready (true) or not ready (false).
func (r *Readiness) SetReady(ready bool) {
	r.ready.Store(ready)
}

// IsReady reports whether the application can take traffic.
func (r *Readiness) IsReady() bool {
	return r.ready.Load()
}
