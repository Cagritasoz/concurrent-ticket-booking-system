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

// These tests go through the whole stack, like the venue and hall tests (see newVenueRouter).

// layoutsOf returns the path to create layouts in this hall.
func layoutsOf(hallID string) string { return hallsPath + "/" + hallID + "/layouts" }

// newLayoutHall stores a venue with one hall and returns the hall.
func newLayoutHall(t *testing.T, pool *pgxpool.Pool) eventdb.Hall {
	t.Helper()
	return insertHall(t, pool, insertVenue(t, pool, minimalVenue("Zorlu PSM")).ID, "Turkcell Stage")
}

func TestCreateLayout_Returns201WithTheDraftAndItsLocation(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	hall := newLayoutHall(t, pool)

	rec := post(t, router, layoutsOf(hall.ID.String()), "application/json", `{
		"name": "  Concert – standing floor ",
		"description": "Floor seats removed."
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
		"hall_id": "%s",
		"name": "Concert – standing floor",
		"description": "Floor seats removed.",
		"status": "draft",
		"version": 1,
		"created_at": "%[3]s",
		"updated_at": "%[3]s"
	}`, body.ID, hall.ID, timeJSON(body.CreatedAt)))
	checkETag(t, rec, `"1"`)
	if got, want := rec.Header().Get("Location"), "/api/v1/admin/layouts/"+body.ID.String(); got != want {
		t.Errorf("Location: got %s, want %s", got, want)
	}

	// It is stored as answered.
	row, err := eventdb.New(pool).GetLayout(t.Context(), body.ID)
	if err != nil {
		t.Fatalf("GetLayout: %v", err)
	}
	if row.HallID != hall.ID || row.Name != "Concert – standing floor" || row.Status != "draft" {
		t.Errorf("stored hall %s, name %q, status %q; want %s, %q, draft",
			row.HallID, row.Name, row.Status, hall.ID, "Concert – standing floor")
	}
}

func TestCreateLayout_DescriptionIsNullWhenLeftOutOrBlank(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := layoutsOf(newLayoutHall(t, pool).ID.String())

	for i, req := range []string{`{"name": "Basketball"}`, `{"name": "Ice show", "description": " "}`} {
		rec := post(t, router, path, "application/json", req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("request %d: status %d, want 201\n%s", i+1, rec.Code, rec.Body)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if value, ok := body["description"]; !ok || value != nil {
			t.Errorf("request %d: description %v (present: %t), want null", i+1, value, ok)
		}
	}
}

func TestCreateLayout_InvalidFieldsAre400WithEveryProblem(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := layoutsOf(newLayoutHall(t, pool).ID.String())

	rec := post(t, router, path, "application/json",
		fmt.Sprintf(`{"name": " ", "description": "%s"}`, strings.Repeat("a", 5001)))

	checkProblem(t, rec, http.StatusBadRequest, fmt.Sprintf(`{
		"type": "about:blank",
		"title": "Bad Request",
		"status": 400,
		"detail": "the request has invalid fields; see errors",
		"instance": "%s",
		"errors": [
			{"field": "name", "message": "is required"},
			{"field": "description", "message": "must be at most 5000 characters"}
		]
	}`, path))
	checkNoLayouts(t, pool)
}

func TestCreateLayout_UnknownOrMalformedHallIs404(t *testing.T) {
	router, pool, _ := newVenueRouter(t)

	for _, id := range []string{uuid.New().String(), "not-a-uuid"} {
		t.Run(id, func(t *testing.T) {
			path := layoutsOf(id)
			rec := post(t, router, path, "application/json", `{"name": "Basketball"}`)

			checkProblem(t, rec, http.StatusNotFound, problemJSON(http.StatusNotFound, "hall not found", path))
		})
	}
	checkNoLayouts(t, pool)
}

// A layout can't be created in an archived hall, nor in a hall of an archived venue. When both
// are archived, the venue is named: the hall can't be restored before it.
func TestCreateLayout_ArchivedHallOrVenueIs409(t *testing.T) {
	tests := []struct {
		name    string
		archive func(t *testing.T, router http.Handler, hall eventdb.Hall)
		detail  string
	}{
		{"archived hall", func(t *testing.T, router http.Handler, hall eventdb.Hall) {
			archiveThroughAPI(t, router, hallPath(hall.ID))
		}, "hall is archived: restore it first"},
		{"archived venue", func(t *testing.T, router http.Handler, hall eventdb.Hall) {
			archiveThroughAPI(t, router, venuesPath+"/"+hall.VenueID.String())
		}, "venue is archived: restore it first"},
		{"both archived", func(t *testing.T, router http.Handler, hall eventdb.Hall) {
			archiveThroughAPI(t, router, hallPath(hall.ID))
			archiveThroughAPI(t, router, venuesPath+"/"+hall.VenueID.String())
		}, "venue is archived: restore it first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, pool, _ := newVenueRouter(t)
			hall := newLayoutHall(t, pool)
			tt.archive(t, router, hall)
			path := layoutsOf(hall.ID.String())

			rec := post(t, router, path, "application/json", `{"name": "Basketball"}`)

			checkProblem(t, rec, http.StatusConflict, problemJSON(http.StatusConflict, tt.detail, path))
			checkNoLayouts(t, pool)
		})
	}
}

