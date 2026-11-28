package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/event/eventdb"
)

// These tests go through the whole stack, like the layout tests (see newVenueRouter).

// sectionsOf returns the path to create and list the sections of this layout.
func sectionsOf(layoutID string) string { return layoutsPath + "/" + layoutID + "/sections" }

// sectionPath is the URL of one section.
func sectionPath(id uuid.UUID) string { return sectionsPath + "/" + id.String() }

// newSectionLayout stores a venue, a hall and a draft layout, and returns the layout.
func newSectionLayout(t *testing.T, pool *pgxpool.Pool) eventdb.Layout {
	t.Helper()
	return insertLayout(t, pool, newLayoutHall(t, pool).ID, "Concert")
}

// insertSectionRow stores a section with the generated query and returns it.
func insertSectionRow(t *testing.T, pool *pgxpool.Pool, params eventdb.CreateSectionParams) eventdb.Section {
	t.Helper()
	row, err := eventdb.New(pool).CreateSection(t.Context(), params)
	if err != nil {
		t.Fatalf("CreateSection %q: %v", params.Name, err)
	}
	return row
}

// sectionBody decodes the fields of a section response these tests look at.
type sectionBody struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Capacity *int   `json:"capacity"`
	Position int    `json:"position"`
	Version  int    `json:"version"`
}

func decodeSection(t *testing.T, rec *httptest.ResponseRecorder) sectionBody {
	t.Helper()
	var body sectionBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v\n%s", err, rec.Body)
	}
	return body
}

// activateThroughAPI activates the layout at version 1; it needs a section.
func activateThroughAPI(t *testing.T, router http.Handler, layoutID uuid.UUID) {
	t.Helper()
	if rec := action(t, router, layoutPath(layoutID), "activate", `"1"`); rec.Code != http.StatusOK {
		t.Fatalf("activate: got %d\n%s", rec.Code, rec.Body)
	}
}

// checkSectionCount fails the test unless the number of stored sections is want.
func checkSectionCount(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM sections").Scan(&count); err != nil {
		t.Fatalf("count sections: %v", err)
	}
	if count != want {
		t.Errorf("sections stored: got %d, want %d", count, want)
	}
}

func TestCreateSection_Returns201WithTheSectionAndItsLocation(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	layout := newSectionLayout(t, pool)

	rec := post(t, router, sectionsOf(layout.ID.String()), "application/json",
		`{"name": " Golden Circle ", "kind": "standing", "capacity": 800}`)

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
		"layout_id": "%s",
		"name": "Golden Circle",
		"kind": "standing",
		"capacity": 800,
		"position": 1,
		"version": 1,
		"created_at": "%[3]s",
		"updated_at": "%[3]s"
	}`, body.ID, layout.ID, timeJSON(body.CreatedAt)))
	checkETag(t, rec, `"1"`)
	if got, want := rec.Header().Get("Location"), "/api/v1/admin/sections/"+body.ID.String(); got != want {
		t.Errorf("Location: got %s, want %s", got, want)
	}

	// A seated section has no capacity, and without a position it goes after the last one.
	rec = post(t, router, sectionsOf(layout.ID.String()), "application/json", `{"name": "Block 101", "kind": "seated"}`)
	if got := decodeSection(t, rec); rec.Code != http.StatusCreated || got.Capacity != nil || got.Position != 2 {
		t.Errorf("seated: got %d %+v; want 201, capacity null, position 2", rec.Code, got)
	}
}

func TestCreateSection_InvalidFieldsAre400WithEveryProblem(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := sectionsOf(newSectionLayout(t, pool).ID.String())

	tests := []struct {
		name   string
		body   string
		errors string
	}{
		{"standing without a capacity, bad position", `{"name": " ", "kind": "standing", "position": 0}`, `[
			{"field": "name", "message": "is required"},
			{"field": "capacity", "message": "is required for a standing section"},
			{"field": "position", "message": "must be between 1 and 10000"}
		]`},
		{"seated with a capacity", `{"name": "Block", "kind": "seated", "capacity": 10}`, `[
			{"field": "capacity", "message": "must be left out for a seated section: its seats are its capacity"}
		]`},
		{"unknown or missing kind", `{"name": "Box"}`, `[
			{"field": "kind", "message": "must be seated or standing"}
		]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkProblem(t, post(t, router, path, "application/json", tt.body), http.StatusBadRequest, fmt.Sprintf(`{
				"type": "about:blank", "title": "Bad Request", "status": 400,
				"detail": "the request has invalid fields; see errors", "instance": "%s",
				"errors": %s
			}`, path, tt.errors))
		})
	}
	checkSectionCount(t, pool, 0)
}

