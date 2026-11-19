package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event/eventdb"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres/pgtest"
	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/telemetry"
)

// These tests send requests through the whole stack: router and middleware, the handler, the
// real VenueService and a real Postgres. Only the network is left out (httptest), like
// @SpringBootTest with MockMvc.

// newVenueRouter returns a router with a real VenueService on a fresh migrated database, the
// pool (to insert test data or break the database) and the buffer its JSON logs go to.
func newVenueRouter(t *testing.T) (http.Handler, *pgxpool.Pool, *bytes.Buffer) {
	t.Helper()
	pool, err := postgres.NewPool(t.Context(), pgtest.NewMigratedDatabase(t))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close) // runs before pgtest drops the database

	var logs bytes.Buffer
	logger, err := telemetry.NewLogger("debug", "json", &logs)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	return NewRouter(logger, &Readiness{}, pool, event.NewVenueService(pool)), pool, &logs
}

// insertVenue stores a venue with the generated query, since there is no create endpoint yet.
func insertVenue(t *testing.T, pool *pgxpool.Pool, params eventdb.CreateVenueParams) eventdb.Venue {
	t.Helper()
	row, err := eventdb.New(pool).CreateVenue(t.Context(), params)
	if err != nil {
		t.Fatalf("CreateVenue: %v", err)
	}
	return row
}

// checkJSON fails the test unless got and want are the same JSON value. Both are decoded first,
// so key order and spacing don't matter, but every key and value does.
func checkJSON(t *testing.T, got, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("response is not valid JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("want is not valid JSON: %v\n%s", err, want)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("body:\ngot  %s\nwant %s", got, want)
	}
}

// checkProblem fails the test unless rec is a Problem Details response with this status and body.
func checkProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, want string) {
	t.Helper()
	res := rec.Result()
	defer func() { _ = res.Body.Close() }() // an in-memory body; closing can't fail
	if res.StatusCode != status {
		t.Errorf("status: got %d, want %d", res.StatusCode, status)
	}
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, problemContentType) {
		t.Errorf("Content-Type: got %q, want %s", got, problemContentType)
	}
	checkJSON(t, rec.Body.String(), want)
}

// timeJSON formats a time the way the response does.
func timeJSON(tm time.Time) string { return tm.UTC().Format(time.RFC3339Nano) }

func TestGetVenue_ReturnsTheVenue(t *testing.T) {
	router, pool, _ := newVenueRouter(t)

	tests := []struct {
		name   string
		params eventdb.CreateVenueParams
		want   string // %[1]s is the id, %[2]s and %[3]s the timestamps
	}{
		{
			name: "every field set",
			params: eventdb.CreateVenueParams{
				Name:         "Volkswagen Arena",
				Description:  new("Box office opens two hours before each event."),
				AddressLine1: "Huzur Mah. Azerbaycan Cad. 1",
				AddressLine2: new("Gate B"),
				City:         "Istanbul",
				Region:       new("Sariyer"),
				PostalCode:   new("34396"),
				CountryCode:  "TR",
				Latitude:     new(41.108934),
				Longitude:    new(29.010547),
				Timezone:     "Europe/Istanbul",
				Capacity:     new(int32(5000)),
			},
			want: `{
				"id": "%[1]s",
				"name": "Volkswagen Arena",
				"description": "Box office opens two hours before each event.",
				"address": {
					"line1": "Huzur Mah. Azerbaycan Cad. 1",
					"line2": "Gate B",
					"city": "Istanbul",
					"region": "Sariyer",
					"postal_code": "34396",
					"country_code": "TR"
				},
				"location": {"latitude": 41.108934, "longitude": 29.010547},
				"time_zone": "Europe/Istanbul",
				"capacity": 5000,
				"created_at": "%[2]s",
				"updated_at": "%[3]s"
			}`,
		},
		{
			name: "only required fields: the optional ones are null",
			params: eventdb.CreateVenueParams{
				Name:         "Club",
				AddressLine1: "Kemankes Cad. 5",
				City:         "Istanbul",
				CountryCode:  "TR",
				Timezone:     "Europe/Istanbul",
			},
			want: `{
				"id": "%[1]s",
				"name": "Club",
				"description": null,
				"address": {
					"line1": "Kemankes Cad. 5",
					"line2": null,
					"city": "Istanbul",
					"region": null,
					"postal_code": null,
					"country_code": "TR"
				},
				"location": null,
				"time_zone": "Europe/Istanbul",
				"capacity": null,
				"created_at": "%[2]s",
				"updated_at": "%[3]s"
			}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stored := insertVenue(t, pool, tt.params)

			rec := get(t, router, "/api/v1/admin/venues/"+stored.ID.String())

			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want %d; body %s", rec.Code, http.StatusOK, rec.Body)
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Errorf("Content-Type: got %q, want application/json", got)
			}
			checkJSON(t, rec.Body.String(), fmt.Sprintf(tt.want,
				stored.ID, timeJSON(stored.CreatedAt), timeJSON(stored.UpdatedAt)))
		})
	}
}

func TestGetVenue_UnknownOrMalformedIDIs404(t *testing.T) {
	router, _, _ := newVenueRouter(t)

	for name, id := range map[string]string{
		"no venue has this id": uuid.New().String(),
		"not a UUID":           "not-a-uuid",
	} {
		t.Run(name, func(t *testing.T) {
			path := "/api/v1/admin/venues/" + id

			rec := get(t, router, path)

			checkProblem(t, rec, http.StatusNotFound, `{
				"type": "about:blank",
				"title": "Not Found",
				"status": 404,
				"detail": "venue not found",
				"instance": "`+path+`"
			}`)
		})
	}
}

func TestGetVenue_DatabaseFailureIs500WithoutDetails(t *testing.T) {
	router, pool, logs := newVenueRouter(t)
	pool.Close() // every query now fails; Close may be called again by the cleanup
	path := "/api/v1/admin/venues/" + uuid.New().String()

	rec := get(t, router, path)

	// The client learns only that something went wrong on our side...
	checkProblem(t, rec, http.StatusInternalServerError, `{
		"type": "about:blank",
		"title": "Internal Server Error",
		"status": 500,
		"instance": "`+path+`"
	}`)
	// ...and the cause is in the log, with the request's ID.
	entry := findLog(t, logs.String(), "request failed")
	if entry.Level != "ERROR" || entry.RequestID == "" {
		t.Errorf("log: got level %s, request ID %q; want ERROR and an ID", entry.Level, entry.RequestID)
	}
	if !strings.Contains(logs.String(), "get venue") {
		t.Errorf("the log doesn't contain the service's error:\n%s", logs.String())
	}
}

// A missing venue is a normal answer, not a failure of ours, so it is not logged as an error.
func TestGetVenue_NotFoundIsNotLoggedAsAnError(t *testing.T) {
	router, _, logs := newVenueRouter(t)

	get(t, router, "/api/v1/admin/venues/"+uuid.New().String())

	if strings.Contains(logs.String(), "request failed") {
		t.Errorf("a 404 was logged as a failure:\n%s", logs.String())
	}
}
