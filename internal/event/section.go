package event

import (
	"errors"
	"time"
	"uuid"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event/eventdb"
)

// The section's errors, and the layout error only section changes return. httpapi turns
// ErrSectionNotFound into 404 Not Found and the others into 409 Conflict.
var (
	// ErrSectionNotFound: no section has the requested id.
	ErrSectionNotFound = errors.New("section not found")
	// ErrSectionNameTaken: another section of the layout already has this name, ignoring case.
	// Sections have no status, so every section of the layout counts.
	ErrSectionNameTaken = errors.New("section name is taken")
	// ErrLayoutNotDraft: the layout is active, and its sections change only while it is a draft;
	// deactivate it first. (An archived layout is ErrLayoutArchived.)
	ErrLayoutNotDraft = errors.New("layout is not a draft")
)

// SectionKind is what a section holds: seats, or standing places counted by a capacity. It is
// chosen when the section is created and never changes; to change it, delete the section and
// create another.
type SectionKind string

const (
	SectionSeated   SectionKind = "seated"   // its seats are rows of the seats table (M1 step 7)
	SectionStanding SectionKind = "standing" // general admission: no seats, only a capacity
)

// Section is a part of a layout: "Block 101" (seated), "Floor" (standing). It has no status: it
// is part of its layout's definition, changed only while the layout is a draft. It has its own
// version, so changing a section doesn't change its layout's. Like Layout, it is filled from its
// table's row by toSection.
type Section struct {
	ID       uuid.UUID
	LayoutID uuid.UUID // a section never moves to another layout
	Name     string
	Kind     SectionKind
	// How many people a standing section holds; nil for a seated one, which holds as many as it
	// has seats.
	Capacity *int
	// Where the section comes in its layout's list, 1 first. Not unique: equal positions are
	// listed by name.
	Position  int
	Version   int // as Venue.Version
	CreatedAt time.Time
	UpdatedAt time.Time
}

// toSection converts a row of the sections table into a Section.
func toSection(row eventdb.Section) Section {
	s := Section{
		ID:        row.ID,
		LayoutID:  row.LayoutID,
		Name:      row.Name,
		Kind:      SectionKind(row.Kind), // the table's CHECK allows only our two values
		Position:  int(row.Position),
		Version:   int(row.Version),
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
	if row.Capacity != nil {
		s.Capacity = new(int(*row.Capacity)) // int32 → int always fits
	}
	return s
}
