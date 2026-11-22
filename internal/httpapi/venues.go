package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
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

// venueID reads the :id part of the path, like @PathVariable. An id that isn't a UUID can't name
// a venue, so it is "not found" (404) too, not "bad request"; venueID then writes that response
// and returns false.
func venueID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		writeProblem(c, http.StatusNotFound, event.ErrVenueNotFound.Error())
		return uuid.UUID{}, false
	}
	return id, true
}

// get handles GET /api/v1/admin/venues/:id.
func (h venueHandler) get(c *gin.Context) {
	id, ok := venueID(c)
	if !ok {
		return
	}

	// The request's context goes to the service: it carries the request ID for logs, and it is
	// canceled if the client disconnects, which also cancels the database query.
	venue, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		h.writeError(c, err)
		return
	}
	writeVenue(c, http.StatusOK, venue)
}

// writeVenue sends one venue with its version as the ETag, which a later change sends back in
// If-Match.
func writeVenue(c *gin.Context, status int, venue event.Venue) {
	setETag(c, venue.Version)
	c.JSON(status, toVenueResponse(venue))
}

// create handles POST /api/v1/admin/venues. It answers 201 Created with the new venue and its
// URL in the Location header, like Spring's ResponseEntity.created(uri).body(venue).
func (h venueHandler) create(c *gin.Context) {
	var req venueRequest
	if !decodeJSON(c, &req, jsonContentType) {
		return // decodeJSON has written the problem response
	}
	in, problems := req.toInput()
	if len(problems) > 0 { // at least one problem
		writeValidationProblem(c, problems)
		return
	}

	venue, err := h.service.Create(c.Request.Context(), in)
	if err != nil {
		h.writeError(c, err)
		return
	}
	// c.FullPath() is the route's path pattern, here "/api/v1/admin/venues".
	c.Header("Location", c.FullPath()+"/"+venue.ID.String())
	writeVenue(c, http.StatusCreated, venue)
}

// list handles GET /api/v1/admin/venues?status=active&limit=20&cursor=..., one page of venues
// sorted by name. Every query parameter is optional (like @RequestParam(required = false)):
// status defaults to active (archived venues are listed only when asked for), limit to
// event.DefaultVenuePageSize, and without a cursor the first page is returned. To get the next
// page, the client sends the next_cursor of the previous response as cursor, with the same status.
//
// A list has no ETag: each venue carries its version in the body instead, so a client can change
// any of them with the version it was shown.
func (h venueHandler) list(c *gin.Context) {
	status := event.VenueActive
	if s := c.Query("status"); s != "" {
		status = event.VenueStatus(s) // the service checks it is one of the statuses
	}
	limit := event.DefaultVenuePageSize // 20 is the default
	var after *event.VenueCursor
	// Problems with the text of the parameters are found here; the limit's range is the
	// service's rule. c.Query returns "" for a parameter that is missing or empty.
	var problems []event.FieldError
	if s := c.Query("limit"); s != "" {
		if n, err := strconv.Atoi(s); err != nil { // convert string to int via Atoi.
			problems = append(problems, event.FieldError{Field: "limit",
				Message: fmt.Sprintf("must be a whole number between 1 and %d", event.MaxVenuePageSize)})
		} else {
			limit = n
		}
	}
	if s := c.Query("cursor"); s != "" {
		if cursor, err := decodeVenueCursor(s); err != nil {
			problems = append(problems, event.FieldError{Field: "cursor",
				Message: "must be the next_cursor of a previous page"})
		} else {
			after = &cursor
		}
	}
	if len(problems) > 0 {
		writeValidationProblem(c, problems)
		return
	}

	page, err := h.service.List(c.Request.Context(), status, after, limit)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toVenueListResponse(page))
}

// update handles PATCH /api/v1/admin/venues/:id: it changes only the fields in the body (JSON
// Merge Patch, see venuePatch) of the version named in If-Match, and answers 200 with the venue
// as stored and its new ETag.
func (h venueHandler) update(c *gin.Context) {
	id, ok := venueID(c)
	if !ok {
		return
	}
	version, ok := ifMatchVersion(c)
	if !ok {
		return
	}
	// Merge Patch is the standard media type; plain JSON is accepted too, since the body is the
	// same and many clients send application/json by default.
	body, ok := readBody(c, mergePatchContentType, jsonContentType)
	if !ok {
		return
	}
	// First check the body against the create body's shape, for the same strict checks and
	// error messages; then decode it into venuePatch, which records which fields were sent.
	if !decodeStrict(c, body, &venueRequest{}) {
		return
	}
	var patch venuePatch
	if err := json.Unmarshal(body, &patch); err != nil {
		// Can't happen for a body that passed the check above, so it would be our bug (500).
		h.writeError(c, fmt.Errorf("decode venue patch: %w", err))
		return
	}

	// The patch is applied inside the service's transaction, to the venue's current values: a
	// function literal (a closure, like a Java lambda) that captures patch.
	venue, err := h.service.Update(c.Request.Context(), id, version, func(in *event.VenueInput) error {
		if problems := patch.apply(in); len(problems) > 0 {
			return &event.ValidationError{Fields: problems}
		}
		return nil
	})
	if err != nil {
		h.writeError(c, err)
		return
	}
	writeVenue(c, http.StatusOK, venue)
}

