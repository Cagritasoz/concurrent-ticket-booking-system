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
	"time"

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

// insertSQL builds the INSERT statement for r and its values.
func insertSQL(table string, r row) (string, []any) {
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
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table, strings.Join(columns, ", "), strings.Join(placeholders, ", "))
	return sql, values
}

// insert adds r to table and returns the new row's id.
func insert(ctx context.Context, conn *pgx.Conn, table string, r row) (string, error) {
	sql, values := insertSQL(table, r)
	var id string
	err := conn.QueryRow(ctx, sql+" RETURNING id::text", values...).Scan(&id)
	return id, err
}

// insertNoID adds r to a table without an id column, such as tier_prices.
func insertNoID(ctx context.Context, conn *pgx.Conn, table string, r row) error {
	sql, values := insertSQL(table, r)
	_, err := conn.Exec(ctx, sql, values...)
	return err
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
	return row{"layout_id": layoutID, "name": "Floor", "kind": "standing", "capacity": 3000, "position": 1}
}

func validSeatedSection(layoutID string) row {
	return row{"layout_id": layoutID, "name": "Block 101", "kind": "seated", "position": 1}
}

func validSeat(sectionID string) row {
	return row{"section_id": sectionID, "row_label": "A", "seat_label": "1", "row_position": 1, "seat_position": 1}
}

// eventTime is hour:minute on the given day of March 2027, Istanbul time. Days past 31 roll over
// into the next months.
func eventTime(day, hour, minute int) string {
	istanbul := time.FixedZone("TRT", 3*60*60)
	return time.Date(2027, time.March, day, hour, minute, 0, 0, istanbul).Format(time.RFC3339)
}

// eventOn is a draft event in tr's layout from 20:00 to 23:00 on the given day. Events in one
// hall can't overlap, so a test that needs several in one hall gives each its own day.
func eventOn(tr tree, day int) row {
	return row{
		"layout_id": tr.layout, "hall_id": tr.hall, "name": "Duman", "currency": "TRY",
		"starts_at": eventTime(day, 20, 0), "ends_at": eventTime(day, 23, 0),
	}
}

func validEvent(tr tree) row {
	return eventOn(tr, 14)
}

// submitted, confirmed, announced and cancelled move an event row along its lifecycle, setting
// the status and every time the status needs, in January 2027, before the events of the tests.
func submitted(r row) row {
	return r.with("status", "submitted").with("submitted_at", "2027-01-05T12:00:00Z")
}

func confirmed(r row) row {
	return submitted(r).with("status", "confirmed").with("confirmed_at", "2027-01-07T12:00:00Z")
}

func announced(r row) row {
	return confirmed(r).with("status", "announced").with("announced_at", "2027-01-10T12:00:00Z")
}

func cancelled(r row) row {
	return announced(r).with("status", "cancelled").with("cancelled_at", "2027-02-01T09:00:00Z")
}

func validSeries() row {
	return row{"name": "Hamlet, spring 2027"}
}

func validTier(eventID, kind string) row {
	return row{"event_id": eventID, "name": "Standard", "kind": kind}
}

func validTicketType(eventID string) row {
	return row{"event_id": eventID, "name": "Adult"}
}

func validPrice(tierID, ticketTypeID, eventID string) row {
	return row{"tier_id": tierID, "ticket_type_id": ticketTypeID, "event_id": eventID, "price_amount": 45000}
}

func validHold(eventID string) row {
	return row{"event_id": eventID, "name": "Artist guests", "reason": "artist"}
}

func validPass(seriesID string) row {
	return row{"series_id": seriesID, "name": "3-day pass", "kind": "standing", "currency": "TRY"}
}

func validPassPrice(passID string) row {
	return row{"pass_id": passID, "name": "Adult", "price_amount": 130000}
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
	seated := validSeatedSection(layoutID).with("name", "Balcony") // newTree already has a Block 101

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
		{"same position as another section", validSection(layoutID).with("name", "Standing right"), ""},
		{"missing position", validSection(layoutID).with("position", nil), notNullViolation},
		{"zero position", validSection(layoutID).with("position", 0), checkViolation},
		{"zero version", validSection(layoutID).with("version", 0), checkViolation},
		{"a status", validSection(layoutID).with("status", "active"), undefinedColumn},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkRule(t, conn, "sections", tc)
		})
	}
}

func TestSeats_TableRules(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	tr := newTree(t, conn)
	standingID := mustInsert(t, conn, "sections", validSection(tr.layout))

	// Each case gets its own place, so the cases don't collide with each other's seats.
	place := 10
	seat := func() row {
		place++
		return validSeat(tr.section).
			with("seat_label", strconv.Itoa(place)).
			with("seat_position", place)
	}

	tests := []ruleCase{
		{"required columns only", seat(), ""},
		{"every column", seat().
			with("row_label", "AA").
			with("row_position", 27).
			with("type", "wheelchair").
			with("restricted_view", true).
			with("view_note", "Pillar on the left").
			with("aisle", true).
			with("physical_seat_key", unknownID).
			with("version", 4), ""},
		{"companion seat", seat().with("type", "companion"), ""},
		{"in a standing section", seat().with("section_id", standingID), foreignKeyViolation},
		{"in an unknown section", seat().with("section_id", unknownID), foreignKeyViolation},
		{"section kind other than seated", seat().with("section_kind", "standing"), checkViolation},
		{"missing row label", seat().with("row_label", nil), notNullViolation},
		{"blank seat label", seat().with("seat_label", " "), checkViolation},
		{"label longer than 10", seat().with("row_label", strings.Repeat("A", 11)), checkViolation},
		{"zero row position", seat().with("row_position", 0), checkViolation},
		{"zero seat position", seat().with("seat_position", 0), checkViolation},
		{"unknown type", seat().with("type", "vip"), checkViolation},
		{"view note without a restricted view", seat().with("view_note", "Pillar"), checkViolation},
		{"empty view note", seat().with("restricted_view", true).with("view_note", ""), checkViolation},
		{"missing physical seat key", seat().with("physical_seat_key", nil), notNullViolation},
		{"zero version", seat().with("version", 0), checkViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkRule(t, conn, "seats", tc)
		})
	}
}

// A section can't have two seats at one place, or two seats with the same labels (ignoring
// case). Other sections can repeat both. The services will tell the two apart by index name.
func TestSeatsAreUniquePerPlaceAndLabel(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	tr := newTree(t, conn) // its seat is row A, seat 1, at position (1, 1)

	_, err := insert(t.Context(), conn, "seats", validSeat(tr.section).with("seat_label", "2"))
	if !postgres.IsUniqueViolation(err, "seats_section_id_position_key") {
		t.Errorf("same place: got %v, want a seats_section_id_position_key violation", err)
	}
	_, err = insert(t.Context(), conn, "seats", validSeat(tr.section).with("row_label", "a").with("seat_position", 2))
	if !postgres.IsUniqueViolation(err, "seats_section_id_label_key") {
		t.Errorf("same labels: got %v, want a seats_section_id_label_key violation", err)
	}
	if _, err := insert(t.Context(), conn, "seats", validSeat(mustInsert(t, conn, "sections",
		validSeatedSection(tr.layout).with("name", "Block 102")))); err != nil {
		t.Errorf("same place and labels in another section: %v", err)
	}
}

