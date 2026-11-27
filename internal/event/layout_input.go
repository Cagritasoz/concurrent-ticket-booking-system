package event

// LayoutInput is every field of Layout an admin can edit, like HallInput for halls. HallID isn't
// in it: it is chosen once, by the hall the layout is created in, and never changes. Neither is
// Status: it changes only through Activate, Archive and Restore.
type LayoutInput struct {
	Name        string
	Description *string
}

// input returns the layout's changeable values, the starting point of an update, like
// Hall.input.
func (l Layout) input() LayoutInput {
	return LayoutInput{Name: l.Name, Description: l.Description}
}

// clean returns the input as it will be stored, or a *ValidationError listing every broken rule.
// Text is handled as in VenueInput.clean; the limits match the CHECKs in migration 3, the same
// as a venue's and a hall's.
func (in LayoutInput) clean() (LayoutInput, error) {
	var problems fieldErrors

	in.Name = problems.required("name", in.Name, maxNameLength)
	in.Description = problems.optional("description", in.Description, maxDescriptionLength)

	if len(problems) > 0 {
		return LayoutInput{}, &ValidationError{Fields: problems}
	}
	return in, nil
}
