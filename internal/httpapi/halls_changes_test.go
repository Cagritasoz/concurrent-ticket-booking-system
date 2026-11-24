package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event/eventdb"
)

// The tests of the hall endpoints other than create (halls_test.go), through the whole stack.

// insertHall stores a hall with the generated query, so these tests don't depend on POST.
func insertHall(t *testing.T, pool *pgxpool.Pool, venueID uuid.UUID, name string) eventdb.Hall {
	t.Helper()
	row, err := eventdb.New(pool).CreateHall(t.Context(), eventdb.CreateHallParams{VenueID: venueID, Name: name})
	if err != nil {
		t.Fatalf("CreateHall %q: %v", name, err)
	}
	return row
}

// hallPath is the URL of one hall.
func hallPath(id uuid.UUID) string { return hallsPath + "/" + id.String() }

// hallBody decodes the fields of a hall response these tests look at.
type hallBody struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Capacity    *int    `json:"capacity"`
	Status      string  `json:"status"`
	Version     int     `json:"version"`
}

func decodeHall(t *testing.T, rec *httptest.ResponseRecorder) hallBody {
	t.Helper()
	var body hallBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v\n%s", err, rec.Body)
	}
	return body
}

// problemJSON is the Problem Details body these tests expect.
func problemJSON(status int, detail, instance string) string {
	b, _ := json.Marshal(problem{Type: "about:blank", Title: http.StatusText(status), Status: status,
		Detail: detail, Instance: instance}) // a problem always encodes
	return string(b)
}

func TestGetHall_ReturnsTheHallWithItsETag(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	venue := insertVenue(t, pool, minimalVenue("Zorlu PSM"))
	row, err := eventdb.New(pool).CreateHall(t.Context(), eventdb.CreateHallParams{
		VenueID: venue.ID, Name: "Turkcell Stage", Description: new("Gate B"), Capacity: new(int32(2500)),
	})
	if err != nil {
		t.Fatalf("CreateHall: %v", err)
	}

	rec := get(t, router, hallPath(row.ID))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\n%s", rec.Code, rec.Body)
	}
	checkETag(t, rec, `"1"`)
	checkJSON(t, rec.Body.String(), fmt.Sprintf(`{
		"id": "%s", "venue_id": "%s", "name": "Turkcell Stage", "description": "Gate B",
		"capacity": 2500, "status": "active", "version": 1,
		"created_at": "%[3]s", "updated_at": "%[3]s"
	}`, row.ID, venue.ID, timeJSON(row.CreatedAt)))
}

func TestGetHall_UnknownOrMalformedIDIs404(t *testing.T) {
	router, _, _ := newVenueRouter(t)

	for _, path := range []string{hallPath(uuid.New()), hallsPath + "/not-a-uuid"} {
		rec := get(t, router, path)
		checkProblem(t, rec, http.StatusNotFound, problemJSON(http.StatusNotFound, "hall not found", path))
	}
}

func TestGetHall_DatabaseFailureIs500WithoutDetails(t *testing.T) {
	router, pool, logs := newVenueRouter(t)
	pool.Close() // every query now fails; Close may be called again by the cleanup

	path := hallPath(uuid.New())
	rec := get(t, router, path)

	checkProblem(t, rec, http.StatusInternalServerError, problemJSON(http.StatusInternalServerError, "", path))
	if !strings.Contains(logs.String(), "get hall") {
		t.Errorf("the log doesn't contain the service's error:\n%s", logs.String())
	}
}

