package eventdb_test

import (
	"errors"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event/eventdb"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres"
)

// newDraftLayout stores a venue with a hall and one draft layout, and returns the layout.
func newDraftLayout(t *testing.T, pool *pgxpool.Pool) eventdb.Layout {
	t.Helper()
	return newLayout(t, eventdb.New(pool), newHall(t, pool), "Concert")
}

// createSection stores a section and returns it.
func createSection(t *testing.T, q *eventdb.Queries, params eventdb.CreateSectionParams) eventdb.Section {
	t.Helper()
	s, err := q.CreateSection(t.Context(), params)
	if err != nil {
		t.Fatalf("CreateSection %q: %v", params.Name, err)
	}
	return s
}

// standing and seated are the parameters of a valid section of each kind.
func standing(layoutID uuid.UUID, name string, position int32) eventdb.CreateSectionParams {
	return eventdb.CreateSectionParams{
		LayoutID: layoutID, Name: name, Kind: "standing", Capacity: new(int32(500)), Position: position,
	}
}

func seated(layoutID uuid.UUID, name string, position int32) eventdb.CreateSectionParams {
	return eventdb.CreateSectionParams{LayoutID: layoutID, Name: name, Kind: "seated", Position: position}
}

// sectionNames returns the names of the sections, in order.
func sectionNames(sections []eventdb.Section) []string {
	names := []string{}
	for _, s := range sections {
		names = append(names, s.Name)
	}
	return names
}

func TestCreateSection_StoresEveryColumn(t *testing.T) {
	pool := newPool(t)
	q := eventdb.New(pool)
	layout := newDraftLayout(t, pool)

	for name, params := range map[string]eventdb.CreateSectionParams{
		"standing, with a capacity": standing(layout.ID, "Floor", 1),
		"seated, without one":       seated(layout.ID, "Block 101", 2),
	} {
		t.Run(name, func(t *testing.T) {
			created, err := q.CreateSection(t.Context(), params)
			if err != nil {
				t.Fatalf("CreateSection: %v", err)
			}
			if created.ID == uuid.Nil() || created.Version != 1 {
				t.Errorf("id %v, version %d; want a generated id and version 1", created.ID, created.Version)
			}
			if created.CreatedAt.IsZero() || !created.UpdatedAt.Equal(created.CreatedAt) {
				t.Errorf("timestamps: created %v, updated %v, want both set and equal",
					created.CreatedAt, created.UpdatedAt)
			}

			// Read back separately: what RETURNING gave is what is stored.
			got, err := q.GetSection(t.Context(), created.ID)
			if err != nil {
				t.Fatalf("GetSection: %v", err)
			}
			if got.LayoutID != params.LayoutID || got.Name != params.Name || got.Kind != params.Kind ||
				!equalPtr(got.Capacity, params.Capacity) || got.Position != params.Position {
				t.Errorf("got %+v, want the columns of %+v", got, params)
			}
		})
	}
}

func TestCreateSection_BrokenRuleIsACheckViolation(t *testing.T) {
	pool := newPool(t)
	q := eventdb.New(pool)
	layout := newDraftLayout(t, pool)
	withCapacity := seated(layout.ID, "Block 101", 1)
	withCapacity.Capacity = new(int32(100))
	withoutCapacity := standing(layout.ID, "Floor", 1)
	withoutCapacity.Capacity = nil
	// Without a capacity, so only the kind's CHECK fails: a kind other than standing with a
	// capacity would break sections_capacity_only_standing too.
	otherKind := seated(layout.ID, "Box", 1)
	otherKind.Kind = "box"

	tests := []struct {
		name       string
		params     eventdb.CreateSectionParams
		constraint string
	}{
		// The service maps the first two to field errors; the others can't get past its checks.
		{"seated with a capacity", withCapacity, "sections_capacity_only_standing"},
		{"standing without a capacity", withoutCapacity, "sections_capacity_only_standing"},
		{"unknown kind", otherKind, "sections_kind_check"},
		{"blank name", standing(layout.ID, "  ", 1), "sections_name_check"},
		{"position 0", standing(layout.ID, "Floor", 0), "sections_position_check"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := q.CreateSection(t.Context(), tt.params)
			if !postgres.IsCheckViolation(err, tt.constraint) {
				t.Errorf("got %v, want a violation of %s", err, tt.constraint)
			}
		})
	}
}