// The (id, kind) foreign key keeps seats in seated sections in both directions: a seat can't be
// added to a standing section (TestSeats_TableRules), and a section with seats can't turn standing.
func TestASectionWithSeatsCannotBecomeStanding(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	tr := newTree(t, conn)
	empty := mustInsert(t, conn, "sections", validSeatedSection(tr.layout).with("name", "Block 102"))

	toStanding := `UPDATE sections SET kind = 'standing', capacity = 100 WHERE id = $1`
	_, err := conn.Exec(t.Context(), toStanding, tr.section)
	if !postgres.IsForeignKeyViolation(err, "seats_section_id_fkey") {
		t.Errorf("section with seats: got %v, want a seats_section_id_fkey violation", err)
	}
	if _, err := conn.Exec(t.Context(), toStanding, empty); err != nil {
		t.Errorf("section without seats: %v", err)
	}
}

// Every new seat is a new physical seat with its own key. A layout copy (M1 step 11) gives its
// seats the keys of the seats they were copied from, so the same key can be in several layouts.
func TestSeatsShareAPhysicalSeatKeyOnlyWhenCopied(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	tr := newTree(t, conn) // its seat is row A, seat 1
	other := mustInsert(t, conn, "seats", validSeat(tr.section).with("seat_label", "2").with("seat_position", 2))

	key := func(seatID string) string {
		t.Helper()
		var k string
		if err := conn.QueryRow(t.Context(), `SELECT physical_seat_key::text FROM seats WHERE id = $1`, seatID).Scan(&k); err != nil {
			t.Fatalf("select key: %v", err)
		}
		return k
	}
	if key(tr.seat) == key(other) {
		t.Errorf("two new seats got the same physical seat key %s", key(tr.seat))
	}

	// The copy: the same hall, a second layout, the same seat with the original's key.
	copyLayout := mustInsert(t, conn, "layouts", validLayout(tr.hall).with("name", "Theater v2"))
	copySection := mustInsert(t, conn, "sections", validSeatedSection(copyLayout))
	copied := mustInsert(t, conn, "seats", validSeat(copySection).with("physical_seat_key", key(tr.seat)))
	if key(copied) != key(tr.seat) {
		t.Errorf("copied seat has key %s, want the original's %s", key(copied), key(tr.seat))
	}
}

func TestEventSeries_TableRules(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))

	tests := []ruleCase{
		{"required columns only", validSeries(), ""},
		{"every column", validSeries().
			with("description", "Eight weeks at the Harbiye stage.").
			with("version", 2), ""},
		{"same name again", validSeries(), ""}, // the same play can have a run every year
		{"missing name", validSeries().with("name", nil), notNullViolation},
		{"blank name", validSeries().with("name", " "), checkViolation},
		{"name longer than 200", validSeries().with("name", strings.Repeat("a", 201)), checkViolation},
		{"empty description", validSeries().with("description", ""), checkViolation},
		{"zero version", validSeries().with("version", 0), checkViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkRule(t, conn, "event_series", tc)
		})
	}
}

// A series with events can't be deleted (no ON DELETE): its events are taken out of it or
// deleted first. Taking an event out keeps the event.
func TestASeriesKeepsItsEvents(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	seriesID := mustInsert(t, conn, "event_series", validSeries())
	eventID := mustInsert(t, conn, "events", validEvent(newTree(t, conn)).with("series_id", seriesID))

	_, err := conn.Exec(t.Context(), `DELETE FROM event_series WHERE id = $1`, seriesID)
	if !postgres.IsForeignKeyViolation(err, "events_series_id_fkey") {
		t.Fatalf("delete a series with an event: got %v, want an events_series_id_fkey violation", err)
	}

	if _, err := conn.Exec(t.Context(), `UPDATE events SET series_id = NULL WHERE id = $1`, eventID); err != nil {
		t.Fatalf("take the event out of the series: %v", err)
	}
	if _, err := conn.Exec(t.Context(), `DELETE FROM event_series WHERE id = $1`, seriesID); err != nil {
		t.Fatalf("delete the empty series: %v", err)
	}
	if !exists(t, conn, "events", eventID) {
		t.Error("the event was deleted with its series")
	}
}

func TestEvents_TableRules(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	tr, other := newTree(t, conn), newTree(t, conn)
	seriesID := mustInsert(t, conn, "event_series", validSeries())

	// Each case gets its own day, so the cases don't overlap each other in the hall.
	day := 0
	event := func() row {
		day++
		return eventOn(tr, day)
	}
	// at is a time on the day of the event made last.
	at := func(hour, minute int) string { return eventTime(day, hour, minute) }

	tests := []ruleCase{
		{"required columns only", event(), ""},
		{"submitted", submitted(event()), ""},
		{"confirmed", confirmed(event()), ""},
		{"every column", announced(event()).
			with("series_id", seriesID).
			with("description", "The album tour, with guests.").
			with("doors_open_at", at(18, 30)).
			with("hall_reserved_from", at(12, 0)).
			with("hall_reserved_until", at(23, 59)).
			with("on_sale_at", "2027-01-15T10:00:00Z").
			with("off_sale_at", at(18, 0)).
			with("per_customer_limit", 6).
			with("rescheduled_at", "2027-01-20T09:00:00Z").
			with("version", 3), ""},
		{"cancelled", cancelled(event()), ""},
		{"declined, back to draft", event().with("decline_reason", "The hall is booked for maintenance."), ""},
		{"unknown layout", event().with("layout_id", unknownID), foreignKeyViolation},
		{"hall of another layout", event().with("hall_id", other.hall), foreignKeyViolation},
		{"unknown series", event().with("series_id", unknownID), foreignKeyViolation},
		{"missing layout", event().with("layout_id", nil), notNullViolation},
		{"missing hall", event().with("hall_id", nil), notNullViolation},
		{"blank name", event().with("name", " "), checkViolation},
		{"empty description", event().with("description", ""), checkViolation},
		{"missing start", event().with("starts_at", nil), notNullViolation},
		{"missing end", event().with("ends_at", nil), notNullViolation},
		{"ends at the start", event().with("ends_at", at(20, 0)), checkViolation},
		{"doors open after the start", event().with("doors_open_at", at(20, 30)), checkViolation},
		{"on sale at the start", event().with("on_sale_at", at(20, 0)), checkViolation},
		{"on sale after the start", event().with("on_sale_at", at(21, 0)), checkViolation},
		{"hall reserved from the start, no door time", event().with("hall_reserved_from", at(20, 0)), ""},
		{"hall reserved from after the start", event().with("hall_reserved_from", at(20, 30)), checkViolation},
		{"hall reserved from after the doors open", event().with("doors_open_at", at(18, 30)).with("hall_reserved_from", at(19, 0)), checkViolation},
		{"hall reserved until the end", event().with("hall_reserved_until", at(23, 0)), ""},
		{"hall reserved until before the end", event().with("hall_reserved_until", at(22, 0)), checkViolation},
		{"off sale after the start, for late entry", event().with("off_sale_at", at(20, 15)), ""},
		{"off sale at the end", event().with("off_sale_at", at(23, 0)), ""},
		{"off sale after the end", event().with("off_sale_at", at(23, 30)), checkViolation},
		{"off sale when it goes on sale", event().with("on_sale_at", at(10, 0)).with("off_sale_at", at(10, 0)), checkViolation},
		{"off sale before it goes on sale", event().with("on_sale_at", at(10, 0)).with("off_sale_at", at(9, 0)), checkViolation},
		{"lowercase currency", event().with("currency", "try"), checkViolation},
		{"missing currency", event().with("currency", nil), notNullViolation},
		{"unknown status", event().with("status", "published"), checkViolation},
		{"draft with a submit time", event().with("submitted_at", "2027-01-05T12:00:00Z"), checkViolation},
		{"submitted without a submit time", event().with("status", "submitted"), checkViolation},
		{"submitted with a confirm time", submitted(event()).with("confirmed_at", "2027-01-07T12:00:00Z"), checkViolation},
		{"confirmed without a confirm time", submitted(event()).with("status", "confirmed"), checkViolation},
		{"confirmed with an announce time", confirmed(event()).with("announced_at", "2027-01-10T12:00:00Z"), checkViolation},
		{"announced without an announce time", confirmed(event()).with("status", "announced"), checkViolation},
		{"confirmed before it was submitted", confirmed(event()).with("confirmed_at", "2027-01-01T00:00:00Z"), checkViolation},
		{"announced before it was confirmed", announced(event()).with("announced_at", "2027-01-06T00:00:00Z"), checkViolation},
		{"cancelled without a cancel time", announced(event()).with("status", "cancelled"), checkViolation},
		{"cancel time on an announced event", announced(event()).with("cancelled_at", "2027-02-01T09:00:00Z"), checkViolation},
		{"cancelled before it was announced", cancelled(event()).with("cancelled_at", "2027-01-08T00:00:00Z"), checkViolation},
		{"cancelled without being announced", confirmed(event()).with("status", "cancelled").with("cancelled_at", "2027-02-01T09:00:00Z"), checkViolation},
		{"cancelled draft", event().with("status", "cancelled").with("cancelled_at", "2027-02-01T09:00:00Z"), checkViolation},
		{"decline reason on a submitted event", submitted(event()).with("decline_reason", "Booked."), checkViolation},
		{"blank decline reason", event().with("decline_reason", " "), checkViolation},
		{"rescheduled draft", event().with("rescheduled_at", "2027-01-20T09:00:00Z"), checkViolation},
		{"rescheduled before it was announced", announced(event()).with("rescheduled_at", "2027-01-09T00:00:00Z"), checkViolation},
		{"rescheduled confirmed event", confirmed(event()).with("rescheduled_at", "2027-01-20T09:00:00Z"), checkViolation},
		{"zero limit", event().with("per_customer_limit", 0), checkViolation},
		{"zero version", event().with("version", 0), checkViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkRule(t, conn, "events", tc)
		})
	}
}