func TestCreateSection_Refusals(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	unknown := sectionsOf(uuid.New().String())
	malformed := sectionsOf("not-a-uuid")
	for _, path := range []string{unknown, malformed} {
		checkProblem(t, post(t, router, path, "application/json", `{"name": "Floor", "kind": "seated"}`),
			http.StatusNotFound, problemJSON(404, "layout not found", path))
	}

	layout := newSectionLayout(t, pool)
	path := sectionsOf(layout.ID.String())
	if rec := post(t, router, path, "application/json", `{"name": "Floor", "kind": "standing", "capacity": 100}`); rec.Code != http.StatusCreated {
		t.Fatalf("first create: got %d\n%s", rec.Code, rec.Body)
	}
	checkProblem(t, post(t, router, path, "application/json", `{"name": "FLOOR", "kind": "seated"}`), http.StatusConflict,
		problemJSON(409, "another section of this layout already has this name", path))

	// An active layout's sections don't change: deactivate it first.
	activateThroughAPI(t, router, layout.ID)
	checkProblem(t, post(t, router, path, "application/json", `{"name": "Pit", "kind": "seated"}`), http.StatusConflict,
		problemJSON(409, "layout is active: deactivate it first", path))
	checkSectionCount(t, pool, 1)
}

func TestCreateSection_UnusableBodies(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := sectionsOf(newSectionLayout(t, pool).ID.String())

	tests := []struct {
		name   string
		body   string
		detail string
	}{
		{"capacity as string", `{"name": "Floor", "kind": "standing", "capacity": "500"}`, "capacity must be a whole number"},
		{"layout_id can't be sent", `{"name": "Floor", "kind": "seated", "layout_id": "x"}`, `unknown field "layout_id"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkProblem(t, post(t, router, path, "application/json", tt.body), http.StatusBadRequest,
				problemJSON(400, tt.detail, path))
		})
	}
	checkSectionCount(t, pool, 0)
}

func TestListSections_TheLayoutsSectionsInOrder(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	layout := newSectionLayout(t, pool)
	insertSectionRow(t, pool, eventdb.CreateSectionParams{LayoutID: layout.ID, Name: "Table 10", Kind: "seated", Position: 3})
	insertSectionRow(t, pool, eventdb.CreateSectionParams{LayoutID: layout.ID, Name: "Table 2", Kind: "seated", Position: 2})
	insertSectionRow(t, pool, eventdb.CreateSectionParams{
		LayoutID: layout.ID, Name: "Floor", Kind: "standing", Capacity: new(int32(500)), Position: 1,
	})

	rec := get(t, router, sectionsOf(layout.ID.String()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\n%s", rec.Code, rec.Body)
	}
	var body struct {
		Sections []sectionBody `json:"sections"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	var got []string
	for _, s := range body.Sections {
		got = append(got, s.Name)
	}
	if strings.Join(got, ",") != "Floor,Table 2,Table 10" {
		t.Errorf("got %q, want Floor, Table 2, Table 10", got)
	}

	empty := newSectionLayout(t, pool)
	checkJSON(t, get(t, router, sectionsOf(empty.ID.String())).Body.String(), `{"sections": []}`)
	unknown := sectionsOf(uuid.New().String())
	checkProblem(t, get(t, router, unknown), http.StatusNotFound, problemJSON(404, "layout not found", unknown))
}

func TestGetSection_ReturnsTheSectionWithItsETagOr404(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	row := insertSectionRow(t, pool, eventdb.CreateSectionParams{
		LayoutID: newSectionLayout(t, pool).ID, Name: "Balcony", Kind: "seated", Position: 1,
	})

	rec := get(t, router, sectionPath(row.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200\n%s", rec.Code, rec.Body)
	}
	checkJSON(t, rec.Body.String(), fmt.Sprintf(`{
		"id": "%s", "layout_id": "%s", "name": "Balcony", "kind": "seated", "capacity": null,
		"position": 1, "version": 1, "created_at": "%[3]s", "updated_at": "%[3]s"
	}`, row.ID, row.LayoutID, timeJSON(row.CreatedAt)))
	checkETag(t, rec, `"1"`)

	for _, path := range []string{sectionPath(uuid.New()), sectionsPath + "/not-a-uuid"} {
		checkProblem(t, get(t, router, path), http.StatusNotFound, problemJSON(404, "section not found", path))
	}
}

func TestPatchSection_ChangesOnlyTheSentFields(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	row := insertSectionRow(t, pool, eventdb.CreateSectionParams{
		LayoutID: newSectionLayout(t, pool).ID, Name: "Floor", Kind: "standing", Capacity: new(int32(500)), Position: 1,
	})

	rec := patch(t, router, sectionPath(row.ID), `"1"`, `{"capacity": 650, "position": 3}`)

	got := decodeSection(t, rec)
	if rec.Code != http.StatusOK || got.Name != "Floor" || got.Kind != "standing" || got.Capacity == nil ||
		*got.Capacity != 650 || got.Position != 3 || got.Version != 2 {
		t.Errorf("got %d %+v; want 200, Floor, standing, capacity 650, position 3, version 2", rec.Code, got)
	}
	checkETag(t, rec, `"2"`)
}

func TestPatchSection_Refusals(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	layout := newSectionLayout(t, pool)
	section := insertSectionRow(t, pool, eventdb.CreateSectionParams{
		LayoutID: layout.ID, Name: "Floor", Kind: "standing", Capacity: new(int32(500)), Position: 1,
	})
	insertSectionRow(t, pool, eventdb.CreateSectionParams{LayoutID: layout.ID, Name: "Balcony", Kind: "seated", Position: 2})
	path := sectionPath(section.ID)
	unknown := sectionPath(uuid.New())
	fieldProblem := func(field, message string) string {
		return fmt.Sprintf(`{
			"type": "about:blank", "title": "Bad Request", "status": 400,
			"detail": "the request has invalid fields; see errors", "instance": "%s",
			"errors": [{"field": "%s", "message": "%s"}]
		}`, path, field, message)
	}

	tests := []struct {
		name    string
		path    string
		ifMatch string
		body    string
		status  int
		want    string
	}{
		{"without If-Match", path, "", `{"name": "X"}`, http.StatusPreconditionRequired, problemJSON(428,
			`this request must have an If-Match header with the ETag of your last read, e.g. If-Match: "3"; GET the resource to get it`, path)},
		{"stale If-Match", path, `"7"`, `{"name": "X"}`, http.StatusPreconditionFailed,
			problemJSON(412, versionMismatchDetail, path)},
		{"unknown section", unknown, `"1"`, `{"name": "X"}`, http.StatusNotFound,
			problemJSON(404, "section not found", unknown)},
		{"name of another section", path, `"1"`, `{"name": "balcony"}`, http.StatusConflict,
			problemJSON(409, "another section of this layout already has this name", path)},
		// A section's kind is chosen at creation and never changes.
		{"kind can't change", path, `"1"`, `{"kind": "seated"}`, http.StatusBadRequest,
			problemJSON(400, `unknown field "kind"`, path)},
		{"null capacity on a standing section", path, `"1"`, `{"capacity": null}`, http.StatusBadRequest,
			fieldProblem("capacity", "is required for a standing section")},
		{"null position", path, `"1"`, `{"position": null}`, http.StatusBadRequest,
			fieldProblem("position", "is required")},
		{"position over the limit", path, `"1"`, `{"position": 10001}`, http.StatusBadRequest,
			fieldProblem("position", "must be between 1 and 10000")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkProblem(t, patch(t, router, tt.path, tt.ifMatch, tt.body), tt.status, tt.want)
		})
	}

	// Nothing above was stored.
	if rec := get(t, router, path); decodeSection(t, rec).Version != 1 {
		t.Errorf("the section changed: %s", rec.Body)
	}
}

// A section of an active layout can't be changed or deleted, whatever version is sent; reading
// it still works.
func TestChangeSection_InAnActiveLayoutIs409(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	layout := newSectionLayout(t, pool)
	section := insertSectionRow(t, pool, eventdb.CreateSectionParams{LayoutID: layout.ID, Name: "Balcony", Kind: "seated", Position: 1})
	path := sectionPath(section.ID)
	activateThroughAPI(t, router, layout.ID)
	const detail = "layout is active: deactivate it first"

	for _, ifMatch := range []string{`"1"`, `"9"`} {
		checkProblem(t, patch(t, router, path, ifMatch, `{"name": "X"}`), http.StatusConflict, problemJSON(409, detail, path))
		checkProblem(t, sendIfMatch(t, router, http.MethodDelete, path, ifMatch, "", ""), http.StatusConflict,
			problemJSON(409, detail, path))
	}
	if rec := get(t, router, path); rec.Code != http.StatusOK {
		t.Errorf("GET: got %d, want 200", rec.Code)
	}

	// Deactivated, the layout is a draft again and its sections can change.
	if rec := action(t, router, layoutPath(layout.ID), "deactivate", `"2"`); rec.Code != http.StatusOK {
		t.Fatalf("deactivate: got %d\n%s", rec.Code, rec.Body)
	}
	if rec := patch(t, router, path, `"1"`, `{"name": "Upper Balcony"}`); rec.Code != http.StatusOK {
		t.Errorf("PATCH after deactivate: got %d, want 200\n%s", rec.Code, rec.Body)
	}
}

func TestDeleteSection_Returns204AndTheSectionIsGone(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	section := insertSectionRow(t, pool, eventdb.CreateSectionParams{
		LayoutID: newSectionLayout(t, pool).ID, Name: "Balcony", Kind: "seated", Position: 1,
	})
	path := sectionPath(section.ID)

	checkProblem(t, sendIfMatch(t, router, http.MethodDelete, path, `"2"`, "", ""), http.StatusPreconditionFailed,
		problemJSON(412, versionMismatchDetail, path))
	if rec := sendIfMatch(t, router, http.MethodDelete, path, "", "", ""); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("without If-Match: got %d, want 428", rec.Code)
	}

	rec := sendIfMatch(t, router, http.MethodDelete, path, `"1"`, "", "")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("DELETE: got %d with %q, want 204 and no body", rec.Code, rec.Body)
	}
	checkProblem(t, get(t, router, path), http.StatusNotFound, problemJSON(404, "section not found", path))
	checkProblem(t, sendIfMatch(t, router, http.MethodDelete, path, `"1"`, "", ""), http.StatusNotFound,
		problemJSON(404, "section not found", path))
}

func TestSection_DatabaseFailureIs500WithoutDetails(t *testing.T) {
	router, pool, logs := newVenueRouter(t)
	layout := newSectionLayout(t, pool)
	section := insertSectionRow(t, pool, eventdb.CreateSectionParams{LayoutID: layout.ID, Name: "Balcony", Kind: "seated", Position: 1})
	path := sectionPath(section.ID)
	pool.Close() // every query now fails; Close may be called again by the cleanup

	tests := []struct {
		name     string
		rec      *httptest.ResponseRecorder
		instance string
	}{
		{"POST", post(t, router, sectionsOf(layout.ID.String()), "application/json", `{"name": "Pit", "kind": "seated"}`),
			sectionsOf(layout.ID.String())},
		{"GET", get(t, router, path), path},
		{"PATCH", patch(t, router, path, `"1"`, `{"name": "X"}`), path},
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
