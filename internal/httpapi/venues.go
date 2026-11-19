package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"
	"uuid"

	"github.com/gin-gonic/gin"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
)

// venueHandler holds the handlers of the /venues routes, like a @RestController class: each
// method is one endpoint. It translates HTTP into calls of event.VenueService and back, and has
// no venue rules of its own.
type venueHandler struct {
	service *event.VenueService
	logger  *slog.Logger
}

// get handles GET /api/v1/admin/venues/:id.
func (h venueHandler) get(c *gin.Context) {
	// c.Param reads the :id part of the path, like @PathVariable. An id that isn't a UUID can't
	// name a venue, so it is "not found" (404) too, not "bad request".
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		writeProblem(c, http.StatusNotFound, event.ErrVenueNotFound.Error())
		return
	}

	// The request's context goes to the service: it carries the request ID for logs, and it is
	// cancelled if the client disconnects, which also cancels the database query.
	venue, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toVenueResponse(venue))
}

// writeError turns an error from the service into a response, like an @ExceptionHandler in a
// @ControllerAdvice. Errors the module defines get their status; anything else is a 500 whose
// cause is logged but never sent, since it may show internals (SQL, hosts).
func (h venueHandler) writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, event.ErrVenueNotFound):
		writeProblem(c, http.StatusNotFound, err.Error())
	default: // Handle unexpected errors.
		h.logger.ErrorContext(c.Request.Context(), "request failed", "error", err)
		writeProblem(c, http.StatusInternalServerError, "")
	}
}

// venueResponse is the JSON form of a venue. It is separate from event.Venue so the API's
// format is decided here, in the HTTP layer: snake_case names, and optional values as null (a
// nil pointer is written as null) rather than left out, so every venue has the same keys.
type venueResponse struct {
	ID          uuid.UUID         `json:"id"` // written as text, e.g. "0b6e...", by UUID's MarshalText
	Name        string            `json:"name"`
	Description *string           `json:"description"`
	Address     addressResponse   `json:"address"`
	Location    *locationResponse `json:"location"`
	TimeZone    string            `json:"time_zone"`
	Capacity    *int              `json:"capacity"`
	CreatedAt   time.Time         `json:"created_at"` // RFC 3339, e.g. "2026-11-20T09:30:00.123456Z"
	UpdatedAt   time.Time         `json:"updated_at"`
}

type addressResponse struct {
	Line1       string  `json:"line1"`
	Line2       *string `json:"line2"`
	City        string  `json:"city"`
	Region      *string `json:"region"`
	PostalCode  *string `json:"postal_code"`
	CountryCode string  `json:"country_code"`
}

type locationResponse struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

func toVenueResponse(v event.Venue) venueResponse {
	r := venueResponse{
		ID:          v.ID,
		Name:        v.Name,
		Description: v.Description,
		Address: addressResponse{
			Line1:       v.Address.Line1,
			Line2:       v.Address.Line2,
			City:        v.Address.City,
			Region:      v.Address.Region,
			PostalCode:  v.Address.PostalCode,
			CountryCode: v.Address.CountryCode,
		},
		TimeZone: v.TimeZone,
		Capacity: v.Capacity,
		// pgx returns times in the server machine's local time zone. UTC makes the output the
		// same wherever the app runs.
		CreatedAt: v.CreatedAt.UTC(),
		UpdatedAt: v.UpdatedAt.UTC(),
	}
	if v.Location != nil {
		r.Location = &locationResponse{Latitude: v.Location.Latitude, Longitude: v.Location.Longitude}
	}
	return r
}