// A hall is set up one way at a time, so the events that hold a slot in it (submitted, confirmed
// or announced) can't overlap, whichever of its layouts they use, counting the time reserved for
// setting up and clearing. Back to back is fine, other halls are free, and drafts and cancelled
// events don't take the slot.
func TestEventsInOneHallCannotOverlap(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	tr, other := newTree(t, conn), newTree(t, conn)
	basketball := mustInsert(t, conn, "layouts", validLayout(tr.hall).with("name", "Basketball"))
	mustInsert(t, conn, "events", submitted(eventOn(tr, 1))) // 20:00 to 23:00 on March 1
	// 20:00 to 23:00 on March 3, then cleared until 01:00 on March 4.
	mustInsert(t, conn, "events", submitted(eventOn(tr, 3)).with("hall_reserved_until", eventTime(4, 1, 0)))

	tests := []struct {
		name        string
		event       row
		wantOverlap bool
	}{
		{"same layout, overlapping", submitted(eventOn(tr, 1).with("starts_at", eventTime(1, 22, 0)).with("ends_at", eventTime(2, 1, 0))), true},
		{"same hall, other layout", submitted(eventOn(tr, 1).with("layout_id", basketball)), true},
		{"inside the other event", submitted(eventOn(tr, 1).with("starts_at", eventTime(1, 21, 0)).with("ends_at", eventTime(1, 22, 0))), true},
		{"confirmed, same time", confirmed(eventOn(tr, 1)), true},
		{"announced, same time", announced(eventOn(tr, 1)), true},
		{"its setup overlaps the other event", submitted(eventOn(tr, 1).
			with("starts_at", eventTime(1, 23, 30)).with("ends_at", eventTime(2, 1, 0)).
			with("hall_reserved_from", eventTime(1, 22, 30))), true},
		{"during the other's clearing", submitted(eventOn(tr, 4).
			with("starts_at", eventTime(4, 0, 30)).with("ends_at", eventTime(4, 2, 0))), true},
		{"right after the other's clearing", submitted(eventOn(tr, 4).
			with("starts_at", eventTime(4, 1, 0)).with("ends_at", eventTime(4, 3, 0))), false},
		{"right after it", submitted(eventOn(tr, 1).with("starts_at", eventTime(1, 23, 0)).with("ends_at", eventTime(2, 1, 0))), false},
		{"other hall, same time", submitted(eventOn(other, 1)), false},
		{"draft, same time", eventOn(tr, 1), false},
		{"another draft, same time", eventOn(tr, 1).with("name", "Duman, second date"), false},
		{"cancelled, same time", cancelled(eventOn(tr, 1)), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := insert(t.Context(), conn, "events", tc.event)
			if tc.wantOverlap {
				if !postgres.IsExclusionViolation(err, "events_no_overlap_in_hall") {
					t.Errorf("got %v, want an events_no_overlap_in_hall violation", err)
				}
				return
			}
			if err != nil {
				t.Errorf("got %v, want no error", err)
			}
		})
	}

	// Moving an event onto a taken slot is refused too, not only inserting one.
	t.Run("moved onto the slot", func(t *testing.T) {
		id := mustInsert(t, conn, "events", submitted(eventOn(tr, 5)))
		_, err := conn.Exec(t.Context(), `UPDATE events SET starts_at = $2, ends_at = $3 WHERE id = $1`,
			id, eventTime(1, 19, 0), eventTime(1, 21, 0))
		if !postgres.IsExclusionViolation(err, "events_no_overlap_in_hall") {
			t.Errorf("got %v, want an events_no_overlap_in_hall violation", err)
		}
	})

	// A draft can be on a taken date; submitting it is what claims the slot, so that is refused.
	t.Run("submitting a draft into a taken slot", func(t *testing.T) {
		id := mustInsert(t, conn, "events", eventOn(tr, 1).with("name", "Duman, third date"))
		_, err := conn.Exec(t.Context(),
			`UPDATE events SET status = 'submitted', submitted_at = now() WHERE id = $1`, id)
		if !postgres.IsExclusionViolation(err, "events_no_overlap_in_hall") {
			t.Errorf("got %v, want an events_no_overlap_in_hall violation", err)
		}
	})
}

// Events reference their layout without CASCADE: deleting the layout, its hall or its venue is
// refused while an event uses it, and nothing is deleted. Archiving is the way to retire them.
func TestAnEventKeepsItsLayoutHallAndVenue(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))

	for _, table := range []string{"venues", "halls", "layouts"} {
		t.Run(table, func(t *testing.T) {
			tr := newTree(t, conn)
			eventID := mustInsert(t, conn, "events", validEvent(tr))

			_, err := conn.Exec(t.Context(), `DELETE FROM `+table+` WHERE id = $1`, tr.id(table))
			if !postgres.IsForeignKeyViolation(err, "events_layout_id_fkey") {
				t.Fatalf("got %v, want an events_layout_id_fkey violation", err)
			}
			for _, other := range treeTables {
				if !exists(t, conn, other, tr.id(other)) {
					t.Errorf("the %s row was deleted", other)
				}
			}
			if !exists(t, conn, "events", eventID) {
				t.Error("the event was deleted")
			}
		})
	}
}

// sales is one announced event in a tree's layout, which also has a standing section, with a
// seated and a standing tier, an "Adult" ticket type and an admin hold: what prices and tickets
// need.
type sales struct {
	tree
	standingSection, event, seatedTier, standingTier, ticketType, hold string
}