// The section service turns these two errors into "layout not found" and "name taken" by the
// constraint's name, so the names are checked here, next to the query that breaks them.
func TestCreateSection_UnknownLayoutIsAForeignKeyViolation(t *testing.T) {
	q := eventdb.New(newPool(t))

	_, err := q.CreateSection(t.Context(), standing(uuid.New(), "Floor", 1))
	if !postgres.IsForeignKeyViolation(err, "sections_layout_id_fkey") {
		t.Errorf("got %v, want a violation of sections_layout_id_fkey", err)
	}
}

func TestCreateSection_NameTakenInTheLayoutIsAUniqueViolation(t *testing.T) {
	pool := newPool(t)
	q := eventdb.New(pool)
	layout := newDraftLayout(t, pool)
	other := newDraftLayout(t, pool)
	createSection(t, q, standing(layout.ID, "Floor", 1))

	// Sections have no status, so every section of the layout counts, whatever its kind.
	_, err := q.CreateSection(t.Context(), seated(layout.ID, "FLOOR", 2))
	if !postgres.IsUniqueViolation(err, "sections_layout_id_name_key") {
		t.Errorf("same layout, other case: got %v, want a violation of sections_layout_id_name_key", err)
	}

	// Names are unique per layout: another layout can have its own "Floor".
	if _, err := q.CreateSection(t.Context(), standing(other.ID, "Floor", 1)); err != nil {
		t.Errorf("same name in another layout: %v", err)
	}
}

func TestGetSection_UnknownIDIsErrNoRows(t *testing.T) {
	q := eventdb.New(newPool(t))

	for name, get := range map[string]func(*eventdb.Queries) error{
		"GetSection": func(q *eventdb.Queries) error {
			_, err := q.GetSection(t.Context(), uuid.New())
			return err
		},
		"GetSectionForUpdate": func(q *eventdb.Queries) error {
			_, err := q.GetSectionForUpdate(t.Context(), uuid.New())
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := get(q); !errors.Is(err, pgx.ErrNoRows) {
				t.Errorf("got %v, want pgx.ErrNoRows", err)
			}
		})
	}
}

func TestNextSectionPosition_IsOneAfterTheHighest(t *testing.T) {
	pool := newPool(t)
	q := eventdb.New(pool)
	layout := newDraftLayout(t, pool)
	other := newDraftLayout(t, pool)
	createSection(t, q, standing(other.ID, "Floor", 40)) // another layout's sections don't count

	steps := []struct {
		name string
		add  *eventdb.CreateSectionParams // a section to add first, if any
		want int32
	}{
		{"no sections", nil, 1},
		{"one at 1", new(standing(layout.ID, "Floor", 1)), 2},
		{"a gap: one at 5", new(seated(layout.ID, "Balcony", 5)), 6},
		{"a lower one doesn't change it", new(seated(layout.ID, "Stalls", 2)), 6},
	}
	for _, step := range steps {
		if step.add != nil {
			createSection(t, q, *step.add)
		}
		got, err := q.NextSectionPosition(t.Context(), layout.ID)
		if err != nil {
			t.Fatalf("%s: NextSectionPosition: %v", step.name, err)
		}
		if got != step.want {
			t.Errorf("%s: got %d, want %d", step.name, got, step.want)
		}
	}
}