func TestListHalls_ReturnsTheVenuesHallsOfOneStatusByName(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	venue := insertVenue(t, pool, minimalVenue("Zorlu PSM"))
	insertHall(t, pool, venue.ID, "studio")
	insertHall(t, pool, venue.ID, "Screen 2")
	annex := insertHall(t, pool, venue.ID, "Annex")
	if rec := action(t, router, hallPath(annex.ID), "archive", `"1"`); rec.Code != http.StatusOK {
		t.Fatalf("archive: got %d\n%s", rec.Code, rec.Body)
	}

	names := func(query string) []string {
		t.Helper()
		rec := get(t, router, hallsOf(venue.ID.String())+query)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: got %d\n%s", query, rec.Code, rec.Body)
		}
		var body struct {
			Halls []hallBody `json:"halls"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got := make([]string, len(body.Halls))
		for i, h := range body.Halls {
			got[i] = h.Name
		}
		return got
	}
	if got, want := names(""), []string{"Screen 2", "studio"}; !reflect.DeepEqual(got, want) {
		t.Errorf("default (active): got %q, want %q", got, want)
	}
	if got, want := names("?status=archived"), []string{"Annex"}; !reflect.DeepEqual(got, want) {
		t.Errorf("archived: got %q, want %q", got, want)
	}
}

func TestListHalls_NoHallsIsAnEmptyList(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	venue := insertVenue(t, pool, minimalVenue("Zorlu PSM"))

	rec := get(t, router, hallsOf(venue.ID.String()))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	checkJSON(t, rec.Body.String(), `{"halls": []}`)
}

func TestListHalls_Refusals(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	venue := insertVenue(t, pool, minimalVenue("Zorlu PSM"))

	for _, id := range []string{uuid.New().String(), "not-a-uuid"} {
		path := hallsOf(id)
		checkProblem(t, get(t, router, path), http.StatusNotFound, problemJSON(http.StatusNotFound, "venue not found", path))
	}

	path := hallsOf(venue.ID.String())
	checkProblem(t, get(t, router, path+"?status=deleted"), http.StatusBadRequest, fmt.Sprintf(`{
		"type": "about:blank", "title": "Bad Request", "status": 400,
		"detail": "the request has invalid fields; see errors", "instance": "%s",
		"errors": [{"field": "status", "message": "must be active or archived"}]
	}`, path))
}

func TestPatchHall_ChangesOnlyTheSentFields(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	venue := insertVenue(t, pool, minimalVenue("Zorlu PSM"))
	row, err := eventdb.New(pool).CreateHall(t.Context(), eventdb.CreateHallParams{
		VenueID: venue.ID, Name: "Studio", Description: new("Gate B"), Capacity: new(int32(300)),
	})
	if err != nil {
		t.Fatalf("CreateHall: %v", err)
	}

	// null clears the description, a value sets the capacity, and the name isn't sent.
	rec := patch(t, router, hallPath(row.ID), `"1"`, `{"description": null, "capacity": 450}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\n%s", rec.Code, rec.Body)
	}
	checkETag(t, rec, `"2"`)
	got := decodeHall(t, rec)
	if got.Name != "Studio" || got.Description != nil || got.Capacity == nil || *got.Capacity != 450 || got.Version != 2 {
		t.Errorf("got %+v; want name Studio, no description, capacity 450, version 2", got)
	}
}

func TestPatchHall_Refusals(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	venue := insertVenue(t, pool, minimalVenue("Zorlu PSM"))
	hall := insertHall(t, pool, venue.ID, "Studio")
	insertHall(t, pool, venue.ID, "Screen 1")
	path := hallPath(hall.ID)
	unknown := hallPath(uuid.New())

	tests := []struct {
		name    string
		path    string
		ifMatch string
		body    string
		status  int
		want    string // the whole Problem Details body
	}{
		{"without If-Match", path, "", `{"name": "X"}`, http.StatusPreconditionRequired, problemJSON(428,
			`this request must have an If-Match header with the ETag of your last read, e.g. If-Match: "3"; GET the resource to get it`, path)},
		{"stale If-Match", path, `"7"`, `{"name": "X"}`, http.StatusPreconditionFailed,
			problemJSON(412, versionMismatchDetail, path)},
		{"unknown hall", unknown, `"1"`, `{"name": "X"}`, http.StatusNotFound,
			problemJSON(404, "hall not found", unknown)},
		{"name of another hall", path, `"1"`, `{"name": "screen 1"}`, http.StatusConflict,
			problemJSON(409, "another hall of this venue already has this name", path)},
		{"capacity of the wrong type", path, `"1"`, `{"capacity": "many"}`, http.StatusBadRequest,
			problemJSON(400, "capacity must be a whole number", path)},
		{"venue_id can't change", path, `"1"`, `{"venue_id": "x"}`, http.StatusBadRequest,
			problemJSON(400, `unknown field "venue_id"`, path)},
		{"null name", path, `"1"`, `{"name": null}`, http.StatusBadRequest, fmt.Sprintf(`{
			"type": "about:blank", "title": "Bad Request", "status": 400,
			"detail": "the request has invalid fields; see errors", "instance": "%s",
			"errors": [{"field": "name", "message": "is required"}]
		}`, path)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := patch(t, router, tt.path, tt.ifMatch, tt.body)
			checkProblem(t, rec, tt.status, tt.want)
		})
	}

	// Nothing above was stored.
	if rec := get(t, router, path); decodeHall(t, rec).Version != 1 {
		t.Errorf("the hall changed: %s", rec.Body)
	}
}