func newSales(t *testing.T, conn *pgx.Conn) sales {
	t.Helper()
	s := sales{tree: newTree(t, conn)}
	s.standingSection = mustInsert(t, conn, "sections", validSection(s.layout))
	s.event = mustInsert(t, conn, "events", announced(validEvent(s.tree)))
	s.seatedTier = mustInsert(t, conn, "ticket_tiers", validTier(s.event, "seated"))
	s.standingTier = mustInsert(t, conn, "ticket_tiers", validTier(s.event, "standing").with("name", "Floor"))
	s.ticketType = mustInsert(t, conn, "ticket_types", validTicketType(s.event))
	s.hold = mustInsert(t, conn, "admin_holds", validHold(s.event))
	return s
}

// newSeat adds the seat at position n of row A to the sales tree's seated section.
func (s sales) newSeat(t *testing.T, conn *pgx.Conn, n int) string {
	t.Helper()
	return mustInsert(t, conn, "seats", validSeat(s.section).
		with("seat_label", strconv.Itoa(n)).with("seat_position", n))
}

func (s sales) seatedTicket(seatID string) row {
	return row{"event_id": s.event, "tier_id": s.seatedTier, "kind": "seated", "section_id": s.section, "seat_id": seatID}
}

func (s sales) standingTicket() row {
	return row{"event_id": s.event, "tier_id": s.standingTier, "kind": "standing", "section_id": s.standingSection}
}

func TestTicketTiers_TableRules(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	eventID := mustInsert(t, conn, "events", validEvent(newTree(t, conn)))

	// Each case gets its own name, since names are unique per event.
	n := 0
	tier := func(kind string) row {
		n++
		return validTier(eventID, kind).with("name", "Tier "+strconv.Itoa(n))
	}

	tests := []ruleCase{
		{"seated", tier("seated"), ""},
		{"standing", tier("standing"), ""},
		{"every column", tier("seated").
			with("description", "Includes a drink and early entry.").
			with("per_customer_limit", 4).
			with("sold_via", "shared").
			with("version", 2), ""},
		{"only through passes", tier("standing").with("sold_via", "pass_only"), ""},
		{"boxes sold whole", tier("seated").with("sold_as", "section"), ""},
		{"a standing box sold whole", tier("standing").with("sold_as", "section"), ""},
		{"sold whole through passes", tier("seated").with("sold_as", "section").with("sold_via", "pass_only"), checkViolation},
		{"sold whole, shared with passes", tier("seated").with("sold_as", "section").with("sold_via", "shared"), checkViolation},
		{"unknown event", tier("seated").with("event_id", unknownID), foreignKeyViolation},
		{"unknown sold_as", tier("seated").with("sold_as", "row"), checkViolation},
		{"unknown sold_via", tier("seated").with("sold_via", "resale"), checkViolation},
		{"missing sold_via", tier("seated").with("sold_via", nil), notNullViolation},
		{"blank name", tier("seated").with("name", " "), checkViolation},
		{"name longer than 100", tier("seated").with("name", strings.Repeat("a", 101)), checkViolation},
		{"empty description", tier("seated").with("description", ""), checkViolation},
		{"unknown kind", tier("mixed"), checkViolation},
		{"zero limit", tier("seated").with("per_customer_limit", 0), checkViolation},
		{"zero version", tier("seated").with("version", 0), checkViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkRule(t, conn, "ticket_tiers", tc)
		})
	}

	t.Run("a new tier is sold per seat, on its own", func(t *testing.T) {
		id := mustInsert(t, conn, "ticket_tiers", validTier(eventID, "seated").with("name", "Defaults"))
		var soldAs, soldVia string
		err := conn.QueryRow(t.Context(), `SELECT sold_as, sold_via FROM ticket_tiers WHERE id = $1`, id).Scan(&soldAs, &soldVia)
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if soldAs != "seat" || soldVia != "single" {
			t.Errorf("sold_as %q, sold_via %q; want seat, single", soldAs, soldVia)
		}
	})

	t.Run("name taken in the event, ignoring case", func(t *testing.T) {
		mustInsert(t, conn, "ticket_tiers", validTier(eventID, "seated").with("name", "VIP"))
		_, err := insert(t.Context(), conn, "ticket_tiers", validTier(eventID, "standing").with("name", "vip"))
		if !postgres.IsUniqueViolation(err, "ticket_tiers_event_id_name_key") {
			t.Errorf("got %v, want a ticket_tiers_event_id_name_key violation", err)
		}
	})
}

func TestTicketTypes_TableRules(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	eventID := mustInsert(t, conn, "events", validEvent(newTree(t, conn)))

	// Each case gets its own name, since names are unique per event.
	n := 0
	ticketType := func() row {
		n++
		return validTicketType(eventID).with("name", "Type "+strconv.Itoa(n))
	}

	tests := []ruleCase{
		{"required columns only", ticketType(), ""},
		{"every column", ticketType().
			with("description", "With a valid student ID.").
			with("visibility", "admin").
			with("version", 2), ""},
		{"unknown visibility", ticketType().with("visibility", "code"), checkViolation},
		{"missing visibility", ticketType().with("visibility", nil), notNullViolation},
		{"unknown event", ticketType().with("event_id", unknownID), foreignKeyViolation},
		{"missing event", ticketType().with("event_id", nil), notNullViolation},
		{"blank name", ticketType().with("name", " "), checkViolation},
		{"name longer than 100", ticketType().with("name", strings.Repeat("a", 101)), checkViolation},
		{"empty description", ticketType().with("description", ""), checkViolation},
		{"zero version", ticketType().with("version", 0), checkViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkRule(t, conn, "ticket_types", tc)
		})
	}

	t.Run("a new type is public", func(t *testing.T) {
		wantVisibility(t, conn, "ticket_types", mustInsert(t, conn, "ticket_types",
			validTicketType(eventID).with("name", "Defaults")), "public")
	})

	t.Run("name taken in the event, ignoring case", func(t *testing.T) {
		mustInsert(t, conn, "ticket_types", validTicketType(eventID).with("name", "Student"))
		_, err := insert(t.Context(), conn, "ticket_types", validTicketType(eventID).with("name", "STUDENT"))
		if !postgres.IsUniqueViolation(err, "ticket_types_event_id_name_key") {
			t.Errorf("got %v, want a ticket_types_event_id_name_key violation", err)
		}
	})
}

