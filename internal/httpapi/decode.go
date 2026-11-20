package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
)

// maxRequestBodyBytes limits how much of a request body is read. A venue with every text field at
// its longest is about 25 KB, so 64 KiB is plenty, and a client can't make the server read
// (and hold in memory) a body of any size.
const maxRequestBodyBytes = 64 << 10

// decodeJSON reads the request body as one JSON object into dst, strictly:
//
//   - the Content-Type must be application/json (else 415 Unsupported Media Type);
//   - the body may be at most maxRequestBodyBytes long (else 413 Content Too Large);
//   - unknown fields are rejected, so a typo like "capcity" isn't silently ignored;
//   - nothing may follow the object.
//
// Every other problem with the body (empty, not valid JSON, a value of the wrong type) is a 400
// Bad Request. If the body can't be used, decodeJSON writes the problem response and returns
// false, and the handler just returns:
//
//	var req venueRequest
//	if !decodeJSON(c, &req) {
//		return
//	}
//
// Gin has c.ShouldBindJSON for this, which Spring's @RequestBody resembles more, but it accepts
// unknown fields unless a global setting changes that, and its validation rules are struct tags
// in the HTTP layer. Ours live in the service (event.VenueInput.clean).
func decodeJSON(c *gin.Context, dst any) bool {
	// ParseMediaType splits "application/json; charset=utf-8" into the type and its parameters.
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(c, http.StatusUnsupportedMediaType, `the request body must be JSON, sent with "Content-Type: application/json"`)
		return false
	}

	// MaxBytesReader returns an error once more than the limit is read, and tells the server to
	// close the connection afterwards instead of reading the rest.
	body := http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		status, detail := describeDecodeError(err)
		writeProblem(c, status, detail)
		return false
	}
	// Decode reads one JSON value. A second Decode must find the end of the body; anything else
	// ({"a":1}{"b":2}, or stray text after the object) would otherwise be ignored silently.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeProblem(c, http.StatusBadRequest, "the request body must contain one JSON object and nothing after it")
		return false
	}
	return true
}

// describeDecodeError turns an error from json.Decoder.Decode into a status and a detail for the
// client. encoding/json's own messages name Go types (e.g. "Go struct field
// venueRequest.capacity of type int"), which are our internals, so the details are written here
// in terms of the JSON.
func describeDecodeError(err error) (int, string) {
	var (
		tooLarge  *http.MaxBytesError
		syntaxErr *json.SyntaxError
		typeErr   *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &tooLarge):
		return http.StatusRequestEntityTooLarge, fmt.Sprintf("the request body must be at most %d bytes", tooLarge.Limit)
	case errors.Is(err, io.EOF):
		return http.StatusBadRequest, "the request body is empty"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return http.StatusBadRequest, "the request body is not valid JSON: it ends too early"
	case errors.As(err, &syntaxErr):
		return http.StatusBadRequest, fmt.Sprintf("the request body is not valid JSON: %s (at byte %d)", syntaxErr, syntaxErr.Offset)
	case errors.As(err, &typeErr):
		// Field is the path of the field in the JSON, e.g. "address.city"; it is empty when the
		// body itself is the wrong type, e.g. an array instead of an object.
		if typeErr.Field == "" {
			return http.StatusBadRequest, "the request body must be a JSON object"
		}
		return http.StatusBadRequest, fmt.Sprintf("%s must be %s", typeErr.Field, jsonKind(typeErr.Type))
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		// DisallowUnknownFields reports this with a plain error, not an error type, so the
		// message is the only way to recognize it. The rest of it is the quoted field name.
		return http.StatusBadRequest, "unknown field " + strings.TrimPrefix(err.Error(), "json: unknown field ")
	default:
		return http.StatusBadRequest, "the request body is not valid JSON"
	}
}

// jsonKind names, in JSON terms, what a value decoded into Go type t must look like.
func jsonKind(t reflect.Type) string {
	for t.Kind() == reflect.Pointer { // *int is still a number in JSON (or null)
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Bool:
		return "true or false"
	case reflect.Struct, reflect.Map:
		return "an object"
	case reflect.Slice, reflect.Array:
		return "an array"
	default:
		return "a different type"
	}
}
