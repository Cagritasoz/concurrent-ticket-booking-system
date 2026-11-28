package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
)

// This file decides which HTTP status each service error gets; problem.go decides how an error
// response looks. The errors themselves are defined in the event package, which knows nothing of
// HTTP.

// versionMismatchDetail is the 412's explanation, the same for every kind of object.
const versionMismatchDetail = "it has changed since you read it: GET it again, then repeat your request with its new ETag in If-Match"

// writeServiceError turns an error from a service of the event module into a response, like an
// @ExceptionHandler in a @ControllerAdvice: one mapping for every handler, so the same error is
// the same status wherever it comes from (an archived venue is a 409 whether you change it or
// create a hall in it). Errors the module defines get their status; anything else is a 500 whose
// cause is logged but never sent, since it may show internals (SQL, hosts).
func writeServiceError(c *gin.Context, logger *slog.Logger, err error) {
	var invalid *event.ValidationError
	switch {
	case errors.As(err, &invalid): // errors.As also fills invalid, so its Fields can be sent
		writeValidationProblem(c, invalid.Fields)
	case errors.Is(err, event.ErrVenueNotFound), errors.Is(err, event.ErrHallNotFound),
		errors.Is(err, event.ErrLayoutNotFound), errors.Is(err, event.ErrSectionNotFound):
		writeProblem(c, http.StatusNotFound, err.Error())
	case errors.Is(err, event.ErrVersionMismatch):
		writeProblem(c, http.StatusPreconditionFailed, versionMismatchDetail)
	case errors.Is(err, event.ErrVenueArchived):
		writeProblem(c, http.StatusConflict, "venue is archived: restore it first")
	case errors.Is(err, event.ErrVenueNotArchived):
		writeProblem(c, http.StatusConflict, err.Error())
	case errors.Is(err, event.ErrHallArchived):
		writeProblem(c, http.StatusConflict, "hall is archived: restore it first")
	case errors.Is(err, event.ErrHallNotArchived):
		writeProblem(c, http.StatusConflict, err.Error())
	case errors.Is(err, event.ErrHallNameTaken):
		writeProblem(c, http.StatusConflict, "another hall of this venue already has this name")
	case errors.Is(err, event.ErrLayoutArchived):
		writeProblem(c, http.StatusConflict, "layout is archived: restore it first")
	case errors.Is(err, event.ErrLayoutNotArchived), errors.Is(err, event.ErrLayoutAlreadyActive), errors.Is(err, event.ErrLayoutNotActive):
		writeProblem(c, http.StatusConflict, err.Error())
	case errors.Is(err, event.ErrLayoutHasNoSections):
		writeProblem(c, http.StatusConflict, "layout has no sections: add one before activating it")
	case errors.Is(err, event.ErrLayoutNameTaken):
		writeProblem(c, http.StatusConflict, "another layout of this hall already has this name")
	case errors.Is(err, event.ErrLayoutNotDraft):
		writeProblem(c, http.StatusConflict, "layout is active: deactivate it first")
	case errors.Is(err, event.ErrSectionNameTaken):
		writeProblem(c, http.StatusConflict, "another section of this layout already has this name")
	default: // Handle unexpected errors.
		logger.ErrorContext(c.Request.Context(), "request failed", "error", err)
		writeProblem(c, http.StatusInternalServerError, "")
	}
}