// wantVisibility checks the visibility of the row with the given id. table is a constant of the
// caller, never input, so building the SQL from it is safe.
func wantVisibility(t *testing.T, conn *pgx.Conn, table, id, want string) {
	t.Helper()
	var got string
	if err := conn.QueryRow(t.Context(), `SELECT visibility FROM `+table+` WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("select visibility: %v", err)
	}
	if got != want {
		t.Errorf("visibility %q, want %q", got, want)
	}
}

// A price is what one ticket type costs in one tier:one price per pair, the tier and the type of
// the same event, and deleting a tier or a type deletes its prices.
func TestTierPrices(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	s, other := newSales(t, conn), newSales(t, conn)
	student := mustInsert(t, conn, "ticket_types", validTicketType(s.event).with("name", "Student"))
	price := func(tierID, ticketTypeID string) row { return validPrice(tierID, ticketTypeID, s.event) }

	tests := []struct {
		name       string
		row        row
		wantCode   string // "" means the insert must succeed
		constraint string // the constraint a refusal must name, if it is one of several
	}{
		{"adult in the seated tier", price(s.seatedTier, s.ticketType), "", ""},
		{"student in the seated tier", price(s.seatedTier, student).with("price_amount", 30000), "", ""},
		{"free, every column", price(s.standingTier, s.ticketType).with("price_amount", 0).with("version", 2), "", ""},
		{"second price for one tier and type", price(s.seatedTier, s.ticketType), uniqueViolation, "tier_prices_pkey"},
		{"tier of another event", price(other.seatedTier, student), foreignKeyViolation, "tier_prices_tier_id_fkey"},
		{"ticket type of another event", price(s.standingTier, other.ticketType), foreignKeyViolation, "tier_prices_ticket_type_id_fkey"},
		{"missing event", price(s.standingTier, student).with("event_id", nil), notNullViolation, ""},
		{"missing price", price(s.standingTier, student).with("price_amount", nil), notNullViolation, ""},
		{"negative price", price(s.standingTier, student).with("price_amount", -1), checkViolation, ""},
		{"zero version", price(s.standingTier, student).with("version", 0), checkViolation, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := insertNoID(t.Context(), conn, "tier_prices", tc.row)
			wantError(t, err, tc.wantCode)
			var pgErr *pgconn.PgError
			if tc.constraint != "" && errors.As(err, &pgErr) && pgErr.ConstraintName != tc.constraint {
				t.Errorf("refused by %s, want %s", pgErr.ConstraintName, tc.constraint)
			}
		})
	}

	t.Run("a new price has version 1", func(t *testing.T) {
		var version int
		err := conn.QueryRow(t.Context(), `SELECT version FROM tier_prices WHERE tier_id = $1 AND ticket_type_id = $2`,
			s.seatedTier, s.ticketType).Scan(&version)
		if err != nil {
			t.Fatalf("select version: %v", err)
		}
		if version != 1 {
			t.Errorf("version %d, want 1", version)
		}
	})

	t.Run("deleting a ticket type or a tier deletes its prices", func(t *testing.T) {
		// column is one of the two below, never input, so building the SQL from it is safe.
		pricesOf := func(column, id string) int {
			t.Helper()
			var count int
			if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM tier_prices WHERE `+column+` = $1`, id).Scan(&count); err != nil {
				t.Fatalf("count prices: %v", err)
			}
			return count
		}
		if _, err := conn.Exec(t.Context(), `DELETE FROM ticket_types WHERE id = $1`, student); err != nil {
			t.Fatalf("delete ticket type: %v", err)
		}
		if n := pricesOf("ticket_type_id", student); n != 0 {
			t.Errorf("%d prices of the deleted ticket type are left", n)
		}
		if _, err := conn.Exec(t.Context(), `DELETE FROM ticket_tiers WHERE id = $1`, s.seatedTier); err != nil {
			t.Fatalf("delete tier: %v", err)
		}
		if n := pricesOf("tier_id", s.seatedTier); n != 0 {
			t.Errorf("%d prices of the deleted tier are left", n)
		}
	})
}

func TestAdminHolds_TableRules(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	eventID := newSales(t, conn).event

	n := 0
	hold := func() row {
		n++
		return validHold(eventID).with("name", "Hold "+strconv.Itoa(n))
	}

	tests := []ruleCase{
		{"required columns only", hold(), ""},
		{"every column", hold().with("reason", "sponsor").with("note", "Until the end of March.").with("version", 2), ""},
		{"accessibility reserve", hold().with("reason", "accessibility"), ""},
		{"unknown event", hold().with("event_id", unknownID), foreignKeyViolation},
		{"blank name", hold().with("name", ""), checkViolation},
		{"unknown reason", hold().with("reason", "friends"), checkViolation},
		{"missing reason", hold().with("reason", nil), notNullViolation},
		{"empty note", hold().with("note", " "), checkViolation},
		{"zero version", hold().with("version", 0), checkViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkRule(t, conn, "admin_holds", tc)
		})
	}
}

func TestTickets_TableRules(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	s, other := newSales(t, conn), newSales(t, conn)

	// Each seated case gets its own seat, since a seat has one ticket per event.
	n := 10
	seat := func() string {
		n++
		return s.newSeat(t, conn, n)
	}

	tests := []ruleCase{
		{"seated", s.seatedTicket(seat()), ""},
		{"standing", s.standingTicket(), ""},
		{"held", s.standingTicket().with("status", "held").with("hold_id", s.hold), ""},
		{"killed", s.seatedTicket(seat()).with("status", "killed"), ""},
		{"seated without a seat", s.seatedTicket(seat()).with("seat_id", nil), checkViolation},
		{"standing with a seat", s.standingTicket().with("seat_id", seat()), checkViolation},
		{"seated ticket of a standing tier", s.seatedTicket(seat()).with("tier_id", s.standingTier), foreignKeyViolation},
		{"standing ticket in a seated section", s.standingTicket().with("section_id", s.section), foreignKeyViolation},
		{"seat of another section", s.seatedTicket(other.seat), foreignKeyViolation},
		{"tier of another event", s.standingTicket().with("event_id", other.event), foreignKeyViolation},
		{"held without a hold", s.standingTicket().with("status", "held"), checkViolation},
		{"hold on an available ticket", s.standingTicket().with("hold_id", s.hold), checkViolation},
		{"hold of another event", s.standingTicket().with("status", "held").with("hold_id", other.hold), foreignKeyViolation},
		{"unknown status", s.standingTicket().with("status", "sold"), checkViolation},
		{"missing kind", s.standingTicket().with("kind", nil), notNullViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkRule(t, conn, "tickets", tc)
		})
	}

	// A seat's ticket is how a tier sells it, so one ticket per seat and event also means a seat
	// is in at most one tier of an event.
	t.Run("seat in a second tier of the event", func(t *testing.T) {
		vip := mustInsert(t, conn, "ticket_tiers", validTier(s.event, "seated").with("name", "VIP"))
		mustInsert(t, conn, "tickets", s.seatedTicket(s.seat))
		_, err := insert(t.Context(), conn, "tickets", s.seatedTicket(s.seat).with("tier_id", vip))
		if !postgres.IsUniqueViolation(err, "tickets_seat_id_event_id_key") {
			t.Errorf("got %v, want a tickets_seat_id_event_id_key violation", err)
		}
	})

	t.Run("same seat, another event in the layout", func(t *testing.T) {
		later := mustInsert(t, conn, "events", eventOn(s.tree, 20))
		laterTier := mustInsert(t, conn, "ticket_tiers", validTier(later, "seated"))
		if _, err := insert(t.Context(), conn, "tickets", s.seatedTicket(s.seat).
			with("event_id", later).with("tier_id", laterTier)); err != nil {
			t.Errorf("got %v, want no error", err)
		}
	})

	t.Run("two tiers share a standing section", func(t *testing.T) {
		earlyBird := mustInsert(t, conn, "ticket_tiers", validTier(s.event, "standing").with("name", "Floor early bird"))
		if _, err := insert(t.Context(), conn, "tickets", s.standingTicket().with("tier_id", earlyBird)); err != nil {
			t.Errorf("got %v, want no error", err)
		}
	})
}