func TestListSections_TheLayoutsSectionsByPositionThenName(t *testing.T) {
	pool := newPool(t)
	q := eventdb.New(pool)
	layout := newDraftLayout(t, pool)
	other := newDraftLayout(t, pool)
	// "Table 10" before "Table 2" by name, so positions give the order; equal positions are
	// listed by name ignoring case ("balcony" before "Box").
	createSection(t, q, seated(layout.ID, "Table 10", 3))
	createSection(t, q, seated(layout.ID, "Table 2", 2))
	createSection(t, q, standing(layout.ID, "Floor", 1))
	createSection(t, q, seated(layout.ID, "Box", 4))
	createSection(t, q, seated(layout.ID, "balcony", 4))
	createSection(t, q, standing(other.ID, "Pit", 1))

	sections, err := q.ListSections(t.Context(), layout.ID)
	if err != nil {
		t.Fatalf("ListSections: %v", err)
	}
	want := []string{"Floor", "Table 2", "Table 10", "balcony", "Box"}
	if got := sectionNames(sections); !equalNames(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestListSections_NoSectionsIsAnEmptySlice(t *testing.T) {
	pool := newPool(t)
	q := eventdb.New(pool)

	for name, layoutID := range map[string]uuid.UUID{
		"layout without sections": newDraftLayout(t, pool).ID,
		"unknown layout":          uuid.New(),
	} {
		t.Run(name, func(t *testing.T) {
			sections, err := q.ListSections(t.Context(), layoutID)
			if err != nil {
				t.Fatalf("ListSections: %v", err)
			}
			if sections == nil || len(sections) != 0 {
				t.Errorf("got %#v, want an empty slice, not nil", sections)
			}
		})
	}
}

func TestUpdateSection_ReplacesColumnsAndBumpsVersion(t *testing.T) {
	pool := newPool(t)
	q := eventdb.New(pool)
	created := createSection(t, q, standing(newDraftLayout(t, pool).ID, "Floor", 1))

	updated, err := q.UpdateSection(t.Context(), eventdb.UpdateSectionParams{
		ID: created.ID, Version: created.Version, Name: "Golden Circle", Capacity: new(int32(800)), Position: 3,
	})
	if err != nil {
		t.Fatalf("UpdateSection: %v", err)
	}

	if updated.Name != "Golden Circle" || *updated.Capacity != 800 || updated.Position != 3 {
		t.Errorf("got %+v, want the new name, capacity and position", updated)
	}
	if updated.LayoutID != created.LayoutID || updated.Kind != created.Kind {
		t.Errorf("layout %v, kind %q; want them unchanged (%v, %q)",
			updated.LayoutID, updated.Kind, created.LayoutID, created.Kind)
	}
	if updated.Version != created.Version+1 {
		t.Errorf("version: got %d, want %d", updated.Version, created.Version+1)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) || !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Errorf("created_at %v → %v, updated_at %v → %v; want created_at kept and updated_at later",
			created.CreatedAt, updated.CreatedAt, created.UpdatedAt, updated.UpdatedAt)
	}
}

// UpdateSection changes nothing unless the id and the version match. The service reads the
// section first (locked) to tell the cases apart, so here each one is only ErrNoRows.
func TestUpdateSection_NoMatchIsErrNoRows(t *testing.T) {
	pool := newPool(t)
	q := eventdb.New(pool)
	section := createSection(t, q, seated(newDraftLayout(t, pool).ID, "Balcony", 1))

	for name, params := range map[string]eventdb.UpdateSectionParams{
		"unknown id":    {ID: uuid.New(), Version: 1, Name: "Balcony", Position: 1},
		"other version": {ID: section.ID, Version: section.Version + 1, Name: "Balcony", Position: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := q.UpdateSection(t.Context(), params); !errors.Is(err, pgx.ErrNoRows) {
				t.Errorf("got %v, want pgx.ErrNoRows", err)
			}
		})
	}
}

