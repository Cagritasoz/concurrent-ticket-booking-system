package httpapi

import (
	"context"
	"sync/atomic"
)

// Pinger checks that a dependency can be reached. *pgxpool.Pool satisfies it without saying so:
// in Go, a type implements an interface just by having its methods, so there is no "implements".
// The interface lives here, in the package that uses it, and lets tests pass a fake database.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Readiness tracks whether the application itself can take traffic right now.
//
// It starts as "not ready". run marks it ready once the server is listening, and marks it not
// ready again when shutdown begins, so load balancers stop sending new requests.
// Whether Postgres can be reached is checked separately, on each /readyz request (see readyz).
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
