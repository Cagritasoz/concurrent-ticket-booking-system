package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// readyzPingTimeout limits how long /readyz waits for the database, so a hanging database makes
// the check answer "not ready" instead of making it hang too.
const readyzPingTimeout = 2 * time.Second

// healthz is the liveness check: "is the process alive and able to answer?"
// It always returns 200. If it stops answering, the platform (Docker, Kubernetes) restarts the
// app. It must not check databases: a database outage is not fixed by restarting the app.
// Spring equivalent: /actuator/health/liveness.
func healthz(c *gin.Context) {
	// c.JSON writes the status code and body to c.Writer, which is Gin's wrapper around Go's standard http.ResponseWriter.
	// Go's net/http buffers what we write, a few KB at a time. When the handler chain finishes
	// (the outermost c.Next() returns), the server flushes the buffer and the client receives the response.
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// readyz is the readiness check: "should this instance receive traffic right now?"
// It returns 200 when ready and 503 Service Unavailable when not (still starting, shutting down,
// or Postgres can't be reached). A load balancer stops sending requests to an instance that
// answers 503, without restarting it.
// Spring equivalent: /actuator/health/readiness.
// It is a factory function that takes in a readiness struct pointer.
//
// readyz takes the Readiness it needs and returns the handler, so the handler can use it later.
// A function returning a function that remembers variables like this is called a closure.
func readyz(readiness *Readiness, database Pinger, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !readiness.IsReady() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready"})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), readyzPingTimeout)
		defer cancel()
		if err := database.Ping(ctx); err != nil {
			// The reason goes to the logs only; this endpoint is public, so it stays generic.
			logger.WarnContext(ctx, "readiness check failed", "error", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}
