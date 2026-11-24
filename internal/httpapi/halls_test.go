package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event/eventdb"
)

// These tests go through the whole stack, like the venue tests (see newVenueRouter).

// hallsOf returns the path to create halls in this venue.
func hallsOf(venueID string) string { return venuesPath + "/" + venueID + "/halls" }

func TestCreateHall_Returns201WithTheHallAndItsLocation(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	venue := insertVenue(t, pool, minimalVenue("Zorlu PSM"))

	rec := post(t, router, hallsOf(venue.ID.String()), "application/json", `{
		"name": "  Turkcell Stage ",
		"description": "Entrance through Gate B.",
		"capacity": 2500
	}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201\n%s", rec.Code, rec.Body)
	}
	var body struct {
		ID        uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	checkJSON(t, rec.Body.String(), fmt.Sprintf(`{
		"id": "%s",
		"venue_id": "%s",
		"name": "Turkcell Stage",
		"description": "Entrance through Gate B.",
		"capacity": 2500,
		"status": "active",
		"version": 1,
		"created_at": "%[3]s",
		"updated_at": "%[3]s"
	}`, body.ID, venue.ID, timeJSON(body.CreatedAt)))
	checkETag(t, rec, `"1"`)
	if got, want := rec.Header().Get("Location"), "/api/v1/admin/halls/"+body.ID.String(); got != want {
		t.Errorf("Location: got %s, want %s", got, want)
	}

	// It is stored as answered.
	row, err := eventdb.New(pool).GetHall(t.Context(), body.ID)
	if err != nil {
		t.Fatalf("GetHall: %v", err)
	}
	if row.VenueID != venue.ID || row.Name != "Turkcell Stage" {
		t.Errorf("stored venue %s, name %q; want %s, %q", row.VenueID, row.Name, venue.ID, "Turkcell Stage")
	}
}

func TestCreateHall_OptionalFieldsAreNull(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	venue := insertVenue(t, pool, minimalVenue("Zorlu PSM"))

	rec := post(t, router, hallsOf(venue.ID.String()), "application/json",
		`{"name": "Studio", "description": " ", "capacity": null}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201\n%s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	for _, key := range []string{"description", "capacity"} {
		if value, ok := body[key]; !ok || value != nil {
			t.Errorf("%s: got %v (present: %t), want null", key, value, ok)
		}
	}
}

func TestCreateHall_InvalidFieldsAre400WithEveryProblem(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := hallsOf(insertVenue(t, pool, minimalVenue("Zorlu PSM")).ID.String())

	rec := post(t, router, path, "application/json", `{"name": " ", "capacity": 0}`)

	checkProblem(t, rec, http.StatusBadRequest, fmt.Sprintf(`{
		"type": "about:blank",
		"title": "Bad Request",
		"status": 400,
		"detail": "the request has invalid fields; see errors",
		"instance": "%s",
		"errors": [
			{"field": "name", "message": "is required"},
			{"field": "capacity", "message": "must be between 1 and 2147483647"}
		]
	}`, path))
	checkNoHalls(t, pool)
}

func TestCreateHall_UnknownOrMalformedVenueIs404(t *testing.T) {
	router, pool, _ := newVenueRouter(t)

	for _, id := range []string{uuid.New().String(), "not-a-uuid"} {
		t.Run(id, func(t *testing.T) {
			path := hallsOf(id)
			rec := post(t, router, path, "application/json", `{"name": "Studio"}`)

			checkProblem(t, rec, http.StatusNotFound, fmt.Sprintf(`{
				"type": "about:blank",
				"title": "Not Found",
				"status": 404,
				"detail": "venue not found",
				"instance": "%s"
			}`, path))
		})
	}
	checkNoHalls(t, pool)
}

func TestCreateHall_ArchivedVenueIs409(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	venue := insertVenue(t, pool, minimalVenue("Zorlu PSM"))
	path := venuesPath + "/" + venue.ID.String()
	if rec := action(t, router, path, "archive", `"1"`); rec.Code != http.StatusOK {
		t.Fatalf("archive: got %d\n%s", rec.Code, rec.Body)
	}

	rec := post(t, router, path+"/halls", "application/json", `{"name": "Studio"}`)

	checkProblem(t, rec, http.StatusConflict, fmt.Sprintf(`{
		"type": "about:blank",
		"title": "Conflict",
		"status": 409,
		"detail": "venue is archived: restore it first",
		"instance": "%s/halls"
	}`, path))
	checkNoHalls(t, pool)
}

func TestCreateHall_NameTakenInTheVenueIs409(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := hallsOf(insertVenue(t, pool, minimalVenue("Zorlu PSM")).ID.String())
	if rec := post(t, router, path, "application/json", `{"name": "Screen 1"}`); rec.Code != http.StatusCreated {
		t.Fatalf("first create: got %d\n%s", rec.Code, rec.Body)
	}

	rec := post(t, router, path, "application/json", `{"name": "screen 1"}`)

	checkProblem(t, rec, http.StatusConflict, fmt.Sprintf(`{
		"type": "about:blank",
		"title": "Conflict",
		"status": 409,
		"detail": "another hall of this venue already has this name",
		"instance": "%s"
	}`, path))
}

func TestCreateHall_UnusableBodies(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := hallsOf(insertVenue(t, pool, minimalVenue("Zorlu PSM")).ID.String())

	tests := []struct {
		name        string
		contentType string
		body        string
		status      int
		detail      string
	}{
		{"no Content-Type", "", `{"name": "Studio"}`, http.StatusUnsupportedMediaType,
			`the request body must be JSON, sent with "Content-Type: application/json"`},
		{"empty body", "application/json", "", http.StatusBadRequest, "the request body is empty"},
		{"capacity as string", "application/json", `{"name": "Studio", "capacity": "500"}`,
			http.StatusBadRequest, "capacity must be a whole number"},
		{"capacity with a fraction", "application/json", `{"name": "Studio", "capacity": 1.5}`,
			http.StatusBadRequest, "capacity must be a whole number"},
		// The venue comes from the path; status and version change only through their own requests.
		{"venue_id can't be sent", "application/json", `{"name": "Studio", "venue_id": "x"}`,
			http.StatusBadRequest, `unknown field "venue_id"`},
		{"status can't be set", "application/json", `{"name": "Studio", "status": "archived"}`,
			http.StatusBadRequest, `unknown field "status"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := post(t, router, path, tt.contentType, tt.body)

			want, err := json.Marshal(problem{
				Type:     "about:blank",
				Title:    http.StatusText(tt.status),
				Status:   tt.status,
				Detail:   tt.detail,
				Instance: path,
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			checkProblem(t, rec, tt.status, string(want))
		})
	}
	checkNoHalls(t, pool)
}

func TestCreateHall_DatabaseFailureIs500WithoutDetails(t *testing.T) {
	router, pool, logs := newVenueRouter(t)
	path := hallsOf(insertVenue(t, pool, minimalVenue("Zorlu PSM")).ID.String())
	pool.Close() // every query now fails; Close may be called again by the cleanup

	rec := post(t, router, path, "application/json", `{"name": "Studio"}`)

	checkProblem(t, rec, http.StatusInternalServerError, fmt.Sprintf(`{
		"type": "about:blank",
		"title": "Internal Server Error",
		"status": 500,
		"instance": "%s"
	}`, path))
	if !strings.Contains(logs.String(), "request failed") {
		t.Errorf("the error isn't logged:\n%s", logs.String())
	}
}

// checkNoHalls fails the test if any hall is stored.
func checkNoHalls(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM halls").Scan(&count); err != nil {
		t.Fatalf("count halls: %v", err)
	}
	if count != 0 {
		t.Errorf("halls stored: got %d, want 0", count)
	}
}
