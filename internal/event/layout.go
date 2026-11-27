package event

import (
	"errors"
	"time"
	"uuid"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event/eventdb"
)

// The layout's errors. httpapi turns ErrLayoutNotFound into 404 Not Found and the others into
// 409 Conflict.
var (
	// ErrLayoutNotFound: no layout has the requested id.
	ErrLayoutNotFound = errors.New("layout not found")
	// ErrLayoutArchived: the layout is archived, so it can't be changed, activated or archived
	// again; restore it first.
	ErrLayoutArchived = errors.New("layout is archived")
	// ErrLayoutNotArchived: the layout is a draft or active, so there is nothing to restore.
	ErrLayoutNotArchived = errors.New("layout is not archived")
	// ErrLayoutAlreadyActive: only a draft can be activated, and this layout already is active.
	ErrLayoutAlreadyActive = errors.New("layout is already active")
	// ErrLayoutNotActive: only an active layout can be deactivated, and this one is a draft.
	ErrLayoutNotActive = errors.New("layout is not active")
	// ErrLayoutHasNoSections: a layout needs at least one section to be activated, since an event
	// is sold in its sections.
	ErrLayoutHasNoSections = errors.New("layout has no sections")
	// ErrLayoutNameTaken: another layout of the hall that isn't archived (a draft or an active
	// one) already has this name, ignoring case.
	ErrLayoutNameTaken = errors.New("layout name is taken")
)

// LayoutStatus is where a layout is in its life. Layouts are created as drafts.
//
//	draft  -> Activate   -> active
//	active -> Deactivate -> draft
//	draft  -> Archive    -> archived
//	active -> Archive    -> archived
//	archived -> Restore  -> draft
//
// A draft is being built: its sections and seats are added over many requests, and no event can
// use it yet. Activating it (it needs at least one section) lets events be created in it. Its
// sections and seats change only while it is a draft, so an active layout is always one that
// passed Activate's checks; to change one, Deactivate takes it back to a draft, and Activate
// checks it again.
// Restoring always brings a layout back as a draft, whatever it was before it was archived: it is
// only offered for events again after a deliberate Activate, which runs its checks again.
//
// A layout of an archived hall, or of a hall of an archived venue, counts as archived whatever its
// own status says, as a hall of an archived venue does. Its own status isn't written when the hall
// or venue is archived, so restoring them brings the layout back exactly as it was.
type LayoutStatus string

const (
	LayoutDraft    LayoutStatus = "draft"
	LayoutActive   LayoutStatus = "active"
	LayoutArchived LayoutStatus = "archived"
)

// Layout is one way a hall is set up ("Concert – standing floor", "Basketball"). Every event
// takes place in one layout. Its capacity isn't stored: it is the sum of its sections'. Like
// Hall, it is filled from its table's row by toLayout.
type Layout struct {
	ID          uuid.UUID
	HallID      uuid.UUID // a layout never moves to another hall
	Name        string
	Description *string // e.g. which areas are open or closed in this setup
	Status      LayoutStatus
	Version     int // as Venue.Version
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// toLayout converts a row of the layouts table into a Layout.
func toLayout(row eventdb.Layout) Layout {
	return Layout{
		ID:          row.ID,
		HallID:      row.HallID,
		Name:        row.Name,
		Description: row.Description,
		Status:      LayoutStatus(row.Status), // the table's CHECK allows only our three values
		Version:     int(row.Version),
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}
