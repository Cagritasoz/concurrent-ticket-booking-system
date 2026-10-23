package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
	"uuid"

	"github.com/gin-gonic/gin"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/telemetry"
)

// requestIDHeader is the HTTP header used to pass a request ID in and out.
const requestIDHeader = "X-Request-ID"

// maxRequestIDLength limits how long a request ID sent by a client may be.
const maxRequestIDLength = 64

// requestID gives every request an ID, so all log lines of one request can be found together.
//
// If the client (or a proxy in front of us) already sent a valid X-Request-ID, it is reused, so
// the same ID can be followed across systems. Otherwise a new UUID is generated. The ID is
// stored in the request's context.Context, which makes the logger add it to every line logged
// with that context, and it is sent back in the X-Request-ID response header so a client can
// quote it when reporting a problem.
func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(requestIDHeader)
		if !validRequestID(id) {
			id = uuid.New().String()
		}

		// A context can't be changed, only extended into a new one. WithContext returns a copy of
		// the request using the new context, and we replace c.Request so everything after this
		// middleware (other middleware, handlers, services) sees it.
		ctx := telemetry.ContextWithRequestID(c.Request.Context(), id)
		c.Request = c.Request.WithContext(ctx)
		c.Header(requestIDHeader, id)

		c.Next()
	}
}

// validRequestID accepts only short IDs made of letters, digits, '-', '_' and '.'.
// A client-supplied value ends up in our logs, so anything else (very long values, spaces,
// newlines or quotes that could fake extra log lines) is rejected and replaced.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLength {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// requestLogger logs one line per request after it has been handled.
// The request ID is added automatically, because the log call passes the request's context.
//
// A Gin middleware is a handler that calls c.Next() to run the rest of the chain (the next
// middleware and finally the route handler), like chain.doFilter() in a servlet filter.
// Code before c.Next() runs before the handler, code after it runs after the response is ready.
func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		status := c.Writer.Status()
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		case c.Request.URL.Path == "/healthz" || c.Request.URL.Path == "/readyz":
			// Health checks are called every few seconds by Docker/Kubernetes. Logging them at
			// debug keeps them out of the normal (info) logs unless they fail.
			level = slog.LevelDebug
		}

		// c.Request.Context() carries the request ID; the logger reads it from there.
		logger.Log(c.Request.Context(), level, "http request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}

// recovery turns a panic in a handler into a 500 response and logs it through slog, instead of
// crashing the whole server. Spring does the same for an uncaught exception in a controller.
//
// Gin's built-in recovery writes its own text logs; passing a nil writer turns those off, and the
// function below logs the panic through slog instead. Gin still handles broken client
// connections itself (no stack trace for those).
func recovery(logger *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, recovered any) {
		logger.ErrorContext(c.Request.Context(), "panic recovered",
			"panic", recovered,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"stack", string(debug.Stack()),
		)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	})
}