func TestArchiveAndRestoreHall(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	venue := insertVenue(t, pool, minimalVenue("Zorlu PSM"))
	path := hallPath(insertHall(t, pool, venue.ID, "Studio").ID)

	rec := action(t, router, path, "archive", `"1"`)
	if got := decodeHall(t, rec); rec.Code != http.StatusOK || got.Status != "archived" || got.Version != 2 {
		t.Fatalf("archive: got %d %+v; want 200, archived, version 2", rec.Code, got)
	}
	checkETag(t, rec, `"2"`)

	checkProblem(t, action(t, router, path, "archive", `"2"`), http.StatusConflict,
		problemJSON(409, "hall is archived: restore it first", path+"/archive"))
	checkProblem(t, patch(t, router, path, `"2"`, `{"name": "X"}`), http.StatusConflict,
		problemJSON(409, "hall is archived: restore it first", path))

	rec = action(t, router, path, "restore", `"2"`)
	if got := decodeHall(t, rec); rec.Code != http.StatusOK || got.Status != "active" || got.Version != 3 {
		t.Fatalf("restore: got %d %+v; want 200, active, version 3", rec.Code, got)
	}
	checkProblem(t, action(t, router, path, "restore", `"3"`), http.StatusConflict,
		problemJSON(409, "hall is not archived", path+"/restore"))
}

func TestChangeHall_InAnArchivedVenueIs409(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	venue := insertVenue(t, pool, minimalVenue("Zorlu PSM"))
	path := hallPath(insertHall(t, pool, venue.ID, "Studio").ID)
	if rec := action(t, router, venuesPath+"/"+venue.ID.String(), "archive", `"1"`); rec.Code != http.StatusOK {
		t.Fatalf("archive venue: got %d\n%s", rec.Code, rec.Body)
	}
	const detail = "venue is archived: restore it first"

	checkProblem(t, patch(t, router, path, `"1"`, `{"name": "X"}`), http.StatusConflict, problemJSON(409, detail, path))
	checkProblem(t, action(t, router, path, "archive", `"1"`), http.StatusConflict, problemJSON(409, detail, path+"/archive"))

	// Reading it still works, and so does deleting it.
	if rec := get(t, router, path); rec.Code != http.StatusOK {
		t.Errorf("GET: got %d, want 200", rec.Code)
	}
	if rec := sendIfMatch(t, router, http.MethodDelete, path, `"1"`, "", ""); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE: got %d, want 204\n%s", rec.Code, rec.Body)
	}
}

func TestDeleteHall_Returns204AndTheHallIsGone(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	venue := insertVenue(t, pool, minimalVenue("Zorlu PSM"))
	path := hallPath(insertHall(t, pool, venue.ID, "Studio").ID)

	checkProblem(t, sendIfMatch(t, router, http.MethodDelete, path, `"2"`, "", ""), http.StatusPreconditionFailed,
		problemJSON(412, versionMismatchDetail, path))
	if rec := sendIfMatch(t, router, http.MethodDelete, path, "", "", ""); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("without If-Match: got %d, want 428", rec.Code)
	}

	rec := sendIfMatch(t, router, http.MethodDelete, path, `"1"`, "", "")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("DELETE: got %d with %q, want 204 and no body", rec.Code, rec.Body)
	}
	checkProblem(t, get(t, router, path), http.StatusNotFound, problemJSON(404, "hall not found", path))
	checkProblem(t, sendIfMatch(t, router, http.MethodDelete, path, `"1"`, "", ""), http.StatusNotFound,
		problemJSON(404, "hall not found", path))
}