// A draft event has tickets too, and they block deletes like any event's: of the draft itself
// and of the seats it sells. The service deletes a draft's tickets first; the event then takes
// its tiers, ticket types, prices and holds with it.
func TestDeletingADraftEvent(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	tr := newTree(t, conn)
	eventID := mustInsert(t, conn, "events", validEvent(tr))
	tierID := mustInsert(t, conn, "ticket_tiers", validTier(eventID, "seated"))
	ticketTypeID := mustInsert(t, conn, "ticket_types", validTicketType(eventID))
	if err := insertNoID(t.Context(), conn, "tier_prices", validPrice(tierID, ticketTypeID, eventID)); err != nil {
		t.Fatalf("insert price: %v", err)
	}
	holdID := mustInsert(t, conn, "admin_holds", validHold(eventID))
	mustInsert(t, conn, "tickets", row{
		"event_id": eventID, "tier_id": tierID, "kind": "seated", "section_id": tr.section, "seat_id": tr.seat,
		"status": "held", "hold_id": holdID,
	})

	_, err := conn.Exec(t.Context(), `DELETE FROM seats WHERE id = $1`, tr.seat)
	if !postgres.IsForeignKeyViolation(err, "tickets_seat_id_fkey") {
		t.Errorf("delete a seat the draft sells: got %v, want a tickets_seat_id_fkey violation", err)
	}
	_, err = conn.Exec(t.Context(), `DELETE FROM events WHERE id = $1`, eventID)
	wantError(t, err, foreignKeyViolation)

	if _, err := conn.Exec(t.Context(), `DELETE FROM tickets WHERE event_id = $1`, eventID); err != nil {
		t.Fatalf("delete tickets: %v", err)
	}
	if _, err := conn.Exec(t.Context(), `DELETE FROM events WHERE id = $1`, eventID); err != nil {
		t.Fatalf("delete event: %v", err)
	}
	for _, table := range []string{"ticket_tiers", "ticket_types", "tier_prices", "admin_holds"} {
		var left int
		// table is from the list above, never input, so building the SQL from it is safe.
		if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM `+table+` WHERE event_id = $1`, eventID).Scan(&left); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if left != 0 {
			t.Errorf("%d %s rows of the deleted event are left", left, table)
		}
	}
}

// Tickets are the event's inventory and its history: nothing they point at can be deleted
// underneath them, from the seat up to the venue, nor the event, its tier or its hold.
func TestTicketsKeepWhatTheyPointAt(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	s := newSales(t, conn)
	mustInsert(t, conn, "tickets", s.seatedTicket(s.seat))
	mustInsert(t, conn, "tickets", s.standingTicket().with("status", "held").with("hold_id", s.hold))

	tests := []struct{ table, id string }{
		{"seats", s.seat},
		{"sections", s.section},
		{"sections", s.standingSection},
		{"ticket_tiers", s.seatedTier},
		{"admin_holds", s.hold},
		{"events", s.event},
		{"layouts", s.layout},
		{"venues", s.venue},
	}
	for _, tc := range tests {
		t.Run(tc.table, func(t *testing.T) {
			_, err := conn.Exec(t.Context(), `DELETE FROM `+tc.table+` WHERE id = $1`, tc.id)
			wantError(t, err, foreignKeyViolation)
			if !exists(t, conn, tc.table, tc.id) {
				t.Errorf("the %s row was deleted", tc.table)
			}
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

// festival is a series of two days in one hall and a standing pass of the series: what
// pass_events need. Its tiers are sold only through passes, except day 2's single and shared
// tiers. outside is an event of no series, euro a day of the series in another currency.
type festival struct {
	series, pass, day1, day2, outside, euro string
	day1Standing, day1Seated, day2Standing  string
	day2Single, day2Shared                  string
	outsideTier, euroTier                   string
}

func newFestival(t *testing.T, conn *pgx.Conn) festival {
	t.Helper()
	tr := newTree(t, conn)
	var f festival
	f.series = mustInsert(t, conn, "event_series", validSeries().with("name", "Festival 2027"))
	f.pass = mustInsert(t, conn, "passes", validPass(f.series))
	f.day1 = mustInsert(t, conn, "events", eventOn(tr, 1).with("series_id", f.series))
	f.day2 = mustInsert(t, conn, "events", eventOn(tr, 2).with("series_id", f.series))
	f.outside = mustInsert(t, conn, "events", eventOn(tr, 3))
	f.euro = mustInsert(t, conn, "events", eventOn(tr, 4).with("series_id", f.series).with("currency", "EUR"))
	passOnly := func(eventID, kind, name string) string {
		return mustInsert(t, conn, "ticket_tiers", validTier(eventID, kind).with("name", name).with("sold_via", "pass_only"))
	}
	f.day1Standing = passOnly(f.day1, "standing", "3-day pass")
	f.day1Seated = passOnly(f.day1, "seated", "Stand")
	f.day2Standing = passOnly(f.day2, "standing", "3-day pass")
	f.outsideTier = passOnly(f.outside, "standing", "3-day pass")
	f.euroTier = passOnly(f.euro, "standing", "3-day pass")
	f.day2Single = mustInsert(t, conn, "ticket_tiers", validTier(f.day2, "standing").with("name", "Day ticket"))
	f.day2Shared = mustInsert(t, conn, "ticket_tiers", validTier(f.day2, "standing").with("name", "Field").with("sold_via", "shared"))
	return f
}

// passEvent is the row that makes a standing TRY pass of the festival's series take its ticket
// in eventID from tierID, a pass_only tier.
func (f festival) passEvent(passID, eventID, tierID string) row {
	return row{"pass_id": passID, "event_id": eventID, "tier_id": tierID,
		"series_id": f.series, "currency": "TRY", "kind": "standing", "tier_sold_via": "pass_only"}
}

func TestPasses_TableRules(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	seriesID := mustInsert(t, conn, "event_series", validSeries())

	// Each case gets its own name, since names are unique per series.
	n := 0
	pass := func() row {
		n++
		return validPass(seriesID).with("name", "Pass "+strconv.Itoa(n))
	}
	announcedPass := func() row {
		return pass().with("status", "announced").with("announced_at", "2027-01-10T12:00:00Z")
	}

	tests := []ruleCase{
		{"required columns only", pass(), ""},
		{"seated, same seat in every event", pass().with("kind", "seated").with("seat_selection", "same_seat"), ""},
		{"seated, a seat per event", pass().with("kind", "seated").with("seat_selection", "per_event"), ""},
		{"seated without a seat selection", pass().with("kind", "seated"), checkViolation},
		{"standing with a seat selection", pass().with("seat_selection", "per_event"), checkViolation},
		{"unknown seat selection", pass().with("kind", "seated").with("seat_selection", "any"), checkViolation},
		{"every column", announcedPass().
			with("description", "Entry on all three days, re-entry allowed.").
			with("per_customer_limit", 4).
			with("on_sale_at", "2027-01-15T10:00:00Z").
			with("version", 2), ""},
		{"cancelled", announcedPass().with("status", "cancelled").with("cancelled_at", "2027-02-01T09:00:00Z"), ""},
		{"unknown series", pass().with("series_id", unknownID), foreignKeyViolation},
		{"missing series", pass().with("series_id", nil), notNullViolation},
		{"blank name", pass().with("name", " "), checkViolation},
		{"name longer than 100", pass().with("name", strings.Repeat("a", 101)), checkViolation},
		{"empty description", pass().with("description", ""), checkViolation},
		{"unknown kind", pass().with("kind", "mixed"), checkViolation},
		{"lowercase currency", pass().with("currency", "try"), checkViolation},
		{"missing currency", pass().with("currency", nil), notNullViolation},
		{"zero limit", pass().with("per_customer_limit", 0), checkViolation},
		{"unknown status", pass().with("status", "published"), checkViolation},
		{"a venue's step", pass().with("status", "confirmed"), checkViolation},
		{"draft with an announce time", pass().with("announced_at", "2027-01-10T12:00:00Z"), checkViolation},
		{"announced without an announce time", pass().with("status", "announced"), checkViolation},
		{"cancelled without a cancel time", announcedPass().with("status", "cancelled"), checkViolation},
		{"cancelled before it was announced", announcedPass().with("status", "cancelled").with("cancelled_at", "2027-01-01T00:00:00Z"), checkViolation},
		{"cancelled draft", pass().with("status", "cancelled").with("cancelled_at", "2027-02-01T09:00:00Z"), checkViolation},
		{"zero version", pass().with("version", 0), checkViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkRule(t, conn, "passes", tc)
		})
	}

	t.Run("name taken in the series, ignoring case", func(t *testing.T) {
		mustInsert(t, conn, "passes", validPass(seriesID).with("name", "Weekend"))
		_, err := insert(t.Context(), conn, "passes", validPass(seriesID).with("name", "WEEKEND"))
		if !postgres.IsUniqueViolation(err, "passes_series_id_name_key") {
			t.Errorf("got %v, want a passes_series_id_name_key violation", err)
		}
		other := mustInsert(t, conn, "event_series", validSeries())
		if _, err := insert(t.Context(), conn, "passes", validPass(other).with("name", "Weekend")); err != nil {
			t.Errorf("same name in another series: got %v, want no error", err)
		}
	})

	t.Run("a series with a pass can't be deleted", func(t *testing.T) {
		_, err := conn.Exec(t.Context(), `DELETE FROM event_series WHERE id = $1`, seriesID)
		if !postgres.IsForeignKeyViolation(err, "passes_series_id_fkey") {
			t.Errorf("got %v, want a passes_series_id_fkey violation", err)
		}
	})
}

func TestPassPrices_TableRules(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	passID := mustInsert(t, conn, "passes", validPass(mustInsert(t, conn, "event_series", validSeries())))

	// Each case gets its own name, since names are unique per pass.
	n := 0
	price := func() row {
		n++
		return validPassPrice(passID).with("name", "Option "+strconv.Itoa(n))
	}

	tests := []ruleCase{
		{"required columns only", price(), ""},
		{"free, every column", price().
			with("description", "With a valid student ID.").
			with("price_amount", 0).
			with("visibility", "admin").
			with("version", 2), ""},
		{"unknown pass", price().with("pass_id", unknownID), foreignKeyViolation},
		{"missing pass", price().with("pass_id", nil), notNullViolation},
		{"blank name", price().with("name", " "), checkViolation},
		{"name longer than 100", price().with("name", strings.Repeat("a", 101)), checkViolation},
		{"empty description", price().with("description", ""), checkViolation},
		{"missing price", price().with("price_amount", nil), notNullViolation},
		{"negative price", price().with("price_amount", -1), checkViolation},
		{"unknown visibility", price().with("visibility", "hidden"), checkViolation},
		{"missing visibility", price().with("visibility", nil), notNullViolation},
		{"zero version", price().with("version", 0), checkViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkRule(t, conn, "pass_prices", tc)
		})
	}

	t.Run("a new price option is public", func(t *testing.T) {
		wantVisibility(t, conn, "pass_prices", mustInsert(t, conn, "pass_prices",
			validPassPrice(passID).with("name", "Defaults")), "public")
	})

	t.Run("name taken in the pass, ignoring case", func(t *testing.T) {
		mustInsert(t, conn, "pass_prices", validPassPrice(passID).with("name", "Student"))
		_, err := insert(t.Context(), conn, "pass_prices", validPassPrice(passID).with("name", "student"))
		if !postgres.IsUniqueViolation(err, "pass_prices_pass_id_name_key") {
			t.Errorf("got %v, want a pass_prices_pass_id_name_key violation", err)
		}
	})
}

// A pass's events are of its series and currency, and each one's tier is in that event and of
// the pass's kind; one tier per event and pass.
func TestPassEvents(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	f := newFestival(t, conn)
	// The refused rows use a second pass, so they can't hit the primary key of an accepted row.
	other := mustInsert(t, conn, "passes", validPass(f.series).with("name", "Other"))
	weekend := mustInsert(t, conn, "passes", validPass(f.series).with("name", "Weekend"))
	otherSeries := mustInsert(t, conn, "event_series", validSeries())

	tests := []struct {
		name       string
		row        row
		wantCode   string // "" means the insert must succeed
		constraint string // the constraint a refusal must name, if it is one of several
	}{
		{"day 1 from its standing tier", f.passEvent(f.pass, f.day1, f.day1Standing), "", ""},
		{"day 2, every column", f.passEvent(f.pass, f.day2, f.day2Standing).with("version", 2), "", ""},
		{"a second tier for day 1", f.passEvent(f.pass, f.day1, f.day1Standing), uniqueViolation, "pass_events_pkey"},
		{"two passes share a tier", f.passEvent(weekend, f.day1, f.day1Standing), "", ""},
		{"a tier shared with single tickets", f.passEvent(weekend, f.day2, f.day2Shared).with("tier_sold_via", "shared"), "", ""},
		{"a tier sold only on its own", f.passEvent(other, f.day2, f.day2Single).with("tier_sold_via", "single"), checkViolation, ""},
		{"sold_via other than the tier's", f.passEvent(other, f.day2, f.day2Single), foreignKeyViolation, "pass_events_tier_id_fkey"},
		{"event of no series", f.passEvent(other, f.outside, f.outsideTier), foreignKeyViolation, "pass_events_event_id_fkey"},
		{"event in another currency", f.passEvent(other, f.euro, f.euroTier), foreignKeyViolation, "pass_events_event_id_fkey"},
		{"tier of another event", f.passEvent(other, f.day2, f.day1Standing), foreignKeyViolation, "pass_events_tier_id_fkey"},
		{"seated tier for a standing pass", f.passEvent(other, f.day1, f.day1Seated), foreignKeyViolation, "pass_events_tier_id_fkey"},
		{"kind other than the pass's", f.passEvent(other, f.day1, f.day1Seated).with("kind", "seated"), foreignKeyViolation, "pass_events_pass_id_fkey"},
		{"series other than the pass's", f.passEvent(other, f.day1, f.day1Standing).with("series_id", otherSeries), foreignKeyViolation, "pass_events_pass_id_fkey"},
		{"currency other than the pass's", f.passEvent(other, f.euro, f.euroTier).with("currency", "EUR"), foreignKeyViolation, "pass_events_pass_id_fkey"},
		{"unknown pass", f.passEvent(unknownID, f.day2, f.day2Standing), foreignKeyViolation, "pass_events_pass_id_fkey"},
		{"zero version", f.passEvent(other, f.day2, f.day2Standing).with("version", 0), checkViolation, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := insertNoID(t.Context(), conn, "pass_events", tc.row)
			wantError(t, err, tc.wantCode)
			var pgErr *pgconn.PgError
			if tc.constraint != "" && errors.As(err, &pgErr) && pgErr.ConstraintName != tc.constraint {
				t.Errorf("refused by %s, want %s", pgErr.ConstraintName, tc.constraint)
			}
		})
	}

	t.Run("a new row has version 1", func(t *testing.T) {
		var version int
		err := conn.QueryRow(t.Context(), `SELECT version FROM pass_events WHERE pass_id = $1 AND event_id = $2`,
			f.pass, f.day1).Scan(&version)
		if err != nil {
			t.Fatalf("select version: %v", err)
		}
		if version != 1 {
			t.Errorf("version %d, want 1", version)
		}
	})
}

// A pass never silently loses an event: an event it covers can't be deleted, leave the series or
// change currency, and the tier it takes tickets from can't be deleted. The pass itself can't
// change series, currency or kind while it covers events. Deleting the pass deletes its events
// and prices.
func TestPassesKeepWhatTheyCover(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	f := newFestival(t, conn)
	mustInsert(t, conn, "pass_prices", validPassPrice(f.pass))
	if err := insertNoID(t.Context(), conn, "pass_events", f.passEvent(f.pass, f.day1, f.day1Standing)); err != nil {
		t.Fatalf("insert pass event: %v", err)
	}

	tests := []struct {
		name, sql, id, constraint string
	}{
		{"delete the event", `DELETE FROM events WHERE id = $1`, f.day1, ""},
		{"take the event out of the series", `UPDATE events SET series_id = NULL WHERE id = $1`, f.day1, "pass_events_event_id_fkey"},
		{"change the event's currency", `UPDATE events SET currency = 'EUR' WHERE id = $1`, f.day1, "pass_events_event_id_fkey"},
		{"delete the tier", `DELETE FROM ticket_tiers WHERE id = $1`, f.day1Standing, "pass_events_tier_id_fkey"},
		{"change the pass's kind", `UPDATE passes SET kind = 'seated', seat_selection = 'per_event' WHERE id = $1`, f.pass, "pass_events_pass_id_fkey"},
		{"change the pass's currency", `UPDATE passes SET currency = 'EUR' WHERE id = $1`, f.pass, "pass_events_pass_id_fkey"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := conn.Exec(t.Context(), tc.sql, tc.id)
			if tc.constraint == "" { // deleting the event can trip either of its foreign keys
				wantError(t, err, foreignKeyViolation)
				return
			}
			if !postgres.IsForeignKeyViolation(err, tc.constraint) {
				t.Errorf("got %v, want a %s violation", err, tc.constraint)
			}
		})
	}

	t.Run("the tier can't go back to being sold only on its own", func(t *testing.T) {
		_, err := conn.Exec(t.Context(), `UPDATE ticket_tiers SET sold_via = 'single' WHERE id = $1`, f.day1Standing)
		if !postgres.IsCheckViolation(err, "pass_events_tier_sold_via_check") {
			t.Errorf("got %v, want a pass_events_tier_sold_via_check violation", err)
		}
	})

	t.Run("the tier can't switch to being sold as whole sections", func(t *testing.T) {
		_, err := conn.Exec(t.Context(), `UPDATE ticket_tiers SET sold_as = 'section' WHERE id = $1`, f.day1Standing)
		if !postgres.IsCheckViolation(err, "ticket_tiers_section_sold_alone") {
			t.Errorf("got %v, want a ticket_tiers_section_sold_alone violation", err)
		}
	})

	t.Run("a tier switching to shared takes the pass's row with it", func(t *testing.T) {
		if _, err := conn.Exec(t.Context(), `UPDATE ticket_tiers SET sold_via = 'shared' WHERE id = $1`, f.day1Standing); err != nil {
			t.Fatalf("switch the tier to shared: %v", err)
		}
		var soldVia string
		err := conn.QueryRow(t.Context(), `SELECT tier_sold_via FROM pass_events WHERE pass_id = $1 AND event_id = $2`,
			f.pass, f.day1).Scan(&soldVia)
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if soldVia != "shared" {
			t.Errorf("tier_sold_via %q, want shared", soldVia)
		}
	})

	t.Run("deleting the pass deletes its events and prices", func(t *testing.T) {
		if _, err := conn.Exec(t.Context(), `DELETE FROM passes WHERE id = $1`, f.pass); err != nil {
			t.Fatalf("delete pass: %v", err)
		}
		for _, table := range []string{"pass_events", "pass_prices"} {
			var count int
			// table is one of the two above, never input, so building the SQL from it is safe.
			if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM `+table+` WHERE pass_id = $1`, f.pass).Scan(&count); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			if count != 0 {
				t.Errorf("%d %s rows of the deleted pass are left", count, table)
			}
		}
		if !exists(t, conn, "events", f.day1) {
			t.Error("the event was deleted with the pass")
		}
	})
}

