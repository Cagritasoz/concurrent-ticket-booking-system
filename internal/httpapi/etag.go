package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Optimistic locking over HTTP (RFC 9110, section 13): every object the admin API changes has a
// version, sent as the response's ETag header, e.g. ETag: "3". A request that changes the object
// (PATCH, DELETE, archive, restore) must send that ETag back in If-Match, saying "apply this only
// if the object is still the one I read". If someone changed it meanwhile, the version has moved
// on and the answer is 412 Precondition Failed; the client reads it again and repeats its change
// on the new values. Without the header the answer is 428 Precondition Required, so a client
// can't overwrite changes it hasn't seen by forgetting it.
//
// In Spring this is a @Version field with JPA, plus ShallowEtagHeaderFilter or ResponseEntity's
// .eTag() for the header.

// setETag sends the object's version as its ETag. An ETag is quoted text, and "3" without W/ is
// a strong one: it changes whenever the object changes, which a version does.
func setETag(c *gin.Context, version int) {
	c.Header("ETag", `"`+strconv.Itoa(version)+`"`)
}

// ifMatchVersion reads the version from the If-Match header of a request that changes an object.
// If the header is missing or unusable it writes the problem response and returns false:
//
//	missing, or *     428 Precondition Required: the header is needed, with an ETag from a GET
//	anything else     400 Bad Request: not one ETag this API sends, such as "3"
//
// "*" means "whatever version is there" in HTTP, which is the overwrite this check exists to
// prevent, so it isn't accepted. Weak ETags (W/"3") and lists ("3", "4") aren't either: this API
// sends neither, so a client sending one has a bug worth reporting.
func ifMatchVersion(c *gin.Context) (int, bool) {
	header := strings.TrimSpace(c.GetHeader("If-Match"))
	if header == "" || header == "*" {
		writeProblem(c, http.StatusPreconditionRequired,
			`this request must have an If-Match header with the ETag of your last read, e.g. If-Match: "3"; GET the resource to get it`)
		return 0, false
	}
	digits, ok := strings.CutPrefix(header, `"`)
	if ok {
		digits, ok = strings.CutSuffix(digits, `"`)
	}
	if !ok || digits == "" || strings.Trim(digits, "0123456789") != "" {
		writeProblem(c, http.StatusBadRequest,
			`the If-Match header must be one ETag exactly as this API sent it, e.g. "3"`)
		return 0, false
	}
	version, err := strconv.Atoi(digits)
	if err != nil {
		// Only digits, so the number is too large for an int. No object has that version, and
		// neither has 0, which versions never are: the service answers both with a mismatch.
		return 0, true
	}
	return version, true
}
