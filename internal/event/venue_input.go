package event

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// VenueInput is what an admin provides to create a venue: every field of Venue except the ones
// Postgres fills in (ID, CreatedAt, UpdatedAt).
type VenueInput struct {
	Name        string
	Description *string
	Address     Address
	Location    *Location
	TimeZone    string
	Capacity    *int
}

// FieldError is one problem with one input field. Field is the field's path in the API's JSON
// (e.g. "address.city"), so httpapi can pass it on unchanged and the client knows which of its
// fields is meant.
type FieldError struct {
	Field   string
	Message string
}

// ValidationError is returned when input breaks the rules. It lists every problem at once, like
// a Bean Validation ConstraintViolationException, so a client can fix them all in one go.
// httpapi turns it into 400 Bad Request.
//
// Callers get the list back with errors.As:
//
//	var invalid *event.ValidationError
//	if errors.As(err, &invalid) { ... invalid.Fields ... }
type ValidationError struct {
	Fields []FieldError
}

// Error makes *ValidationError an error. The receiver is a pointer, so the error value is always
// a *ValidationError, which is what errors.As above looks for.
func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + ": " + f.Message
	}
	return "invalid input: " + strings.Join(parts, "; ")
}

// The longest text allowed per field, matching the CHECKs in migration 1. Like Postgres's
// length(), they count characters, not bytes: "İstanbul" is 8 characters but 9 bytes.
const (
	maxNameLength        = 200
	maxDescriptionLength = 5000
	maxAddressLineLength = 200
	maxCityLength        = 100
	maxRegionLength      = 100
	maxPostalCodeLength  = 20
	maxTimeZoneLength    = 64
)

// clean returns the input as it will be stored, or a *ValidationError listing every broken rule.
//
// Text is trimmed of surrounding whitespace. Optional text that is empty after trimming becomes
// nil (stored as NULL), so "" and a missing field mean the same: not set. The rules are checked
// here, before Postgres sees the input, so problems are reported per field and all at once; the
// CHECKs in the table stay as the last line of defense.
//
// The receiver is a copy (not a pointer), so clean never changes the caller's VenueInput.
func (in VenueInput) clean() (VenueInput, error) {
	var problems fieldErrors

	in.Name = problems.required("name", in.Name, maxNameLength)
	in.Description = problems.optional("description", in.Description, maxDescriptionLength)

	in.Address.Line1 = problems.required("address.line1", in.Address.Line1, maxAddressLineLength)
	in.Address.Line2 = problems.optional("address.line2", in.Address.Line2, maxAddressLineLength)
	in.Address.City = problems.required("address.city", in.Address.City, maxCityLength)
	in.Address.Region = problems.optional("address.region", in.Address.Region, maxRegionLength)
	in.Address.PostalCode = problems.optional("address.postal_code", in.Address.PostalCode, maxPostalCodeLength)
	in.Address.CountryCode = strings.TrimSpace(in.Address.CountryCode)
	if !isCountryCode(in.Address.CountryCode) {
		problems.add("address.country_code", "must be an ISO 3166-1 alpha-2 country code: two capital letters, e.g. TR")
	}

	// Postgres keeps 6 decimals (about 10 cm); more are rounded, not rejected.
	if l := in.Location; l != nil {
		// Written as !(in range) so that NaN, which is never in range, fails too.
		if !(l.Latitude >= -90 && l.Latitude <= 90) {
			problems.add("location.latitude", "must be between -90 and 90")
		}
		if !(l.Longitude >= -180 && l.Longitude <= 180) {
			problems.add("location.longitude", "must be between -180 and 180")
		}
	}

	in.TimeZone = strings.TrimSpace(in.TimeZone)
	if !isTimeZone(in.TimeZone) {
		problems.add("time_zone", "must be an IANA time zone name, e.g. Europe/Istanbul")
	}

	// The column is a Postgres integer (int32), so the largest int32 is the upper limit.
	if c := in.Capacity; c != nil && (*c < 1 || *c > math.MaxInt32) {
		problems.add("capacity", fmt.Sprintf("must be between 1 and %d", math.MaxInt32))
	}

	if len(problems) > 0 {
		return VenueInput{}, &ValidationError{Fields: problems}
	}
	return in, nil
}

// isCountryCode reports whether code has the shape of an ISO 3166-1 alpha-2 code: two capital
// letters A-Z. It doesn't check the code is assigned to a country (XX passes); that would need
// the ISO list, and a wrong country is a mistake the admin sees on the venue anyway.
func isCountryCode(code string) bool {
	return len(code) == 2 && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z'
}

// isTimeZone reports whether name is a time zone in the IANA database (e.g. Europe/Istanbul).
// Postgres can't easily check this, which is why it is checked here.
//
// time.LoadLocation looks the name up in the time zone database: the operating system's, or the
// copy that cmd/ticketing embeds into the binary (time/tzdata) for systems that have none, such
// as Windows or a minimal container image. It also accepts two names that aren't zones: "" (it
// means UTC) and "Local" (the server's own zone, which differs between machines).
func isTimeZone(name string) bool {
	if name == "" || name == "Local" || len(name) > maxTimeZoneLength {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// fieldErrors collects the problems clean finds. It is a named slice type, so it can have methods.
type fieldErrors []FieldError

func (f *fieldErrors) add(field, message string) {
	*f = append(*f, FieldError{Field: field, Message: message})
}

// required trims s and checks that it is not empty and at most maxLength characters long.
func (f *fieldErrors) required(field, s string, maxLength int) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		f.add(field, "is required")
	case utf8.RuneCountInString(s) > maxLength:
		f.add(field, fmt.Sprintf("must be at most %d characters", maxLength))
	}
	return s
}

// optional trims *s and checks its length. Text that is empty after trimming becomes nil. It
// returns a new pointer, so the caller's string is never changed.
func (f *fieldErrors) optional(field string, s *string, maxLength int) *string {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	if trimmed == "" {
		return nil
	}
	if utf8.RuneCountInString(trimmed) > maxLength {
		f.add(field, fmt.Sprintf("must be at most %d characters", maxLength))
	}
	return &trimmed
}
