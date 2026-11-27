package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event/eventdb"
)

// The tests of the layout routes other than create, through the whole stack like the hall tests.

// insertLayout stores a draft layout of the hall with the generated query, so these tests don't
// depend on POST.
func insertLayout(t *testing.T, pool *pgxpool.Pool, hallID uuid.UUID, name string) eventdb.Layout {
	t.Helper()
	row, err := eventdb.New(pool).CreateLayout(t.Context(), eventdb.CreateLayoutParams{HallID: hallID, Name: name})
	if err != nil {
		t.Fatalf("CreateLayout %q: %v", name, err)
	}
	return row
}

// insertSection stores a standing section in the layout. There are no section routes yet (M1 step
// 6d), so it is plain SQL.
func insertSection(t *testing.T, pool *pgxpool.Pool, layoutID uuid.UUID, name string) {
	t.Helper()
	_, err := pool.Exec(t.Context(),
		`INSERT INTO sections (layout_id, name, kind, capacity, position) VALUES ($1, $2, 'standing', 100, 1)`,
		layoutID, name)
	if err != nil {
		t.Fatalf("insert section %q: %v", name, err)
	}
}

// layoutPath is the URL of one layout.
func layoutPath(id uuid.UUID) string { return layoutsPath + "/" + id.String() }

// layoutBody decodes the fields of a layout response these tests look at.
type layoutBody struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Status      string  `json:"status"`
	Version     int     `json:"version"`
}

func decodeLayout(t *testing.T, rec *httptest.ResponseRecorder) layoutBody {
	t.Helper()
	var body layoutBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v\n%s", err, rec.Body)
	}
	return body
}

// checkStatusAndVersion fails the test unless rec is a 200 with a layout in this status and
// version, and that version as its ETag.
func checkStatusAndVersion(t *testing.T, rec *httptest.ResponseRecorder, status string, version int) {
	t.Helper()
	got := decodeLayout(t, rec)
	if rec.Code != http.StatusOK || got.Status != status || got.Version != version {
		t.Fatalf("got %d %+v; want 200, %s, version %d\n%s", rec.Code, got, status, version, rec.Body)
	}
	checkETag(t, rec, fmt.Sprintf(`"%d"`, version))
}

func TestGetLayout_ReturnsTheLayoutWithItsETag(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	hall := newLayoutHall(t, pool)
	row, err := eventdb.New(pool).CreateLayout(t.Context(), eventdb.CreateLayoutParams{
		HallID: hall.ID, Name: "Concert", Description: new("Standing floor."),
	})
	if err != nil {
		t.Fatalf("CreateLayout: %v", err)
	}

	rec := get(t, router, layoutPath(row.ID))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\n%s", rec.Code, rec.Body)
	}
	checkJSON(t, rec.Body.String(), fmt.Sprintf(`{
		"id": "%s",
		"hall_id": "%s",
		"name": "Concert",
		"description": "Standing floor.",
		"status": "draft",
		"version": 1,
		"created_at": "%[3]s",
		"updated_at": "%[3]s"
	}`, row.ID, hall.ID, timeJSON(row.CreatedAt)))
	checkETag(t, rec, `"1"`)
}

func TestGetLayout_UnknownOrMalformedIDIs404(t *testing.T) {
	router, _, _ := newVenueRouter(t)

	for _, path := range []string{layoutPath(uuid.New()), layoutsPath + "/not-a-uuid"} {
		checkProblem(t, get(t, router, path), http.StatusNotFound, problemJSON(404, "layout not found", path))
	}
}

