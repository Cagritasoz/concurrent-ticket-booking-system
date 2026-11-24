package httpapi

import (
	"log/slog"
	"net/http"
	"time"
	"uuid"

	"github.com/gin-gonic/gin"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
)

// hallHandler holds the handlers of the hall routes, like venueHandler for venues.
//
// Halls are nested under their venue only where the venue is needed to find them: creating one
// (POST /venues/{id}/halls) and, later, listing a venue's halls. Once a hall exists, its own id
// names it, so reading or changing it is /halls/{id} with no venue in the path ("shallow
// nesting"): a client needn't know the venue, and the URL can't name a hall with the wrong venue.
type hallHandler struct {
	service *event.HallService
	logger  *slog.Logger
}

// hallsPath is where single halls live; the Location of a new hall points into it.
const hallsPath = "/api/v1/admin/halls"

// create handles POST /api/v1/admin/venues/:id/halls. It answers 201 Created with the new hall,
// its ETag, and its URL in the Location header.
func (h hallHandler) create(c *gin.Context) {
	venueID, ok := venueID(c) // a malformed id is a 404 for the venue, as on every /venues/:id route
	if !ok {
		return
	}
	var req hallRequest
	if !decodeJSON(c, &req, jsonContentType) {
		return
	}

	hall, err := h.service.Create(c.Request.Context(), venueID, req.toInput())
	if err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	c.Header("Location", hallsPath+"/"+hall.ID.String())
	setETag(c, hall.Version)
	c.JSON(http.StatusCreated, toHallResponse(hall))
}

// hallRequest is the JSON body of POST /venues/{id}/halls. Capacity is a pointer so a missing
// capacity ("not known") can be told apart from 0, which the service rejects.
type hallRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Capacity    *int    `json:"capacity"`
}

func (r hallRequest) toInput() event.HallInput {
	return event.HallInput{Name: r.Name, Description: r.Description, Capacity: r.Capacity}
}

// hallResponse is the JSON form of a hall. Optional values are null rather than left out, as in
// venueResponse.
type hallResponse struct {
	ID          uuid.UUID        `json:"id"`
	VenueID     uuid.UUID        `json:"venue_id"`
	Name        string           `json:"name"`
	Description *string          `json:"description"`
	Capacity    *int             `json:"capacity"`
	Status      event.HallStatus `json:"status"`
	Version     int              `json:"version"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

func toHallResponse(h event.Hall) hallResponse {
	return hallResponse{
		ID:          h.ID,
		VenueID:     h.VenueID,
		Name:        h.Name,
		Description: h.Description,
		Capacity:    h.Capacity,
		Status:      h.Status,
		Version:     h.Version,
		CreatedAt:   h.CreatedAt.UTC(),
		UpdatedAt:   h.UpdatedAt.UTC(),
	}
}
