// Package httpapi is the HTTP layer of the application: the Gin router, middleware and handlers.
//
// Gin is used only inside this package. Services in other packages never see *gin.Context;
// handlers here translate between HTTP and plain Go calls, like @RestController classes in Spring.
package httpapi

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
)

// init runs once, automatically, when the package is loaded (like a static {} block in Java).
// Release mode stops Gin from printing its debug output (route list, warnings) to the console.
// Gin keeps the mode in a global variable, so it is set here once instead of in NewRouter.
func init() {
	gin.SetMode(gin.ReleaseMode)
}

// NewRouter builds the Gin engine with its middleware and routes. The services are passed in
// ready-made (created in cmd/ticketing), like beans injected into a controller's constructor.
func NewRouter(logger *slog.Logger, readiness *Readiness, database Pinger, venues *event.VenueService, halls *event.HallService) *gin.Engine {
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
	// All functions below return a gin.HandlerFunc
	router.Use(
		requestID(),
		requestLogger(logger),
		recovery(logger),
	)

	// no () for healthz → passes the function itself, doesn't call it.
	// It doesn't run it at that point; it stores it and calls it
	// later, once per request, passing in that request's *gin.Context.
	// In a way the following code is saying when a request is sent to this path call this handler func.
	// We can think of it like spring's controller method.
	router.GET("/healthz", healthz)
	router.GET("/readyz", readyz(readiness, database, logger))

	// The admin API. A route group shares its path prefix (like @RequestMapping on a controller
	// class) and can get its own middleware: M2 adds authentication to this group only.
	admin := router.Group("/api/v1/admin")
	venueRoutes := venueHandler{service: venues, logger: logger}
	// venueRoutes.get is a "method value": the method bound to this venueRoutes, passed without
	// calling it, so Gin can call it per request like the functions above.
	admin.POST("/venues", venueRoutes.create)
	admin.GET("/venues", venueRoutes.list)
	admin.GET("/venues/:id", venueRoutes.get)
	admin.PATCH("/venues/:id", venueRoutes.update)
	admin.DELETE("/venues/:id", venueRoutes.delete)
	admin.POST("/venues/:id/archive", venueRoutes.archive)
	admin.POST("/venues/:id/restore", venueRoutes.restore)

	hallRoutes := hallHandler{service: halls, logger: logger}
	admin.POST("/venues/:id/halls", hallRoutes.create)
	admin.GET("/venues/:id/halls", hallRoutes.list)
	admin.GET("/halls/:id", hallRoutes.get)
	admin.PATCH("/halls/:id", hallRoutes.update)
	admin.DELETE("/halls/:id", hallRoutes.delete)
	admin.POST("/halls/:id/archive", hallRoutes.archive)
	admin.POST("/halls/:id/restore", hallRoutes.restore)

	return router
}