// New rows start like a new venue: version 1 (tickets have none); halls active, layouts,
// events and passes as drafts, tickets available.
func TestNewRowsDefaultStatusAndVersion(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	tr := newTree(t, conn)
	eventID := mustInsert(t, conn, "events", validEvent(tr))
	seriesID := mustInsert(t, conn, "event_series", validSeries())
	passID := mustInsert(t, conn, "passes", validPass(seriesID))
	passPriceID := mustInsert(t, conn, "pass_prices", validPassPrice(passID))
	s := newSales(t, conn)
	ticketID := mustInsert(t, conn, "tickets", s.standingTicket())

	tests := []struct {
		table      string
		id         string
		wantStatus string // "" for a table without a status
		noVersion  bool   // tickets change in bulk under the event's lock, without versions
	}{
		{table: "halls", id: tr.hall, wantStatus: "active"},
		{table: "layouts", id: tr.layout, wantStatus: "draft"},
		{table: "sections", id: tr.section},
		{table: "seats", id: tr.seat},
		{table: "event_series", id: seriesID},
		{table: "events", id: eventID, wantStatus: "draft"},
		{table: "ticket_tiers", id: s.seatedTier},
		{table: "ticket_types", id: s.ticketType},
		{table: "admin_holds", id: s.hold},
		{table: "tickets", id: ticketID, wantStatus: "available", noVersion: true},
		{table: "passes", id: passID, wantStatus: "draft"},
		{table: "pass_prices", id: passPriceID},
	}
	for _, tc := range tests {
		t.Run(tc.table, func(t *testing.T) {
			// tc.table comes from the list above, never input, so building the SQL from it is safe.
			where := ` FROM ` + tc.table + ` WHERE id = $1`
			if !tc.noVersion {
				var version int
				if err := conn.QueryRow(t.Context(), `SELECT version`+where, tc.id).Scan(&version); err != nil {
					t.Fatalf("select version: %v", err)
				}
				if version != 1 {
					t.Errorf("version %d, want 1", version)
				}
			}
			if tc.wantStatus == "" {
				return
			}
			var status string
			if err := conn.QueryRow(t.Context(), `SELECT status`+where, tc.id).Scan(&status); err != nil {
				t.Fatalf("select status: %v", err)
			}
			if status != tc.wantStatus {
				t.Errorf("status %q, want %q", status, tc.wantStatus)
			}
		})
	}
}

