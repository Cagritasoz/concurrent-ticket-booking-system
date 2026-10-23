// Package httpapi is the HTTP layer of the application: the Gin router, middleware and handlers.
//
// Gin is used only inside this package. Services in other packages never see *gin.Context;
// handlers here translate between HTTP and plain Go calls, like @RestController classes in Spring.
package httpapi

import (
	"log/slog"

	"github.com/gin-gonic/gin"
)

// init runs once, automatically, when the package is loaded (like a static {} block in Java).
// Release mode stops Gin from printing its debug output (route list, warnings) to the console.
// Gin keeps the mode in a global variable, so it is set here once instead of in NewRouter.
func init() {
	gin.SetMode(gin.ReleaseMode)
}

// NewRouter builds the Gin engine with its middleware and routes.
func NewRouter(logger *slog.Logger, readiness *Readiness) *gin.Engine {
	// gin.New() starts with no middleware. gin.Default() would add Gin's own logger and recovery,
	// which write plain text to the console. We add our own so every log line goes through slog.
	router := gin.New()

	// By default Gin trusts the X-Forwarded-For header from any client when working out the
	// client IP, so anyone could fake their IP in our logs. We have no proxy in front of the app,
	// so trust none. With nil this call cannot fail, so its error is ignored on purpose.
	_ = router.SetTrustedProxies(nil)

	// Middleware runs for every request, in the order added, like servlet filters.
	// requestID comes first so every later log line has the ID. requestLogger comes before
	// recovery so it wraps it and also logs requests that panicked (as 500).
	router.Use(
		requestID(),
		requestLogger(logger),
		recovery(logger),
	)

	router.GET("/healthz", healthz)
	router.GET("/readyz", readyz(readiness))

	return router
}