func TestUpdateSection_BrokenRules(t *testing.T) {
	pool := newPool(t)
	q := eventdb.New(pool)
	layout := newDraftLayout(t, pool)
	createSection(t, q, standing(layout.ID, "Floor", 1))
	balcony := createSection(t, q, seated(layout.ID, "Balcony", 2))
	update := func(name string, capacity *int32) error {
		_, err := q.UpdateSection(t.Context(), eventdb.UpdateSectionParams{
			ID: balcony.ID, Version: balcony.Version, Name: name, Capacity: capacity, Position: 2,
		})
		return err
	}

	if err := update("floor", nil); !postgres.IsUniqueViolation(err, "sections_layout_id_name_key") {
		t.Errorf("name of another section: got %v, want a violation of sections_layout_id_name_key", err)
	}
	// The kind stays, so a seated section still can't get a capacity.
	if err := update("Balcony", new(int32(50))); !postgres.IsCheckViolation(err, "sections_capacity_only_standing") {
		t.Errorf("capacity on a seated section: got %v, want a violation of sections_capacity_only_standing", err)
	}
	// Changing only the case of its own name is not a clash with itself.
	if err := update("BALCONY", nil); err != nil {
		t.Errorf("rename to a different case of its own name: %v", err)
	}
}

func TestDeleteSection_DeletesOnlyTheExpectedVersion(t *testing.T) {
	pool := newPool(t)
	q := eventdb.New(pool)
	created := createSection(t, q, standing(newDraftLayout(t, pool).ID, "Floor", 1))

	steps := []struct {
		name    string
		version int32
		want    int64
	}{
		{"other version", created.Version + 1, 0},
		{"current version", created.Version, 1},
		{"already deleted", created.Version, 0},
	}
	for _, step := range steps {
		deleted, err := q.DeleteSection(t.Context(), eventdb.DeleteSectionParams{ID: created.ID, Version: step.version})
		if err != nil {
			t.Fatalf("%s: DeleteSection: %v", step.name, err)
		}
		if deleted != step.want {
			t.Errorf("%s: deleted %d rows, want %d", step.name, deleted, step.want)
		}
	}
	if _, err := q.GetSection(t.Context(), created.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetSection after delete: got %v, want pgx.ErrNoRows", err)
	}
}

// GetLayoutForShare is how a section change checks its layout is a draft, as GetHallForShare is
// for layouts: a second shared lock is granted at once (two sections of the layout changed at
// once), but activating the layout waits until the first transaction ends. That is what keeps a
// layout from being activated while its last section is deleted.
func TestGetLayoutForShare_ActivatingWaitsUntilTheCommit(t *testing.T) {
	pool := newPool(t)
	layout := newDraftLayout(t, pool)
	addSection(t, pool, layout.ID, "Floor")

	holder, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = holder.Rollback(t.Context()) }() // a no-op once committed
	if _, err := eventdb.New(holder).GetLayoutForShare(t.Context(), layout.ID); err != nil {
		t.Fatalf("GetLayoutForShare: %v", err)
	}

	// other runs fn in its own transaction that waits at most 100 ms for a lock.
	other := func(fn func(q *eventdb.Queries) error) error {
		return postgres.WithTx(t.Context(), pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `SET LOCAL lock_timeout = '100ms'`); err != nil {
				return err
			}
			return fn(eventdb.New(tx))
		})
	}
	activate := func(q *eventdb.Queries) error {
		_, err := q.SetLayoutStatus(t.Context(), eventdb.SetLayoutStatusParams{
			ID: layout.ID, Version: layout.Version, FromStatus: "draft", ToStatus: "active",
		})
		return err
	}

	err = other(func(q *eventdb.Queries) error {
		_, err := q.GetLayoutForShare(t.Context(), layout.ID)
		return err
	})
	if err != nil {
		t.Errorf("a second shared lock: got %v, want it granted at once", err)
	}

	var pgErr *pgconn.PgError
	if err := other(activate); !errors.As(err, &pgErr) || pgErr.Code != lockNotAvailable {
		t.Errorf("activate while the shared lock is held: got %v, want SQLSTATE %s (it has to wait)",
			err, lockNotAvailable)
	}

	if err := holder.Commit(t.Context()); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := other(activate); err != nil {
		t.Errorf("activate after the commit: %v", err)
	}
}

func TestGetLayoutForShare_UnknownIDIsErrNoRows(t *testing.T) {
	q := eventdb.New(newPool(t))

	if _, err := q.GetLayoutForShare(t.Context(), uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("got %v, want pgx.ErrNoRows", err)
	}
}