// tree holds the ids of one venue with one hall, one layout, one seated section and one seat.
type tree struct {
	venue, hall, layout, section, seat string
}

// treeTables lists the levels of a tree from the top down.
var treeTables = []string{"venues", "halls", "layouts", "sections", "seats"}

func newTree(t *testing.T, conn *pgx.Conn) tree {
	t.Helper()
	var tr tree
	tr.venue = mustInsert(t, conn, "venues", validVenue())
	tr.hall = mustInsert(t, conn, "halls", validHall(tr.venue))
	tr.layout = mustInsert(t, conn, "layouts", validLayout(tr.hall))
	tr.section = mustInsert(t, conn, "sections", validSeatedSection(tr.layout))
	tr.seat = mustInsert(t, conn, "seats", validSeat(tr.section))
	return tr
}

// id returns the id of the tree's row in table.
func (tr tree) id(table string) string {
	return map[string]string{
		"venues": tr.venue, "halls": tr.hall, "layouts": tr.layout, "sections": tr.section, "seats": tr.seat,
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

// Deleting a venue, hall, layout or section deletes everything below it (ON DELETE CASCADE), and
// nothing above it or in another venue.
func TestDeletingAParentDeletesEverythingBelowIt(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))

	for level, table := range treeTables[:4] { // a seat has nothing below it
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
