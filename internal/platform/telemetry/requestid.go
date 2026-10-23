package telemetry

import (
	"context"
	"log/slog"
)

// requestIDKey is the key under which the request ID is stored in a context.Context.
// It is an unexported empty struct type, so no other package can create the same key and
// accidentally overwrite or read our value.
type requestIDKey struct{}

// ContextWithRequestID returns a copy of ctx that carries the request ID.
// The HTTP layer calls this once per request; everything that receives that ctx can log the ID.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext returns the request ID stored in ctx, if there is one.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey{}).(string)
	return id, ok && id != ""
}

// contextHandler wraps another slog.Handler and adds the request ID from the context to every
// record logged with a context (logger.InfoContext(ctx, ...), logger.Log(ctx, ...)).
//
// This is how Java's MDC works with SLF4J: put the ID in once per request, and every log line
// written while handling that request includes it, without passing it to each log call.
// The difference: Go has no thread-locals, so the ID travels in the context.Context instead.
type contextHandler struct {
	slog.Handler // embedded: all methods we don't override are passed through to the wrapped handler
}

// Handle adds request_id to the record if ctx carries one, then passes it on.
func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id, ok := RequestIDFromContext(ctx); ok {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs and WithGroup must return a wrapped handler too. Otherwise a logger made with
// logger.With(...) would use the inner handler directly and lose the request ID.
func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