func TestCreateLayout_NameTakenInTheHallIs409(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := layoutsOf(newLayoutHall(t, pool).ID.String())
	if rec := post(t, router, path, "application/json", `{"name": "Basketball"}`); rec.Code != http.StatusCreated {
		t.Fatalf("first create: got %d\n%s", rec.Code, rec.Body)
	}

	rec := post(t, router, path, "application/json", `{"name": "BASKETBALL"}`)

	checkProblem(t, rec, http.StatusConflict,
		problemJSON(http.StatusConflict, "another layout of this hall already has this name", path))
}

func TestCreateLayout_UnusableBodies(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := layoutsOf(newLayoutHall(t, pool).ID.String())

	tests := []struct {
		name        string
		contentType string
		body        string
		status      int
		detail      string
	}{
		{"no Content-Type", "", `{"name": "Basketball"}`, http.StatusUnsupportedMediaType,
			`the request body must be JSON, sent with "Content-Type: application/json"`},
		{"empty body", "application/json", "", http.StatusBadRequest, "the request body is empty"},
		{"name as number", "application/json", `{"name": 7}`, http.StatusBadRequest, "name must be a string"},
		// The hall comes from the path; status and version change only through their own requests.
		{"hall_id can't be sent", "application/json", `{"name": "Basketball", "hall_id": "x"}`,
			http.StatusBadRequest, `unknown field "hall_id"`},
		{"status can't be set", "application/json", `{"name": "Basketball", "status": "active"}`,
			http.StatusBadRequest, `unknown field "status"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := post(t, router, path, tt.contentType, tt.body)

			checkProblem(t, rec, tt.status, problemJSON(tt.status, tt.detail, path))
		})
	}
	checkNoLayouts(t, pool)
}

func TestCreateLayout_DatabaseFailureIs500WithoutDetails(t *testing.T) {
	router, pool, logs := newVenueRouter(t)
	path := layoutsOf(newLayoutHall(t, pool).ID.String())
	pool.Close() // every query now fails; Close may be called again by the cleanup

	rec := post(t, router, path, "application/json", `{"name": "Basketball"}`)

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

// archiveThroughAPI archives the venue or hall at this path, at version 1.
func archiveThroughAPI(t *testing.T, router http.Handler, path string) {
	t.Helper()
	if rec := action(t, router, path, "archive", `"1"`); rec.Code != http.StatusOK {
		t.Fatalf("archive %s: got %d\n%s", path, rec.Code, rec.Body)
	}
}

// checkNoLayouts fails the test if any layout is stored.
func checkNoLayouts(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM layouts").Scan(&count); err != nil {
		t.Fatalf("count layouts: %v", err)
	}
	if count != 0 {
		t.Errorf("layouts stored: got %d, want 0", count)
	}
}
