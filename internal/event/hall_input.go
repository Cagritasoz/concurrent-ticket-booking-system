package event

import (
	"fmt"
	"math"
)

// HallInput is every field of Hall an admin can edit, like VenueInput for venues. VenueID isn't
// in it: it is chosen once, by the venue the hall is created in, and never changes.
type HallInput struct {
	Name        string
	Description *string
	Capacity    *int
}

// input returns the hall's changeable values, the starting point of an update, like
// Venue.input.
func (h Hall) input() HallInput {
	return HallInput{Name: h.Name, Description: h.Description, Capacity: h.Capacity}
}

// clean returns the input as it will be stored, or a *ValidationError listing every broken rule.
// Text is handled as in VenueInput.clean; the limits match the CHECKs in migration 2.
func (in HallInput) clean() (HallInput, error) {
	var problems fieldErrors

	in.Name = problems.required("name", in.Name, maxNameLength)
	in.Description = problems.optional("description", in.Description, maxDescriptionLength)
	// The column is a Postgres integer, so the largest capacity it holds is MaxInt32.
	if c := in.Capacity; c != nil && (*c < 1 || *c > math.MaxInt32) {
		problems.add("capacity", fmt.Sprintf("must be between 1 and %d", math.MaxInt32))
	}

	if len(problems) > 0 {
		return HallInput{}, &ValidationError{Fields: problems}
	}
	return in, nil
}
