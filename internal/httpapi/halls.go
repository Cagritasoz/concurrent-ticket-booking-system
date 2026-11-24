package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
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
// and listing a venue's halls (/venues/{id}/halls). Once a hall exists, its own id names it, so
// reading or changing it is /halls/{id} with no venue in the path ("shallow nesting"): a client
// needn't know the venue, and the URL can't name a hall with the wrong venue.
type hallHandler struct {
	service *event.HallService
	logger  *slog.Logger
}

// hallsPath is where single halls live; the Location of a new hall points into it.
const hallsPath = "/api/v1/admin/halls"

// hallID reads the :id part of a /halls/:id path. An id that isn't a UUID can't name a hall, so
// it is a 404, as for venues; hallID then writes that response and returns false.
func hallID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		writeProblem(c, http.StatusNotFound, event.ErrHallNotFound.Error())
		return uuid.UUID{}, false
	}
	return id, true
}

// writeHall sends one hall with its version as the ETag, which a later change sends back in
// If-Match.
func writeHall(c *gin.Context, status int, hall event.Hall) {
	setETag(c, hall.Version)
	c.JSON(status, toHallResponse(hall))
}

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
	writeHall(c, http.StatusCreated, hall)
}

// list handles GET /api/v1/admin/venues/:id/halls?status=active: every hall of the venue with
// this status (default active), sorted by name. There is no paging, since a venue has only a few
// halls; the body is still an object, {"halls": [...]}, so paging could be added later without
// breaking clients. Like the venue list it has no ETag: each hall carries its version.
func (h hallHandler) list(c *gin.Context) {
	venueID, ok := venueID(c)
	if !ok {
		return
	}
	status := event.HallActive
	if s := c.Query("status"); s != "" {
		status = event.HallStatus(s) // the service checks it is one of the statuses
	}

	halls, err := h.service.List(c.Request.Context(), venueID, status)
	if err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	resp := hallListResponse{Halls: make([]hallResponse, len(halls))} // [] when empty, not null
	for i, hall := range halls {
		resp.Halls[i] = toHallResponse(hall)
	}
	c.JSON(http.StatusOK, resp)
}

// get handles GET /api/v1/admin/halls/:id, archived halls too.
func (h hallHandler) get(c *gin.Context) {
	id, ok := hallID(c)
	if !ok {
		return
	}
	hall, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	writeHall(c, http.StatusOK, hall)
}

// update handles PATCH /api/v1/admin/halls/:id, a JSON Merge Patch of the version named in
// If-Match, like PATCH /venues/:id: the body is checked against the create body's shape first,
// then decoded into hallPatch, which records which fields were sent.
func (h hallHandler) update(c *gin.Context) {
	id, ok := hallID(c)
	if !ok {
		return
	}
	version, ok := ifMatchVersion(c)
	if !ok {
		return
	}
	body, ok := readBody(c, mergePatchContentType, jsonContentType)
	if !ok {
		return
	}
	if !decodeStrict(c, body, &hallRequest{}) {
		return
	}
	var patch hallPatch
	// Uses patchField's custom UnmarshalJSON for each matching field present
	// in the JSON body. Omitted fields retain their zero value (Set == false).
	if err := json.Unmarshal(body, &patch); err != nil {
		// Can't happen for a body that passed the check above, so it would be our bug (500).
		writeServiceError(c, h.logger, fmt.Errorf("decode hall patch: %w", err))
		return
	}

	hall, err := h.service.Update(c.Request.Context(), id, version, func(in *event.HallInput) error {
		patch.apply(in)
		return nil
	})
	if err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	writeHall(c, http.StatusOK, hall)
}

// archive handles POST /api/v1/admin/halls/:id/archive of the version named in If-Match.
func (h hallHandler) archive(c *gin.Context) {
	h.changeStatus(c, h.service.Archive)
}

// restore handles POST /api/v1/admin/halls/:id/restore, archive in reverse.
func (h hallHandler) restore(c *gin.Context) {
	h.changeStatus(c, h.service.Restore)
}

// changeStatus is the shared body of archive and restore, like venueHandler.changeStatus.
func (h hallHandler) changeStatus(c *gin.Context, op func(context.Context, uuid.UUID, int) (event.Hall, error)) {
	id, ok := hallID(c)
	if !ok {
		return
	}
	version, ok := ifMatchVersion(c)
	if !ok {
		return
	}
	hall, err := op(c.Request.Context(), id, version)
	if err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	writeHall(c, http.StatusOK, hall)
}

// delete handles DELETE /api/v1/admin/halls/:id of the version named in If-Match, and answers 204
// No Content. Its layouts, their sections and seats are deleted with it.
func (h hallHandler) delete(c *gin.Context) {
	id, ok := hallID(c)
	if !ok {
		return
	}
	version, ok := ifMatchVersion(c)
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), id, version); err != nil {
		writeServiceError(c, h.logger, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// hallRequest is the JSON body of POST /venues/{id}/halls; PATCH /halls/{id} checks its body
// against it too. Capacity is a pointer so a missing capacity ("not known") can be told apart
// from 0, which the service rejects.
type hallRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Capacity    *int    `json:"capacity"`
}

func (r hallRequest) toInput() event.HallInput {
	return event.HallInput{Name: r.Name, Description: r.Description, Capacity: r.Capacity}
}

// hallPatch is the JSON body of PATCH /halls/{id}: any subset of a hall's fields, with the three
// states of patchField (left out, null, a value).
type hallPatch struct {
	Name        patchField[string] `json:"name"`
	Description patchField[string] `json:"description"`
	Capacity    patchField[int]    `json:"capacity"`
}

// apply changes in, the hall's current values, by the patch. null clears description and
// capacity; a null name becomes "", which the service reports as "is required". A hall's fields
// have no rule that spans two of them (unlike a venue's location), so all checks are the
// service's.
func (p hallPatch) apply(in *event.HallInput) {
	setRequired(p.Name, &in.Name)
	setOptional(p.Description, &in.Description)
	setOptional(p.Capacity, &in.Capacity)
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

// hallListResponse is the JSON form of a venue's halls: {"halls": [...]}.
type hallListResponse struct {
	Halls []hallResponse `json:"halls"`
}
