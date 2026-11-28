package event

import (
	"fmt"
	"math"
)

const (
	// maxSectionNameLength matches migration 4's CHECK: shorter than other names, since a ticket
	// prints its section.
	maxSectionNameLength = 100
	// maxSectionPosition is the highest position a section can have. The column could hold
	// MaxInt32, but then the next section's position (one after the highest) wouldn't fit; no
	// real layout has anywhere near this many sections.
	maxSectionPosition = 10000
)

// SectionInput is every field of Section an admin sets, like LayoutInput for layouts. LayoutID
// isn't in it: it is chosen once, by the layout the section is created in. Kind is set on
// create and can't change afterwards (Update refuses a different one). Position is optional on
// create, where nil means "after the last section".
type SectionInput struct {
	Name     string
	Kind     SectionKind
	Capacity *int
	Position *int
}

// input returns the section's values, the starting point of an update, like Layout.input.
func (s Section) input() SectionInput {
	return SectionInput{Name: s.Name, Kind: s.Kind, Capacity: s.Capacity, Position: new(s.Position)}
}

// clean returns a new section's input as it will be stored, or a *ValidationError listing every
// broken rule. Text is handled as in VenueInput.clean, and the limits match migration 4's
// CHECKs: a standing section has a capacity and a seated one none
// (sections_capacity_only_standing), checked here so the caller gets a field error instead of a
// database error. A missing position is fine: the service puts the section after the last one.
func (in SectionInput) clean() (SectionInput, error) {
	return in.check(nil)
}

// cleanUpdate is clean for a changed section whose stored kind is stored: the kind must still be
// stored, and the position must be set.
func (in SectionInput) cleanUpdate(stored SectionKind) (SectionInput, error) {
	return in.check(&stored)
}

// check holds the rules of clean and cleanUpdate, so their problems come in one order (name,
// kind, capacity, position) whichever is called. stored is the kind of the section being
// changed, or nil for a new one.
func (in SectionInput) check(stored *SectionKind) (SectionInput, error) {
	var problems fieldErrors

	in.Name = problems.required("name", in.Name, maxSectionNameLength)
	if stored != nil && in.Kind != *stored {
		problems.add("kind", "can't be changed: delete the section and create another")
		in.Kind = *stored // check the capacity against the kind the section keeps
	}
	switch in.Kind {
	case SectionStanding:
		switch c := in.Capacity; {
		case c == nil:
			problems.add("capacity", "is required for a standing section")
		case *c < 1 || *c > math.MaxInt32:
			problems.add("capacity", fmt.Sprintf("must be between 1 and %d", math.MaxInt32))
		}
	case SectionSeated:
		if in.Capacity != nil {
			problems.add("capacity", "must be left out for a seated section: its seats are its capacity")
		}
	default:
		problems.add("kind", fmt.Sprintf("must be %s or %s", SectionSeated, SectionStanding))
	}
	switch p := in.Position; {
	case p == nil && stored != nil:
		problems.add("position", "is required")
	case p != nil && (*p < 1 || *p > maxSectionPosition):
		problems.add("position", fmt.Sprintf("must be between 1 and %d", maxSectionPosition))
	}

	if len(problems) > 0 {
		return SectionInput{}, &ValidationError{Fields: problems}
	}
	return in, nil
}
