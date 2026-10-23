package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// healthz is the liveness check: "is the process alive and able to answer?"
// It always returns 200. If it stops answering, the platform (Docker, Kubernetes) restarts the
// app. It must not check databases: a database outage is not fixed by restarting the app.
// Spring equivalent: /actuator/health/liveness.
func healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// readyz is the readiness check: "should this instance receive traffic right now?"
// It returns 200 when ready and 503 Service Unavailable when not (still starting, shutting down,
// and from M1 also when Postgres is unreachable). A load balancer stops sending requests to an
// instance that answers 503, without restarting it.
// Spring equivalent: /actuator/health/readiness.
//
// readyz takes the Readiness it needs and returns the handler, so the handler can use it later.
// A function returning a function that remembers variables like this is called a closure.
func readyz(readiness *Readiness) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !readiness.IsReady() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}
