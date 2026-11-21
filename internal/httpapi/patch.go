package httpapi

import (
	"encoding/json"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
)

// patchField is one field of a JSON Merge Patch (RFC 7396) body. A field there has three states:
//
//	{}                     left out: keep the stored value
//	{"capacity": null}     null: clear it
//	{"capacity": 6000}     a value: set it
//
// A pointer has only two (nil and a value), so with *int a missing capacity and a null one would
// both be nil. patchField records whether the field was in the body at all.
type patchField[T any] struct {
	Set   bool // the field was in the body, as a value or as null
	Null  bool // its value was null
	Value T    // the value, when Set and not Null
}

// UnmarshalJSON makes patchField a json.Unmarshaler, like a custom Jackson deserializer.
// encoding/json calls it only for fields that are in the body (with the text "null" for null),
// so a field that is left out keeps the zero patchField, whose Set is false.
func (f *patchField[T]) UnmarshalJSON(data []byte) error {
	f.Set = true
	if string(data) == "null" {
		f.Null = true
		return nil
	}
	return json.Unmarshal(data, &f.Value)
}

// venuePatch is the JSON body of PATCH /venues/{id}: any subset of a venue's fields. Objects are
// merged, not replaced: {"address": {"city": "Ankara"}} changes the city and keeps the rest of
// the address.
//
// The body is decoded into venuePatch only after it passed decodeStrict as a venueRequest, which
// has the same fields and types. That check reports unknown fields and wrong types with the
// field's path, as for a create. venuePatch can't do it itself: an error returned from a custom
// UnmarshalJSON reaches the client without the path ("must be a whole number", but which field?),
// and the outer decoder's DisallowUnknownFields doesn't reach inside one.
type venuePatch struct {
	Name        patchField[string]        `json:"name"`
	Description patchField[string]        `json:"description"`
	Address     patchField[addressPatch]  `json:"address"`
	Location    patchField[locationPatch] `json:"location"`
	TimeZone    patchField[string]        `json:"time_zone"`
	Capacity    patchField[int]           `json:"capacity"`
}

type addressPatch struct {
	Line1       patchField[string] `json:"line1"`
	Line2       patchField[string] `json:"line2"`
	City        patchField[string] `json:"city"`
	Region      patchField[string] `json:"region"`
	PostalCode  patchField[string] `json:"postal_code"`
	CountryCode patchField[string] `json:"country_code"`
}

type locationPatch struct {
	Latitude  patchField[float64] `json:"latitude"`
	Longitude patchField[float64] `json:"longitude"`
}

// apply changes in, the venue's current values, by the patch. The service then checks the result
// with the same rules as a new venue, so a required field set to null (which becomes "") is
// reported there as "is required". The one rule checked here, as in venueRequest.toInput, is that
// a location ends up with both coordinates or neither.
func (p venuePatch) apply(in *event.VenueInput) []event.FieldError {
	setRequired(p.Name, &in.Name)
	setOptional(p.Description, &in.Description)

	if p.Address.Null {
		// Clears every address field, so the service reports the required ones, as it does
		// for a new venue sent without an address.
		in.Address = event.Address{}
	} else if p.Address.Set {
		a := p.Address.Value
		setRequired(a.Line1, &in.Address.Line1)
		setOptional(a.Line2, &in.Address.Line2)
		setRequired(a.City, &in.Address.City)
		setOptional(a.Region, &in.Address.Region)
		setOptional(a.PostalCode, &in.Address.PostalCode)
		setRequired(a.CountryCode, &in.Address.CountryCode)
	}

	setRequired(p.TimeZone, &in.TimeZone)
	setOptional(p.Capacity, &in.Capacity)

	if p.Location.Null {
		in.Location = nil
		return nil
	}
	if !p.Location.Set {
		return nil
	}
	// Merge the coordinates into the current ones, then check the pair.
	var latitude, longitude *float64
	if in.Location != nil {
		latitude, longitude = &in.Location.Latitude, &in.Location.Longitude
	}
	setOptional(p.Location.Value.Latitude, &latitude)
	setOptional(p.Location.Value.Longitude, &longitude)

	var problems []event.FieldError
	switch {
	case latitude == nil && longitude == nil: // both cleared, or none set: no location
		in.Location = nil
	case latitude == nil:
		problems = append(problems, event.FieldError{Field: "location.latitude", Message: "is required"})
	case longitude == nil:
		problems = append(problems, event.FieldError{Field: "location.longitude", Message: "is required"})
	default:
		// A new Location, not a change to the current one, which the stored venue shares.
		in.Location = &event.Location{Latitude: *latitude, Longitude: *longitude}
	}
	return problems
}

// setRequired applies a patch field to a required value. null sets the zero value ("" for text),
// which the service then reports as missing.
func setRequired[T any](f patchField[T], dst *T) {
	if f.Set {
		*dst = f.Value // the zero value when f.Null
	}
}

// setOptional applies a patch field to an optional value: null clears it, a value sets it.
func setOptional[T any](f patchField[T], dst **T) {
	switch {
	case f.Null:
		*dst = nil
	case f.Set:
		*dst = new(f.Value) // a new pointer, so a value shared with the stored venue isn't changed
	}
}
