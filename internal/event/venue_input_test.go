package event

// These tests are in package event itself (not event_test), so they can call the unexported
// clean directly. They need no database: validation is plain Go.

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

// validInput returns an input that passes every rule, for tests to break one field at a time.
func validInput() VenueInput {
	return VenueInput{
		Name: "Volkswagen Arena",
		Address: Address{
			Line1:       "Huzur Mah. Azerbaycan Cad. 1",
			City:        "Istanbul",
			CountryCode: "TR",
		},
		TimeZone: "Europe/Istanbul",
	}
}

func TestClean_AcceptsValidInput(t *testing.T) {
	in := validInput()
	in.Description = new("Box office opens two hours before each event.")
	in.Address.Line2 = new("Gate B")
	in.Address.Region = new("Sariyer")
	in.Address.PostalCode = new("34396")
	in.Location = &Location{Latitude: -90, Longitude: 180} // the limits themselves are allowed
	in.Capacity = new(math.MaxInt32)

	got, err := in.clean()
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Errorf("valid input was changed:\ngot  %+v\nwant %+v", got, in)
	}
}

func TestClean_TrimsTextAndTurnsBlankOptionalTextIntoNil(t *testing.T) {
	in := VenueInput{
		Name:        "  Volkswagen Arena\n",
		Description: new("   "),
		Address: Address{
			Line1:       "\tHuzur Mah. 1 ",
			Line2:       new(""),
			City:        " Istanbul ",
			Region:      new(" Sariyer "),
			CountryCode: " TR ",
		},
		TimeZone: " Europe/Istanbul ",
	}
	original := *in.Address.Region

	got, err := in.clean()
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	want := VenueInput{
		Name: "Volkswagen Arena",
		Address: Address{
			Line1:       "Huzur Mah. 1",
			City:        "Istanbul",
			Region:      new("Sariyer"),
			CountryCode: "TR",
		},
		TimeZone: "Europe/Istanbul",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if *in.Address.Region != original {
		t.Errorf("clean changed the caller's input: region is now %q", *in.Address.Region)
	}
}

func TestClean_RejectsBrokenRules(t *testing.T) {
	tests := []struct {
		name    string
		change  func(in *VenueInput)
		field   string
		message string // a part of the expected message
	}{
		{"blank name", func(in *VenueInput) { in.Name = "  " }, "name", "is required"},
		{"name too long", func(in *VenueInput) { in.Name = strings.Repeat("a", 201) }, "name", "at most 200 characters"},
		{"description too long", func(in *VenueInput) { in.Description = new(strings.Repeat("a", 5001)) }, "description", "at most 5000"},
		{"missing address line 1", func(in *VenueInput) { in.Address.Line1 = "" }, "address.line1", "is required"},
		{"address line 2 too long", func(in *VenueInput) { in.Address.Line2 = new(strings.Repeat("a", 201)) }, "address.line2", "at most 200"},
		{"missing city", func(in *VenueInput) { in.Address.City = "" }, "address.city", "is required"},
		{"region too long", func(in *VenueInput) { in.Address.Region = new(strings.Repeat("a", 101)) }, "address.region", "at most 100"},
		{"postal code too long", func(in *VenueInput) { in.Address.PostalCode = new(strings.Repeat("1", 21)) }, "address.postal_code", "at most 20"},
		{"missing country code", func(in *VenueInput) { in.Address.CountryCode = "" }, "address.country_code", "ISO 3166-1"},
		{"lowercase country code", func(in *VenueInput) { in.Address.CountryCode = "tr" }, "address.country_code", "ISO 3166-1"},
		{"three-letter country code", func(in *VenueInput) { in.Address.CountryCode = "TUR" }, "address.country_code", "ISO 3166-1"},
		{"latitude out of range", func(in *VenueInput) { in.Location = &Location{Latitude: 90.5} }, "location.latitude", "between -90 and 90"},
		{"latitude NaN", func(in *VenueInput) { in.Location = &Location{Latitude: math.NaN()} }, "location.latitude", "between -90 and 90"},
		{"longitude out of range", func(in *VenueInput) { in.Location = &Location{Longitude: -180.5} }, "location.longitude", "between -180 and 180"},
		{"missing time zone", func(in *VenueInput) { in.TimeZone = "" }, "time_zone", "IANA"},
		{"unknown time zone", func(in *VenueInput) { in.TimeZone = "Mars/Olympus_Mons" }, "time_zone", "IANA"},
		{"Local is not a time zone", func(in *VenueInput) { in.TimeZone = "Local" }, "time_zone", "IANA"},
		{"zero capacity", func(in *VenueInput) { in.Capacity = new(0) }, "capacity", "between 1 and"},
		{"capacity above int32", func(in *VenueInput) { in.Capacity = new(math.MaxInt32 + 1) }, "capacity", "between 1 and"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			tt.change(&in)

			_, err := in.clean()

			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("got %v, want a *ValidationError", err)
			}
			if len(invalid.Fields) != 1 {
				t.Fatalf("got %d problems, want 1: %v", len(invalid.Fields), invalid.Fields)
			}
			got := invalid.Fields[0]
			if got.Field != tt.field || !strings.Contains(got.Message, tt.message) {
				t.Errorf("got %s: %q, want %s: ...%s...", got.Field, got.Message, tt.field, tt.message)
			}
		})
	}
}

func TestClean_ReportsEveryProblemAtOnce(t *testing.T) {
	in := VenueInput{Capacity: new(-1)} // everything required is missing, and capacity is wrong

	_, err := in.clean()

	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("got %v, want a *ValidationError", err)
	}
	var fields []string
	for _, f := range invalid.Fields {
		fields = append(fields, f.Field)
	}
	want := []string{"name", "address.line1", "address.city", "address.country_code", "time_zone", "capacity"}
	if !reflect.DeepEqual(fields, want) {
		t.Errorf("fields: got %v, want %v", fields, want)
	}
	if msg := err.Error(); !strings.HasPrefix(msg, "invalid input: name: is required; ") {
		t.Errorf("Error(): got %q", msg)
	}
}
