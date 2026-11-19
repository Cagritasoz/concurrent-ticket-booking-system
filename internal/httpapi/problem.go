package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
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
	// says", and then Title is the status's standard text. Kinds with extra fields (validation
	// errors with a list of fields) get their own type later.
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	// Detail explains this occurrence for humans. omitempty leaves it out when empty, which is
	// how a 500 is sent: the cause goes to the log only.
	Detail string `json:"detail,omitempty"`
	// Instance is the request path the problem happened on.
	Instance string `json:"instance,omitempty"`
}

// writeProblem sends a Problem Details response with the given status and detail and stops the
// handler chain.
func writeProblem(c *gin.Context, status int, detail string) {
	// c.JSON sets Content-Type to application/json only if it isn't set yet, so setting it first
	// makes it application/problem+json.
	c.Header("Content-Type", problemContentType)
	c.AbortWithStatusJSON(status, problem{
		Type:     "about:blank",
		Title:    http.StatusText(status), // e.g. "Not Found"
		Status:   status,
		Detail:   detail,
		Instance: c.Request.URL.Path,
	})
}