// archive handles POST /api/v1/admin/venues/:id/archive: the version named in If-Match becomes
// archived, hidden from the default list and closed to changes. It answers 200 with the venue
// and its new ETag. Archive and restore are actions, not field changes, so they are their own
// endpoints (Google's API guide calls these custom methods) rather than "status" in a PATCH body.
func (h venueHandler) archive(c *gin.Context) {
	h.changeStatus(c, h.service.Archive)
}

// restore handles POST /api/v1/admin/venues/:id/restore, archive in reverse.
func (h venueHandler) restore(c *gin.Context) {
	h.changeStatus(c, h.service.Restore)
}

// changeStatus is the shared body of archive and restore; op is the service method to call (a
// method value, like a Java method reference: service::archive).
func (h venueHandler) changeStatus(c *gin.Context, op func(context.Context, uuid.UUID, int) (event.Venue, error)) {
	id, ok := venueID(c)
	if !ok {
		return
	}
	version, ok := ifMatchVersion(c)
	if !ok {
		return
	}
	venue, err := op(c.Request.Context(), id, version)
	if err != nil {
		h.writeError(c, err)
		return
	}
	writeVenue(c, http.StatusOK, venue)
}

// delete handles DELETE /api/v1/admin/venues/:id of the version named in If-Match, and answers
// 204 No Content: success, and no body. Deleting the same venue again is a 404. Archived venues
// can be deleted too; once events exist (M1 step 8), a venue any event uses can only be archived.
func (h venueHandler) delete(c *gin.Context) {
	id, ok := venueID(c)
	if !ok {
		return
	}
	version, ok := ifMatchVersion(c)
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), id, version); err != nil {
		h.writeError(c, err)
		return
	}
	// Status only sets the code; Gin sends it when the handler returns, with nothing written.
	c.Status(http.StatusNoContent)
}

// versionMismatchDetail is the 412's explanation, the same for every kind of object.
const versionMismatchDetail = "it has changed since you read it: GET it again, then repeat your request with its new ETag in If-Match"

// writeError turns an error from the service into a response, like an @ExceptionHandler in a
// @ControllerAdvice. Errors the module defines get their status; anything else is a 500 whose
// cause is logged but never sent, since it may show internals (SQL, hosts).
func (h venueHandler) writeError(c *gin.Context, err error) {
	var invalid *event.ValidationError
	switch {
	case errors.As(err, &invalid): // errors.As also fills invalid, so its Fields can be sent
		writeValidationProblem(c, invalid.Fields)
	case errors.Is(err, event.ErrVenueNotFound):
		writeProblem(c, http.StatusNotFound, err.Error())
	case errors.Is(err, event.ErrVersionMismatch):
		writeProblem(c, http.StatusPreconditionFailed, versionMismatchDetail)
	case errors.Is(err, event.ErrVenueArchived):
		writeProblem(c, http.StatusConflict, "venue is archived: restore it first")
	case errors.Is(err, event.ErrVenueNotArchived):
		writeProblem(c, http.StatusConflict, err.Error())
	default: // Handle unexpected errors.
		h.logger.ErrorContext(c.Request.Context(), "request failed", "error", err)
		writeProblem(c, http.StatusInternalServerError, "")
	}
}

// venueRequest is the JSON body of POST /venues. A missing field decodes as its zero value: ""
// for a required text (which the service reports as "is required") and nil for an optional one.
// null does the same. PATCH /venues/{id} checks its body against it too (see update).
type venueRequest struct {
	Name        string           `json:"name"`
	Description *string          `json:"description"`
	Address     addressJSON      `json:"address"`
	Location    *locationRequest `json:"location"`
	TimeZone    string           `json:"time_zone"`
}

// locationRequest has pointers so a missing latitude or longitude can be told apart from 0,
// which is a real position (the equator, the prime meridian).
type locationRequest struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