func TestListLayouts_ReturnsTheHallsLayoutsOfOneStatusByName(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	hall := newLayoutHall(t, pool)
	insertLayout(t, pool, hall.ID, "theatre setup")
	insertLayout(t, pool, hall.ID, "Concert")
	active := insertLayout(t, pool, hall.ID, "Basketball")
	insertSection(t, pool, active.ID, "Court side")
	if rec := action(t, router, layoutPath(active.ID), "activate", `"1"`); rec.Code != http.StatusOK {
		t.Fatalf("activate: got %d\n%s", rec.Code, rec.Body)
	}
	path := layoutsOf(hall.ID.String())

	for query, want := range map[string][]string{
		"":                 {"Basketball"}, // active is the default, as for halls
		"?status=active":   {"Basketball"},
		"?status=draft":    {"Concert", "theatre setup"},
		"?status=archived": {},
	} {
		t.Run(query, func(t *testing.T) {
			rec := get(t, router, path+query)
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200\n%s", rec.Code, rec.Body)
			}
			var body struct {
				Layouts []layoutBody `json:"layouts"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.Layouts == nil {
				t.Fatalf("layouts is null, want a list:\n%s", rec.Body)
			}
			got := make([]string, len(body.Layouts))
			for i, l := range body.Layouts {
				got[i] = l.Name
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestListLayouts_Refusals(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	unknown := layoutsOf(uuid.New().String())
	checkProblem(t, get(t, router, unknown), http.StatusNotFound, problemJSON(404, "hall not found", unknown))

	path := layoutsOf(newLayoutHall(t, pool).ID.String())
	checkProblem(t, get(t, router, path+"?status=frozen"), http.StatusBadRequest, fmt.Sprintf(`{
		"type": "about:blank", "title": "Bad Request", "status": 400,
		"detail": "the request has invalid fields; see errors", "instance": "%s",
		"errors": [{"field": "status", "message": "must be draft, active or archived"}]
	}`, path))
}

func TestPatchLayout_ChangesOnlyTheSentFields(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	row, err := eventdb.New(pool).CreateLayout(t.Context(), eventdb.CreateLayoutParams{
		HallID: newLayoutHall(t, pool).ID, Name: "Concert", Description: new("Standing floor."),
	})
	if err != nil {
		t.Fatalf("CreateLayout: %v", err)
	}

	// null clears the description, and the name isn't sent.
	rec := patch(t, router, layoutPath(row.ID), `"1"`, `{"description": null}`)

	checkStatusAndVersion(t, rec, "draft", 2)
	if got := decodeLayout(t, rec); got.Name != "Concert" || got.Description != nil {
		t.Errorf("got %+v; want name Concert and no description", got)
	}
}

func TestPatchLayout_Refusals(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	hall := newLayoutHall(t, pool)
	layout := insertLayout(t, pool, hall.ID, "Concert")
	insertLayout(t, pool, hall.ID, "Basketball")
	path := layoutPath(layout.ID)
	unknown := layoutPath(uuid.New())

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
		{"unknown layout", unknown, `"1"`, `{"name": "X"}`, http.StatusNotFound,
			problemJSON(404, "layout not found", unknown)},
		{"name of another layout", path, `"1"`, `{"name": "BASKETBALL"}`, http.StatusConflict,
			problemJSON(409, "another layout of this hall already has this name", path)},
		{"name of the wrong type", path, `"1"`, `{"name": 7}`, http.StatusBadRequest,
			problemJSON(400, "name must be a string", path)},
		// A layout never moves to another hall, and its status changes only through its own requests.
		{"hall_id can't change", path, `"1"`, `{"hall_id": "x"}`, http.StatusBadRequest,
			problemJSON(400, `unknown field "hall_id"`, path)},
		{"status can't be set", path, `"1"`, `{"status": "active"}`, http.StatusBadRequest,
			problemJSON(400, `unknown field "status"`, path)},
		{"null name", path, `"1"`, `{"name": null}`, http.StatusBadRequest, fmt.Sprintf(`{
			"type": "about:blank", "title": "Bad Request", "status": 400,
			"detail": "the request has invalid fields; see errors", "instance": "%s",
			"errors": [{"field": "name", "message": "is required"}]
		}`, path)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkProblem(t, patch(t, router, tt.path, tt.ifMatch, tt.body), tt.status, tt.want)
		})
	}

	// Nothing above was stored.
	if rec := get(t, router, path); decodeLayout(t, rec).Version != 1 {
		t.Errorf("the layout changed: %s", rec.Body)
	}
}

// A draft is activated once it has a section and deactivated back to a draft; archiving works
// from active, and restoring brings it back as a draft.
func TestLayoutStatusFlow(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	layout := insertLayout(t, pool, newLayoutHall(t, pool).ID, "Concert")
	path := layoutPath(layout.ID)

	checkProblem(t, action(t, router, path, "activate", `"1"`), http.StatusConflict,
		problemJSON(409, "layout has no sections: add one before activating it", path+"/activate"))
	checkProblem(t, action(t, router, path, "deactivate", `"1"`), http.StatusConflict,
		problemJSON(409, "layout is not active", path+"/deactivate"))

	insertSection(t, pool, layout.ID, "Floor")
	checkStatusAndVersion(t, action(t, router, path, "activate", `"1"`), "active", 2)
	checkProblem(t, action(t, router, path, "activate", `"2"`), http.StatusConflict,
		problemJSON(409, "layout is already active", path+"/activate"))
	checkProblem(t, action(t, router, path, "restore", `"2"`), http.StatusConflict,
		problemJSON(409, "layout is not archived", path+"/restore"))

	checkStatusAndVersion(t, action(t, router, path, "deactivate", `"2"`), "draft", 3)
	checkStatusAndVersion(t, action(t, router, path, "activate", `"3"`), "active", 4)

	checkStatusAndVersion(t, action(t, router, path, "archive", `"4"`), "archived", 5)
	const archived = "layout is archived: restore it first"
	for _, name := range []string{"archive", "activate", "deactivate"} {
		checkProblem(t, action(t, router, path, name, `"5"`), http.StatusConflict,
			problemJSON(409, archived, path+"/"+name))
	}
	checkProblem(t, patch(t, router, path, `"5"`, `{"name": "X"}`), http.StatusConflict,
		problemJSON(409, archived, path))

	checkStatusAndVersion(t, action(t, router, path, "restore", `"5"`), "draft", 6)
}

// Every status change needs If-Match, like PATCH.
func TestLayoutStatusChanges_NeedIfMatch(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := layoutPath(insertLayout(t, pool, newLayoutHall(t, pool).ID, "Concert").ID)

	for _, name := range []string{"activate", "deactivate", "archive", "restore"} {
		if rec := action(t, router, path, name, ""); rec.Code != http.StatusPreconditionRequired {
			t.Errorf("%s without If-Match: got %d, want 428", name, rec.Code)
		}
		checkProblem(t, action(t, router, path, name, `"5"`), http.StatusPreconditionFailed,
			problemJSON(412, versionMismatchDetail, path+"/"+name))
	}
}

// A layout of an archived hall or venue counts as archived: it can be read, listed and deleted,
// but not changed, whatever version is sent.
func TestChangeLayout_InAnArchivedHallOrVenueIs409(t *testing.T) {
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, pool, _ := newVenueRouter(t)
			hall := newLayoutHall(t, pool)
			layout := insertLayout(t, pool, hall.ID, "Concert")
			insertSection(t, pool, layout.ID, "Floor")
			path := layoutPath(layout.ID)
			tt.archive(t, router, hall)

			for _, ifMatch := range []string{`"1"`, `"9"`} { // an old version gets the same answer
				checkProblem(t, patch(t, router, path, ifMatch, `{"name": "X"}`), http.StatusConflict,
					problemJSON(409, tt.detail, path))
				for _, name := range []string{"activate", "deactivate", "archive"} {
					checkProblem(t, action(t, router, path, name, ifMatch), http.StatusConflict,
						problemJSON(409, tt.detail, path+"/"+name))
				}
			}

			if rec := get(t, router, path); rec.Code != http.StatusOK {
				t.Errorf("GET: got %d, want 200", rec.Code)
			}
			if rec := get(t, router, layoutsOf(hall.ID.String())+"?status=draft"); rec.Code != http.StatusOK {
				t.Errorf("list: got %d, want 200", rec.Code)
			}
			if rec := sendIfMatch(t, router, http.MethodDelete, path, `"1"`, "", ""); rec.Code != http.StatusNoContent {
				t.Errorf("DELETE: got %d, want 204\n%s", rec.Code, rec.Body)
			}
		})
	}
}

func TestDeleteLayout_Returns204AndTheLayoutIsGone(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	layout := insertLayout(t, pool, newLayoutHall(t, pool).ID, "Concert")
	insertSection(t, pool, layout.ID, "Floor")
	path := layoutPath(layout.ID)

	checkProblem(t, sendIfMatch(t, router, http.MethodDelete, path, `"2"`, "", ""), http.StatusPreconditionFailed,
		problemJSON(412, versionMismatchDetail, path))
	if rec := sendIfMatch(t, router, http.MethodDelete, path, "", "", ""); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("without If-Match: got %d, want 428", rec.Code)
	}

	rec := sendIfMatch(t, router, http.MethodDelete, path, `"1"`, "", "")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("DELETE: got %d with %q, want 204 and no body", rec.Code, rec.Body)
	}
	checkProblem(t, get(t, router, path), http.StatusNotFound, problemJSON(404, "layout not found", path))
	checkProblem(t, sendIfMatch(t, router, http.MethodDelete, path, `"1"`, "", ""), http.StatusNotFound,
		problemJSON(404, "layout not found", path))
}

func TestChangeLayout_DatabaseFailureIs500WithoutDetails(t *testing.T) {
	router, pool, logs := newVenueRouter(t)
	path := layoutPath(insertLayout(t, pool, newLayoutHall(t, pool).ID, "Concert").ID)
	pool.Close() // every query now fails; Close may be called again by the cleanup

	tests := []struct {
		name     string
		rec      *httptest.ResponseRecorder
		instance string
	}{
		{"GET", get(t, router, path), path},
		{"PATCH", patch(t, router, path, `"1"`, `{"name": "X"}`), path},
		{"activate", action(t, router, path, "activate", `"1"`), path + "/activate"},
		{"DELETE", sendIfMatch(t, router, http.MethodDelete, path, `"1"`, "", ""), path},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkProblem(t, tt.rec, http.StatusInternalServerError, fmt.Sprintf(`{
				"type": "about:blank", "title": "Internal Server Error", "status": 500,
				"instance": "%s"
			}`, tt.instance))
		})
	}
	if !strings.Contains(logs.String(), "request failed") {
		t.Errorf("the error isn't logged:\n%s", logs.String())
	}
}
