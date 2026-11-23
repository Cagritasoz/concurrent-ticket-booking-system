package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
)

// problemContentType is the media type RFC 9457 defines for Problem Details. Clients can tell an
// error body from a normal one by it, without looking inside.
const problemContentType = "application/problem+json"

// problem is an error response in the RFC 9457 "Problem Details" format, the one Spring Boot 3's
// ProblemDetail produces:
//
//	{"type":"about:blank","title":"Not Found","status":404,"detail":"venue not found","instance":"/api/v1/admin/venues/..."}
//
// The struct tags (`json:"..."`) give the JSON names, like @JsonProperty in Jackson.
type problem struct {
	// Type identifies the kind of problem. "about:blank" means "no more than the status code
	// says", and then Title is the status's standard text. A type URI of our own can be added
	// once the API has public documentation for it to point to.
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	// Detail explains this occurrence for humans. omitempty leaves it out when empty, which is
	// how a 500 is sent: the cause goes to the log only.
	Detail string `json:"detail,omitempty"`
	// Instance is the request path the problem happened on.
	Instance string `json:"instance,omitempty"`
	// Errors lists each invalid field of a 400 for invalid input. RFC 9457 allows extra members
	// like this one ("extensions"); it is left out of every other problem.
	Errors []fieldProblem `json:"errors,omitempty"`
}

// fieldProblem is one entry of problem.Errors: {"field":"address.city","message":"is required"}.
type fieldProblem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// writeProblem sends a Problem Details response with the given status and detail and stops the
// handler chain.
func writeProblem(c *gin.Context, status int, detail string) {
	sendProblem(c, problem{Status: status, Detail: detail})
}

// writeValidationProblem sends a 400 Bad Request listing every invalid field.
func writeValidationProblem(c *gin.Context, fields []event.FieldError) {
	p := problem{
		Status: http.StatusBadRequest,
		Detail: "the request has invalid fields; see errors",
		Errors: make([]fieldProblem, len(fields)),
	}
	for i, f := range fields {
		p.Errors[i] = fieldProblem{Field: f.Field, Message: f.Message}
	}
	sendProblem(c, p)
}

// sendProblem fills in the members every problem has and sends p.
func sendProblem(c *gin.Context, p problem) {
	p.Type = "about:blank"
	p.Title = http.StatusText(p.Status) // e.g. "Not Found"
	p.Instance = c.Request.URL.Path

	// c.JSON sets Content-Type to application/json only if it isn't set yet, so setting it first
	// makes it application/problem+json.
	c.Header("Content-Type", problemContentType)
	// Abort skips the handlers that haven't run yet (the chain is the middleware followed by the
	// route's handler), so an error written by a middleware ends the request there. The ones already
	// running still finish, which is how requestLogger logs this response.
	c.AbortWithStatusJSON(p.Status, p)
}