// toInput converts the request into the service's input. The one rule checked here is that a
// location has both coordinates: event.Location has no "missing" value for them, so this can't
// be left to the service. Those problems are reported before the service's own checks run.
func (r venueRequest) toInput() (event.VenueInput, []event.FieldError) {
	in := event.VenueInput{
		Name:        r.Name,
		Description: r.Description,
		Address: event.Address{
			Line1:       r.Address.Line1,
			Line2:       r.Address.Line2,
			City:        r.Address.City,
			Region:      r.Address.Region,
			PostalCode:  r.Address.PostalCode,
			CountryCode: r.Address.CountryCode,
		},
		TimeZone: r.TimeZone,
	}
	var problems []event.FieldError
	if l := r.Location; l != nil {
		if l.Latitude == nil {
			problems = append(problems, event.FieldError{Field: "location.latitude", Message: "is required"})
		}
		if l.Longitude == nil {
			problems = append(problems, event.FieldError{Field: "location.longitude", Message: "is required"})
		}
		if l.Latitude != nil && l.Longitude != nil {
			in.Location = &event.Location{Latitude: *l.Latitude, Longitude: *l.Longitude}
		}
	}
	return in, problems
}

// venueResponse is the JSON form of a venue. It is separate from event.Venue so the API's
// format is decided here, in the HTTP layer: snake_case names, and optional values as null (a
// nil pointer is written as null) rather than left out, so every venue has the same keys.
type venueResponse struct {
	ID          uuid.UUID         `json:"id"` // written as text, e.g. "0b6e...", by UUID's MarshalText
	Name        string            `json:"name"`
	Description *string           `json:"description"`
	Address     addressJSON       `json:"address"`
	Location    *locationResponse `json:"location"`
	TimeZone    string            `json:"time_zone"`
	Status      event.VenueStatus `json:"status"` // "active" or "archived"
	// Version is also sent as the ETag header of single-venue responses; a list has only this.
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"` // RFC 3339, e.g. "2026-11-20T09:30:00.123456Z"
	UpdatedAt time.Time `json:"updated_at"`
}

// addressJSON is the JSON form of an address, the same in requests and responses.
type addressJSON struct {
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
		Address: addressJSON{
			Line1:       v.Address.Line1,
			Line2:       v.Address.Line2,
			City:        v.Address.City,
			Region:      v.Address.Region,
			PostalCode:  v.Address.PostalCode,
			CountryCode: v.Address.CountryCode,
		},
		TimeZone: v.TimeZone,
		Status:   v.Status,
		Version:  v.Version,
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

// venueListResponse is the JSON form of one page: {"venues": [...], "next_cursor": "..."}.
// next_cursor is null on the last page, so a client pages until it gets null.
type venueListResponse struct {
	Venues     []venueResponse `json:"venues"`
	NextCursor *string         `json:"next_cursor"`
}

func toVenueListResponse(page event.VenuePage) venueListResponse {
	r := venueListResponse{Venues: make([]venueResponse, len(page.Venues))} // [] when empty, not null
	for i, v := range page.Venues {
		r.Venues[i] = toVenueResponse(v)
	}
	if page.Next != nil {
		r.NextCursor = new(encodeVenueCursor(*page.Next))
	}
	return r
}

// venueCursorJSON is what a cursor holds: the name and id of the last venue of a page.
type venueCursorJSON struct {
	Name string    `json:"name"`
	ID   uuid.UUID `json:"id"`
}

// encodeVenueCursor turns a cursor into the opaque text the client gets as next_cursor: JSON,
// base64url-encoded so it can go into a URL without escaping. "Opaque" means clients only pass it
// back and never build one, so its contents can change without breaking them. It is not secret
// (anyone can decode base64) and doesn't need to be: it holds only a venue's name and id, which
// the client was just sent anyway, and an edited cursor only starts the page somewhere else.
func encodeVenueCursor(cursor event.VenueCursor) string {
	// json.Marshal fails only for values JSON can't hold (channels, functions, NaN); a string and
	// a UUID always encode, so the error is ignored.
	b, _ := json.Marshal(venueCursorJSON{Name: cursor.Name, ID: cursor.ID})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeVenueCursor is encodeVenueCursor in reverse. It fails for text that encodeVenueCursor
// can't have produced.
func decodeVenueCursor(s string) (event.VenueCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return event.VenueCursor{}, err
	}
	var cursor venueCursorJSON
	if err := json.Unmarshal(b, &cursor); err != nil { // an id that isn't a UUID fails here too
		return event.VenueCursor{}, err
	}
	return event.VenueCursor{Name: cursor.Name, ID: cursor.ID}, nil
}
