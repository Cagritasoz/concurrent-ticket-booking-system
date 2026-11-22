package migrations_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres/pgtest"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/migrations"
)

// fileName is the naming rule for migration files: six digits, a lowercase name, up or down.
var fileName = regexp.MustCompile(`^(\d{6})_([a-z0-9_]+)\.(up|down)\.sql$`)

// latestVersion checks the naming rules for every file and returns the highest version.
// Versions must start at 1 and have no gaps, and each needs exactly one up and one down file with
// the same name. These rules run without a database, so they also catch mistakes in future files.
func latestVersion(t *testing.T) uint {
	t.Helper()
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}

	names := map[uint]string{}        // version → name
	directions := map[uint][]string{} // version → "up", "down"
	for _, entry := range entries {
		match := fileName.FindStringSubmatch(entry.Name())
		if match == nil {
			t.Errorf("%s: want NNNNNN_name.up.sql or NNNNNN_name.down.sql (lowercase name)", entry.Name())
			continue
		}
		number, _ := strconv.ParseUint(match[1], 10, 64) // six digits always parse
		version := uint(number)
		if name, seen := names[version]; seen && name != match[2] {
			t.Errorf("version %d has two names: %s and %s", version, name, match[2])
		}
		names[version] = match[2]
		directions[version] = append(directions[version], match[3])
	}

	for version := uint(1); version <= uint(len(names)); version++ {
		got := directions[version]
		slices.Sort(got)
		if !slices.Equal(got, []string{"down", "up"}) {
			t.Errorf("version %d: got files %v, want one up and one down (versions start at 1, no gaps)", version, got)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	return uint(len(names))
}

func TestFiles_FollowTheNamingRules(t *testing.T) {
	if latestVersion(t) == 0 {
		t.Fatal("no migrations found")
	}
}

func connect(t *testing.T, databaseURL string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// tableCount counts the tables other than schema_migrations.
func tableCount(t *testing.T, conn *pgx.Conn) int {
	t.Helper()
	var count int
	err := conn.QueryRow(t.Context(),
		`SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count tables: %v", err)
	}
	return count
}

// Every migration must apply, roll back cleanly and apply again. This catches a down file that
// forgets something, which otherwise shows up only when someone needs it in a hurry.
func TestMigrations_UpDownUp(t *testing.T) {
	latest := latestVersion(t)
	databaseURL := pgtest.NewDatabase(t)
	conn := connect(t, databaseURL)

	migrator, err := postgres.NewMigrator(t.Context(), databaseURL, migrations.Files)
	if err != nil {
		t.Fatalf("NewMigrator: %v", err)
	}
	t.Cleanup(func() { _ = migrator.Close() })

	if err := migrator.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if version, dirty, _ := migrator.Version(); version != latest || dirty {
		t.Fatalf("after Up: version %d, dirty %t; want %d, not dirty", version, dirty, latest)
	}

	for version := latest; version > 0; version-- {
		if err := migrator.Down(); err != nil {
			t.Fatalf("Down from version %d: %v", version, err)
		}
	}
	if count := tableCount(t, conn); count != 0 {
		t.Errorf("after rolling back everything, %d tables are left", count)
	}

	if err := migrator.Up(); err != nil {
		t.Fatalf("Up again: %v", err)
	}
	if version, _, _ := migrator.Version(); version != latest {
		t.Fatalf("after Up again: version %d, want %d", version, latest)
	}
}

// row holds the columns of one test insert. Start from validVenue (or validHall, validLayout,
// validSection) and change what the case needs.
type row map[string]any

func validVenue() row {
	return row{
		"name":          "Zorlu PSM",
		"address_line1": "Levazım, Koru Sokağı No:2",
		"city":          "Istanbul",
		"country_code":  "TR",
		"timezone":      "Europe/Istanbul",
	}
}

func (r row) with(column string, value any) row {
	out := row{}
	for k, val := range r {
		out[k] = val
	}
	out[column] = value
	return out
}

// insert adds r to table and returns the new row's id.
func insert(ctx context.Context, conn *pgx.Conn, table string, r row) (string, error) {
	columns := make([]string, 0, len(r))
	for column := range r {
		columns = append(columns, column)
	}
	slices.Sort(columns) // map order is random; sorting keeps the SQL the same every run

	placeholders := make([]string, len(columns))
	values := make([]any, len(columns))
	for i, column := range columns {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		values[i] = r[column]
	}
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING id::text",
		table, strings.Join(columns, ", "), strings.Join(placeholders, ", "))
	var id string
	err := conn.QueryRow(ctx, sql, values...).Scan(&id)
	return id, err
}

// mustInsert is insert for rows a test needs in order to start, such as a section's layout.
func mustInsert(t *testing.T, conn *pgx.Conn, table string, r row) string {
	t.Helper()
	id, err := insert(t.Context(), conn, table, r)
	if err != nil {
		t.Fatalf("insert into %s: %v", table, err)
	}
	return id
}

// Postgres error codes (SQLSTATE) for the rules the tables enforce.
const (
	notNullViolation    = "23502"
	foreignKeyViolation = "23503"
	uniqueViolation     = "23505"
	checkViolation      = "23514"
	undefinedColumn     = "42703"
)

// ruleCase is one insert in a TestXxx_TableRules test.
type ruleCase struct {
	name     string
	row      row
	wantCode string // "" means the insert must succeed
}

// checkRule inserts tc.row into table and checks that Postgres accepts it, or refuses it with
// tc.wantCode.
func checkRule(t *testing.T, conn *pgx.Conn, table string, tc ruleCase) {
	t.Helper()
	_, err := insert(t.Context(), conn, table, tc.row)
	wantError(t, err, tc.wantCode)
}

// wantError checks that err is nil when code is "", and otherwise a Postgres error with that code.
func wantError(t *testing.T, err error, code string) {
	t.Helper()
	if code == "" {
		if err != nil {
			t.Fatalf("got %v, want no error", err)
		}
		return
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("got %v, want Postgres error %s", err, code)
	}
}

func TestVenues_TableRules(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))

	tests := []ruleCase{
		{"required columns only", validVenue(), ""},
		{"every column", validVenue().
			with("description", "Box office opens two hours before each show.").
			with("address_line2", "Hall B").
			with("region", "Beşiktaş").
			with("postal_code", "34340").
			with("latitude", 41.066740).
			with("longitude", 29.016090).
			with("status", "archived").
			with("version", 7), ""},
		{"missing name", validVenue().with("name", nil), notNullViolation},
		{"blank name", validVenue().with("name", "   "), checkViolation},
		{"name longer than 200", validVenue().with("name", strings.Repeat("a", 201)), checkViolation},
		{"empty description", validVenue().with("description", ""), checkViolation},
		{"blank optional address line", validVenue().with("address_line2", " "), checkViolation},
		{"missing city", validVenue().with("city", nil), notNullViolation},
		{"lowercase country code", validVenue().with("country_code", "tr"), checkViolation},
		{"three-letter country code", validVenue().with("country_code", "TUR"), checkViolation},
		{"missing time zone", validVenue().with("timezone", nil), notNullViolation},
		{"blank time zone", validVenue().with("timezone", ""), checkViolation},
		{"latitude without longitude", validVenue().with("latitude", 41.0), checkViolation},
		{"latitude above 90", validVenue().with("latitude", 90.5).with("longitude", 29.0), checkViolation},
		{"longitude below -180", validVenue().with("latitude", 41.0).with("longitude", -180.5), checkViolation},
		{"unknown status", validVenue().with("status", "closed"), checkViolation},
		{"missing status", validVenue().with("status", nil), notNullViolation},
		{"zero version", validVenue().with("version", 0), checkViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkRule(t, conn, "venues", tc)
		})
	}
}

func TestVenues_DefaultsFillIDStatusVersionAndTimestamps(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	mustInsert(t, conn, "venues", validVenue())

	var id, status string
	var version int
	var sameTimes bool
	err := conn.QueryRow(t.Context(),
		`SELECT id::text, status, version, created_at = updated_at FROM venues`,
	).Scan(&id, &status, &version, &sameTimes)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Errorf("id %q is not a random (version 4) UUID", id)
	}
	if status != "active" || version != 1 {
		t.Errorf("status %q, version %d; want active, 1", status, version)
	}
	if !sameTimes {
		t.Error("created_at and updated_at should be equal for a new row")
	}
}

func validHall(venueID string) row {
	return row{"venue_id": venueID, "name": "Turkcell Stage"}
}

func validLayout(hallID string) row {
	return row{"hall_id": hallID, "name": "Concert – standing floor"}
}

func validSection(layoutID string) row {
	return row{"layout_id": layoutID, "name": "Floor", "kind": "standing", "capacity": 3000}
}

// unknownID is a well-formed id that no row has.
const unknownID = "5d8d4a43-0a8e-4f4c-9b59-0c6a1f3f8e21"

func TestHalls_TableRules(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	venueID := mustInsert(t, conn, "venues", validVenue())

	tests := []ruleCase{
		{"required columns only", validHall(venueID), ""},
		{"every column", validHall(venueID).
			with("name", "Studio").
			with("description", "Second floor, entrance from the garden.").
			with("capacity", 900).
			with("status", "archived").
			with("version", 3), ""},
		{"missing venue", validHall(venueID).with("venue_id", nil), notNullViolation},
		{"unknown venue", validHall(venueID).with("venue_id", unknownID), foreignKeyViolation},
		{"blank name", validHall(venueID).with("name", " "), checkViolation},
		{"name longer than 200", validHall(venueID).with("name", strings.Repeat("a", 201)), checkViolation},
		{"empty description", validHall(venueID).with("description", ""), checkViolation},
		{"zero capacity", validHall(venueID).with("capacity", 0), checkViolation},
		{"draft status", validHall(venueID).with("status", "draft"), checkViolation},
		{"missing status", validHall(venueID).with("status", nil), notNullViolation},
		{"zero version", validHall(venueID).with("version", 0), checkViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkRule(t, conn, "halls", tc)
		})
	}
}

func TestLayouts_TableRules(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	hallID := mustInsert(t, conn, "halls", validHall(mustInsert(t, conn, "venues", validVenue())))

	tests := []ruleCase{
		{"required columns only", validLayout(hallID), ""},
		{"every column", validLayout(hallID).
			with("name", "Basketball").
			with("description", "Floor seating removed, retractable stands out.").
			with("status", "active").
			with("version", 5), ""},
		{"archived", validLayout(hallID).with("name", "Theatre setup").with("status", "archived"), ""},
		{"missing hall", validLayout(hallID).with("hall_id", nil), notNullViolation},
		{"unknown hall", validLayout(hallID).with("hall_id", unknownID), foreignKeyViolation},
		{"blank name", validLayout(hallID).with("name", " "), checkViolation},
		{"name longer than 200", validLayout(hallID).with("name", strings.Repeat("a", 201)), checkViolation},
		{"empty description", validLayout(hallID).with("description", ""), checkViolation},
		{"unknown status", validLayout(hallID).with("status", "published"), checkViolation},
		{"missing status", validLayout(hallID).with("status", nil), notNullViolation},
		{"zero version", validLayout(hallID).with("version", 0), checkViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkRule(t, conn, "layouts", tc)
		})
	}
}

func TestSections_TableRules(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	layoutID := newTree(t, conn).layout
	seated := validSection(layoutID).with("name", "Block 101").with("kind", "seated").with("capacity", nil)

	tests := []ruleCase{
		{"standing with a capacity", validSection(layoutID).with("name", "Pit"), ""},
		{"seated without a capacity", seated, ""},
		{"seated with a capacity", seated.with("capacity", 500), checkViolation},
		{"standing without a capacity", validSection(layoutID).with("capacity", nil), checkViolation},
		{"zero capacity", validSection(layoutID).with("capacity", 0), checkViolation},
		{"unknown kind", validSection(layoutID).with("kind", "vip"), checkViolation},
		{"missing kind", validSection(layoutID).with("kind", nil), notNullViolation},
		{"blank name", validSection(layoutID).with("name", ""), checkViolation},
		{"name longer than 100", validSection(layoutID).with("name", strings.Repeat("a", 101)), checkViolation},
		{"missing layout", validSection(layoutID).with("layout_id", nil), notNullViolation},
		{"unknown layout", validSection(layoutID).with("layout_id", unknownID), foreignKeyViolation},
		{"a later version", validSection(layoutID).with("name", "Standing left").with("version", 2), ""},
		{"zero version", validSection(layoutID).with("version", 0), checkViolation},
		{"a status", validSection(layoutID).with("status", "active"), undefinedColumn},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkRule(t, conn, "sections", tc)
		})
	}
}

// Each level's names are unique within its parent, ignoring case, and free to repeat under
// another parent. The services will turn the violation into their own error by the index name,
// so the test checks the name too.
func TestNamesAreUniquePerParentIgnoringCase(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))

	tests := []struct {
		table  string
		index  string
		parent string // the parent's table
		child  func(parentID string) row
	}{
		{"halls", "halls_venue_id_name_key", "venues", validHall},
		{"layouts", "layouts_hall_id_name_key", "halls", validLayout},
		{"sections", "sections_layout_id_name_key", "layouts", validSection},
	}
	for _, tc := range tests {
		t.Run(tc.table, func(t *testing.T) {
			parentA, parentB := newTree(t, conn).id(tc.parent), newTree(t, conn).id(tc.parent)
			mustInsert(t, conn, tc.table, tc.child(parentA).with("name", "Main"))

			_, err := insert(t.Context(), conn, tc.table, tc.child(parentA).with("name", "MAIN"))
			if !postgres.IsUniqueViolation(err, tc.index) {
				t.Errorf("same name, same parent: got %v, want a %s violation", err, tc.index)
			}
			if _, err := insert(t.Context(), conn, tc.table, tc.child(parentB).with("name", "Main")); err != nil {
				t.Errorf("same name, another parent: %v", err)
			}
		})
	}
}

// An archived hall or layout gives up its name: the unique indexes are partial (WHERE status <>
// 'archived'), so a new one can take the name, and several archived ones can share it.
func TestArchivedHallsAndLayoutsFreeTheirName(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))

	tests := []struct {
		table  string
		index  string
		parent string
		child  func(parentID string) row
	}{
		{"halls", "halls_venue_id_name_key", "venues", validHall},
		{"layouts", "layouts_hall_id_name_key", "halls", validLayout},
	}
	for _, tc := range tests {
		t.Run(tc.table, func(t *testing.T) {
			parentID := newTree(t, conn).id(tc.parent)
			archived := tc.child(parentID).with("name", "Main").with("status", "archived")
			mustInsert(t, conn, tc.table, archived)
			mustInsert(t, conn, tc.table, archived.with("name", "MAIN"))

			mustInsert(t, conn, tc.table, tc.child(parentID).with("name", "main"))
			_, err := insert(t.Context(), conn, tc.table, tc.child(parentID).with("name", "Main"))
			if !postgres.IsUniqueViolation(err, tc.index) {
				t.Errorf("second name in use: got %v, want a %s violation", err, tc.index)
			}
		})
	}
}

// New rows below a venue start like a new venue: version 1, halls active, layouts as drafts.
func TestTheVenueTreeDefaultsStatusAndVersion(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	tr := newTree(t, conn)

	tests := []struct {
		table      string
		wantStatus string // "" for a table without a status
	}{
		{"halls", "active"},
		{"layouts", "draft"},
		{"sections", ""},
	}
	for _, tc := range tests {
		t.Run(tc.table, func(t *testing.T) {
			// tc.table is one of treeTables, never input, so building the SQL from it is safe.
			where := ` FROM ` + tc.table + ` WHERE id = $1`
			var version int
			if err := conn.QueryRow(t.Context(), `SELECT version`+where, tr.id(tc.table)).Scan(&version); err != nil {
				t.Fatalf("select version: %v", err)
			}
			if version != 1 {
				t.Errorf("version %d, want 1", version)
			}
			if tc.wantStatus == "" {
				return
			}
			var status string
			if err := conn.QueryRow(t.Context(), `SELECT status`+where, tr.id(tc.table)).Scan(&status); err != nil {
				t.Fatalf("select status: %v", err)
			}
			if status != tc.wantStatus {
				t.Errorf("status %q, want %q", status, tc.wantStatus)
			}
		})
	}
}

// tree holds the ids of one venue with one hall, one layout and one section.
type tree struct {
	venue, hall, layout, section string
}

// treeTables lists the levels of a tree from the top down.
var treeTables = []string{"venues", "halls", "layouts", "sections"}

func newTree(t *testing.T, conn *pgx.Conn) tree {
	t.Helper()
	var tr tree
	tr.venue = mustInsert(t, conn, "venues", validVenue())
	tr.hall = mustInsert(t, conn, "halls", validHall(tr.venue))
	tr.layout = mustInsert(t, conn, "layouts", validLayout(tr.hall))
	tr.section = mustInsert(t, conn, "sections", validSection(tr.layout))
	return tr
}

// id returns the id of the tree's row in table.
func (tr tree) id(table string) string {
	return map[string]string{
		"venues": tr.venue, "halls": tr.hall, "layouts": tr.layout, "sections": tr.section,
	}[table]
}

func exists(t *testing.T, conn *pgx.Conn, table, id string) bool {
	t.Helper()
	var found bool
	// table is one of treeTables, never input, so building the SQL from it is safe.
	err := conn.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, id).Scan(&found)
	if err != nil {
		t.Fatalf("check %s: %v", table, err)
	}
	return found
}

// Deleting a venue, hall or layout deletes everything below it (ON DELETE CASCADE), and nothing
// above it or in another venue.
func TestDeletingAParentDeletesEverythingBelowIt(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))

	for level, table := range treeTables[:3] { // a section has nothing below it
		t.Run(table, func(t *testing.T) {
			deleted, kept := newTree(t, conn), newTree(t, conn)

			if _, err := conn.Exec(t.Context(), `DELETE FROM `+table+` WHERE id = $1`, deleted.id(table)); err != nil {
				t.Fatalf("delete from %s: %v", table, err)
			}

			for l, other := range treeTables {
				wantLeft := l < level // only the levels above the deleted row stay
				if got := exists(t, conn, other, deleted.id(other)); got != wantLeft {
					t.Errorf("%s row of the changed tree: exists %t, want %t", other, got, wantLeft)
				}
				if !exists(t, conn, other, kept.id(other)) {
					t.Errorf("%s row of the other tree was deleted", other)
				}
			}
		})
	}
}
