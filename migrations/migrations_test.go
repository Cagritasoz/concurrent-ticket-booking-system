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

// venue holds the columns of one test insert. Start from validVenue and change what the case needs.
type venue map[string]any

func validVenue() venue {
	return venue{
		"name":          "Zorlu PSM",
		"address_line1": "Levazım, Koru Sokağı No:2",
		"city":          "Istanbul",
		"country_code":  "TR",
		"timezone":      "Europe/Istanbul",
	}
}

func (v venue) with(column string, value any) venue {
	out := venue{}
	for k, val := range v {
		out[k] = val
	}
	out[column] = value
	return out
}

func insertVenue(ctx context.Context, conn *pgx.Conn, v venue) error {
	columns := make([]string, 0, len(v))
	for column := range v {
		columns = append(columns, column)
	}
	slices.Sort(columns) // map order is random; sorting keeps the SQL the same every run

	placeholders := make([]string, len(columns))
	values := make([]any, len(columns))
	for i, column := range columns {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		values[i] = v[column]
	}
	sql := fmt.Sprintf("INSERT INTO venues (%s) VALUES (%s)",
		strings.Join(columns, ", "), strings.Join(placeholders, ", "))
	_, err := conn.Exec(ctx, sql, values...)
	return err
}

// Postgres error codes (SQLSTATE) for the rules the venues table enforces.
const (
	notNullViolation = "23502"
	checkViolation   = "23514"
)

func TestVenues_TableRules(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))

	tests := []struct {
		name     string
		venue    venue
		wantCode string // "" means the insert must succeed
	}{
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
			err := insertVenue(t.Context(), conn, tc.venue)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("insert: %v", err)
				}
				return
			}
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != tc.wantCode {
				t.Fatalf("insert: got %v, want Postgres error %s", err, tc.wantCode)
			}
		})
	}
}

func TestVenues_DefaultsFillIDStatusVersionAndTimestamps(t *testing.T) {
	conn := connect(t, pgtest.NewMigratedDatabase(t))
	if err := insertVenue(t.Context(), conn, validVenue()); err != nil {
		t.Fatalf("insert: %v", err)
	}

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
