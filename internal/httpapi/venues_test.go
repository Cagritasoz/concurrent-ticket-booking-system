package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
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

// insertVenue stores a venue with the generated query, so the read tests don't depend on POST.
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
				"status": "active",
				"version": 1,
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
				"status": "active",
				"version": 1,
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
			checkETag(t, rec, `"1"`) // the version, for a later If-Match
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

// post sends a POST request with this body and Content-Type to handler, in memory.
func post(t *testing.T, handler http.Handler, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	return send(t, handler, http.MethodPost, path, contentType, body)
}

// send sends a request with this method, body and Content-Type (none if "") to handler, in memory.
func send(t *testing.T, handler http.Handler, method, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	return sendIfMatch(t, handler, method, path, "", contentType, body)
}

// sendIfMatch is send with an If-Match header (none if ""), for the requests that change a venue.
func sendIfMatch(t *testing.T, handler http.Handler, method, path, ifMatch, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// checkETag fails the test unless rec has this ETag header, e.g. `"2"`.
func checkETag(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if got := rec.Header().Get("ETag"); got != want {
		t.Errorf("ETag: got %s, want %s", got, want)
	}
}

const venuesPath = "/api/v1/admin/venues"

// validVenueJSON is a request body that passes every rule.
const validVenueJSON = `{
	"name": "Volkswagen Arena",
	"address": {"line1": "Huzur Mah. Azerbaycan Cad. 1", "city": "Istanbul", "country_code": "TR"},
	"time_zone": "Europe/Istanbul"
}`

func TestCreateVenue_Returns201WithTheVenueAndItsLocation(t *testing.T) {
	router, _, _ := newVenueRouter(t)

	rec := post(t, router, venuesPath, "application/json; charset=utf-8", `{
		"name": "  Volkswagen Arena ",
		"description": "",
		"address": {
			"line1": "Huzur Mah. Azerbaycan Cad. 1",
			"line2": "Gate B",
			"city": "Istanbul",
			"region": null,
			"postal_code": "34396",
			"country_code": "TR"
		},
		"location": {"latitude": 41.108934, "longitude": 29.010547},
		"time_zone": "Europe/Istanbul"
	}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want %d; body %s", rec.Code, http.StatusCreated, rec.Body)
	}
	// The id and timestamps come from Postgres, so they are read from the body first.
	var created struct {
		ID        string `json:"id"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	// The body is the venue as stored: the name trimmed, the blank description stored as null.
	checkJSON(t, rec.Body.String(), fmt.Sprintf(`{
		"id": %q,
		"name": "Volkswagen Arena",
		"description": null,
		"address": {
			"line1": "Huzur Mah. Azerbaycan Cad. 1",
			"line2": "Gate B",
			"city": "Istanbul",
			"region": null,
			"postal_code": "34396",
			"country_code": "TR"
		},
		"location": {"latitude": 41.108934, "longitude": 29.010547},
		"time_zone": "Europe/Istanbul",
		"status": "active",
		"version": 1,
		"created_at": %q,
		"updated_at": %q
	}`, created.ID, created.CreatedAt, created.UpdatedAt))
	if _, err := uuid.Parse(created.ID); err != nil {
		t.Errorf("id %q is not a UUID", created.ID)
	}
	checkETag(t, rec, `"1"`)

	// The Location header points at the new venue, and reading it gives the same body.
	location := rec.Header().Get("Location")
	if want := venuesPath + "/" + created.ID; location != want {
		t.Fatalf("Location: got %q, want %q", location, want)
	}
	got := get(t, router, location)
	if got.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d", location, got.Code)
	}
	checkJSON(t, got.Body.String(), rec.Body.String())
}

func TestCreateVenue_InvalidFieldsAre400WithEveryProblem(t *testing.T) {
	router, pool, _ := newVenueRouter(t)

	rec := post(t, router, venuesPath, "application/json", `{
		"name": " ",
		"address": {"line1": "Huzur Mah. 1", "city": "Istanbul", "country_code": "tr"},
		"location": {"latitude": 91, "longitude": 29},
		"time_zone": "Mars/Olympus_Mons"
	}`)

	checkProblem(t, rec, http.StatusBadRequest, `{
		"type": "about:blank",
		"title": "Bad Request",
		"status": 400,
		"detail": "the request has invalid fields; see errors",
		"instance": "/api/v1/admin/venues",
		"errors": [
			{"field": "name", "message": "is required"},
			{"field": "address.country_code", "message": "must be an ISO 3166-1 alpha-2 country code: two capital letters, e.g. TR"},
			{"field": "location.latitude", "message": "must be between -90 and 90"},
			{"field": "time_zone", "message": "must be an IANA time zone name, e.g. Europe/Istanbul"}
		]
	}`)
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM venues").Scan(&count); err != nil {
		t.Fatalf("count venues: %v", err)
	}
	if count != 0 {
		t.Errorf("venues stored: got %d, want 0", count)
	}
}

func TestCreateVenue_LocationNeedsBothCoordinates(t *testing.T) {
	router, _, _ := newVenueRouter(t)

	rec := post(t, router, venuesPath, "application/json", `{
		"name": "Volkswagen Arena",
		"address": {"line1": "Huzur Mah. 1", "city": "Istanbul", "country_code": "TR"},
		"location": {"latitude": 41.1},
		"time_zone": "Europe/Istanbul"
	}`)

	checkProblem(t, rec, http.StatusBadRequest, `{
		"type": "about:blank",
		"title": "Bad Request",
		"status": 400,
		"detail": "the request has invalid fields; see errors",
		"instance": "/api/v1/admin/venues",
		"errors": [{"field": "location.longitude", "message": "is required"}]
	}`)
}

func TestCreateVenue_UnusableBodies(t *testing.T) {
	router, _, _ := newVenueRouter(t)
	const mustBeJSON = `the request body must be JSON, sent with "Content-Type: application/json"`

	tests := []struct {
		name        string
		contentType string
		body        string
		status      int
		detail      string
	}{
		{"no Content-Type", "", validVenueJSON, http.StatusUnsupportedMediaType, mustBeJSON},
		{"form instead of JSON", "application/x-www-form-urlencoded", "name=Arena", http.StatusUnsupportedMediaType, mustBeJSON},
		{"empty body", "application/json", "", http.StatusBadRequest, "the request body is empty"},
		{"cut off", "application/json", `{"name": "Arena"`, http.StatusBadRequest,
			"the request body is not valid JSON: it ends too early"},
		{"not JSON", "application/json", `{name: "Arena"}`, http.StatusBadRequest,
			"the request body is not valid JSON: invalid character 'n' looking for beginning of object key string (at byte 2)"},
		{"array instead of object", "application/json", `[]`, http.StatusBadRequest, "the request body must be a JSON object"},
		{"number as string", "application/json", `{"location": {"latitude": "41.1"}}`, http.StatusBadRequest,
			"location.latitude must be a number"},
		{"wrong type in a nested field", "application/json", `{"address": {"city": 7}}`, http.StatusBadRequest,
			"address.city must be a string"},
		{"unknown field", "application/json", `{"title": "Arena"}`, http.StatusBadRequest, `unknown field "title"`},
		// Capacity belongs to halls; status and version change only through their own requests.
		{"capacity is not a venue field", "application/json", `{"capacity": 5000}`, http.StatusBadRequest,
			`unknown field "capacity"`},
		{"status can't be set", "application/json", `{"status": "archived"}`, http.StatusBadRequest,
			`unknown field "status"`},
		{"two objects", "application/json", validVenueJSON + `{}`, http.StatusBadRequest,
			"the request body must contain one JSON object and nothing after it"},
		{"too large", "application/json", `{"name": "` + strings.Repeat("a", maxRequestBodyBytes) + `"}`,
			http.StatusRequestEntityTooLarge, "the request body must be at most 65536 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := post(t, router, venuesPath, tt.contentType, tt.body)

			want, err := json.Marshal(problem{
				Type:     "about:blank",
				Title:    http.StatusText(tt.status),
				Status:   tt.status,
				Detail:   tt.detail,
				Instance: venuesPath,
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			checkProblem(t, rec, tt.status, string(want))
		})
	}
}

func TestCreateVenue_DatabaseFailureIs500WithoutDetails(t *testing.T) {
	router, pool, logs := newVenueRouter(t)
	pool.Close() // every query now fails; Close may be called again by the cleanup

	rec := post(t, router, venuesPath, "application/json", validVenueJSON)

	checkProblem(t, rec, http.StatusInternalServerError, `{
		"type": "about:blank",
		"title": "Internal Server Error",
		"status": 500,
		"instance": "/api/v1/admin/venues"
	}`)
	if !strings.Contains(logs.String(), "create venue") {
		t.Errorf("the log doesn't contain the service's error:\n%s", logs.String())
	}
}

// listPage is the part of a list response these tests look at.
type listPage struct {
	Venues []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"venues"`
	NextCursor *string `json:"next_cursor"`
}

// getPage sends GET /venues with this query and decodes the 200 response.
func getPage(t *testing.T, router http.Handler, query url.Values) listPage {
	t.Helper()
	rec := get(t, router, venuesPath+"?"+query.Encode())
	if rec.Code != http.StatusOK {
		t.Fatalf("GET ?%s: status %d, body %s", query.Encode(), rec.Code, rec.Body)
	}
	var page listPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return page
}

// minimalVenue returns the parameters of a venue with only the required fields.
func minimalVenue(name string) eventdb.CreateVenueParams {
	return eventdb.CreateVenueParams{
		Name: name, AddressLine1: "Huzur Mah. 1", City: "Istanbul", CountryCode: "TR", Timezone: "Europe/Istanbul",
	}
}

func TestListVenues_FollowsNextCursorToTheLastPage(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	for _, name := range []string{"Club", "Arena", "Hall"} {
		insertVenue(t, pool, minimalVenue(name))
	}

	first := getPage(t, router, url.Values{"limit": {"2"}})
	if first.NextCursor == nil {
		t.Fatalf("first page: next_cursor is null, want a cursor")
	}
	second := getPage(t, router, url.Values{"limit": {"2"}, "cursor": {*first.NextCursor}})

	var got []string
	for _, v := range append(first.Venues, second.Venues...) {
		got = append(got, v.Name)
	}
	if want := []string{"Arena", "Club", "Hall"}; !slices.Equal(got, want) {
		t.Errorf("names: got %v, want %v", got, want)
	}
	if second.NextCursor != nil {
		t.Errorf("last page: next_cursor %q, want null", *second.NextCursor)
	}
}

func TestListVenues_PageBody(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	stored := insertVenue(t, pool, minimalVenue("Club"))

	rec := get(t, router, venuesPath)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d; body %s", rec.Code, http.StatusOK, rec.Body)
	}
	// Each venue has the same form as GET /venues/{id}; one page is the last, so next_cursor is null.
	checkJSON(t, rec.Body.String(), fmt.Sprintf(`{
		"venues": [{
			"id": %q,
			"name": "Club",
			"description": null,
			"address": {
				"line1": "Huzur Mah. 1", "line2": null, "city": "Istanbul",
				"region": null, "postal_code": null, "country_code": "TR"
			},
			"location": null,
			"time_zone": "Europe/Istanbul",
			"status": "active",
			"version": 1,
			"created_at": %q,
			"updated_at": %q
		}],
		"next_cursor": null
	}`, stored.ID, timeJSON(stored.CreatedAt), timeJSON(stored.UpdatedAt)))
	// A list has no ETag: each venue's version is in the body instead.
	checkETag(t, rec, "")
}

// The list shows active venues unless ?status=archived asks for the archived ones.
func TestListVenues_StatusSelectsActiveOrArchived(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	insertVenue(t, pool, minimalVenue("Club"))
	archived := insertVenue(t, pool, minimalVenue("Arena"))
	if rec := sendIfMatch(t, router, http.MethodPost, venuesPath+"/"+archived.ID.String()+"/archive", `"1"`, "", ""); rec.Code != http.StatusOK {
		t.Fatalf("archive: status %d, body %s", rec.Code, rec.Body)
	}

	for query, want := range map[string]string{"": "Club", "status=active": "Club", "status=archived": "Arena"} {
		page := getPage(t, router, mustParseQuery(t, query))
		if len(page.Venues) != 1 || page.Venues[0].Name != want {
			t.Errorf("?%s: got %+v, want only %s", query, page.Venues, want)
		}
	}
}

func mustParseQuery(t *testing.T, query string) url.Values {
	t.Helper()
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("parse query %q: %v", query, err)
	}
	return values
}

func TestListVenues_NoVenuesIsAnEmptyList(t *testing.T) {
	router, _, _ := newVenueRouter(t)

	rec := get(t, router, venuesPath)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	checkJSON(t, rec.Body.String(), `{"venues": [], "next_cursor": null}`) // [] , not null
}

func TestListVenues_LimitDefaultsTo20(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	for i := range event.DefaultVenuePageSize + 1 {
		insertVenue(t, pool, minimalVenue(fmt.Sprintf("Venue %02d", i)))
	}

	page := getPage(t, router, url.Values{})

	if len(page.Venues) != event.DefaultVenuePageSize || page.NextCursor == nil {
		t.Errorf("got %d venues, next_cursor %v; want %d and a cursor",
			len(page.Venues), page.NextCursor, event.DefaultVenuePageSize)
	}
}

func TestListVenues_InvalidQueryIs400(t *testing.T) {
	router, _, _ := newVenueRouter(t)
	const notANumber = `{"field": "limit", "message": "must be a whole number between 1 and 100"}`
	const outOfRange = `{"field": "limit", "message": "must be between 1 and 100"}`
	const badCursor = `{"field": "cursor", "message": "must be the next_cursor of a previous page"}`

	tests := []struct {
		name   string
		query  string
		errors string
	}{
		{"limit not a number", "limit=ten", notANumber},
		{"limit a fraction", "limit=2.5", notANumber},
		{"limit zero", "limit=0", outOfRange},
		{"limit over the maximum", "limit=101", outOfRange},
		{"cursor not base64", "cursor=%21%21", badCursor},
		{"cursor base64 but not a cursor", "cursor=" + base64.RawURLEncoding.EncodeToString([]byte("hello")), badCursor},
		{"cursor with an id that isn't a UUID", "cursor=" +
			base64.RawURLEncoding.EncodeToString([]byte(`{"name":"Arena","id":"7"}`)), badCursor},
		{"both wrong: every problem at once", "limit=ten&cursor=%21%21", notANumber + "," + badCursor},
		{"unknown status", "status=closed", `{"field": "status", "message": "must be active or archived"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(t, router, venuesPath+"?"+tt.query)

			checkProblem(t, rec, http.StatusBadRequest, `{
				"type": "about:blank",
				"title": "Bad Request",
				"status": 400,
				"detail": "the request has invalid fields; see errors",
				"instance": "/api/v1/admin/venues",
				"errors": [`+tt.errors+`]
			}`)
		})
	}
}

func TestListVenues_DatabaseFailureIs500WithoutDetails(t *testing.T) {
	router, pool, logs := newVenueRouter(t)
	pool.Close() // every query now fails; Close may be called again by the cleanup

	rec := get(t, router, venuesPath)

	checkProblem(t, rec, http.StatusInternalServerError, `{
		"type": "about:blank",
		"title": "Internal Server Error",
		"status": 500,
		"instance": "/api/v1/admin/venues"
	}`)
	if !strings.Contains(logs.String(), "list venues") {
		t.Errorf("the log doesn't contain the service's error:\n%s", logs.String())
	}
}

// fullVenueParams returns the parameters of a venue with every field set.
func fullVenueParams() eventdb.CreateVenueParams {
	return eventdb.CreateVenueParams{
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
	}
}

// patch sends a PATCH request with this body as JSON Merge Patch, for the version in ifMatch.
func patch(t *testing.T, handler http.Handler, path, ifMatch, body string) *httptest.ResponseRecorder {
	t.Helper()
	return sendIfMatch(t, handler, http.MethodPatch, path, ifMatch, mergePatchContentType, body)
}

// action sends a POST to one of a venue's action endpoints (archive, restore), for the version
// in ifMatch. The actions have no body.
func action(t *testing.T, handler http.Handler, path, name, ifMatch string) *httptest.ResponseRecorder {
	t.Helper()
	return sendIfMatch(t, handler, http.MethodPost, path+"/"+name, ifMatch, "", "")
}

func TestPatchVenue_ChangesOnlyTheSentFields(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	stored := insertVenue(t, pool, fullVenueParams())
	path := venuesPath + "/" + stored.ID.String()

	// Objects are merged: only the city and the region of the address change, only the
	// latitude of the location. Fields left out keep their values; null clears one.
	rec := patch(t, router, path, `"1"`, `{
		"name": "Volkswagen Arena Istanbul",
		"description": null,
		"address": {"city": "Ankara", "region": null},
		"location": {"latitude": 39.92}
	}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d; body %s", rec.Code, http.StatusOK, rec.Body)
	}
	var body struct {
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	checkJSON(t, rec.Body.String(), fmt.Sprintf(`{
		"id": %q,
		"name": "Volkswagen Arena Istanbul",
		"description": null,
		"address": {
			"line1": "Huzur Mah. Azerbaycan Cad. 1",
			"line2": "Gate B",
			"city": "Ankara",
			"region": null,
			"postal_code": "34396",
			"country_code": "TR"
		},
		"location": {"latitude": 39.92, "longitude": 29.010547},
		"time_zone": "Europe/Istanbul",
		"status": "active",
		"version": 2,
		"created_at": %q,
		"updated_at": %q
	}`, stored.ID, timeJSON(stored.CreatedAt), timeJSON(body.UpdatedAt)))
	checkETag(t, rec, `"2"`) // the new version, for the next change
	if !body.UpdatedAt.After(stored.UpdatedAt) {
		t.Errorf("updated_at %v is not after %v", body.UpdatedAt, stored.UpdatedAt)
	}

	// The response is what was stored.
	got := get(t, router, path)
	checkJSON(t, got.Body.String(), rec.Body.String())
	checkETag(t, got, `"2"`)
}

func TestPatchVenue_AcceptsMergePatchAndPlainJSON(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := venuesPath + "/" + insertVenue(t, pool, fullVenueParams()).ID.String()

	for i, contentType := range []string{mergePatchContentType, "application/json; charset=utf-8"} {
		ifMatch := fmt.Sprintf(`"%d"`, i+1) // each change raises the version
		rec := sendIfMatch(t, router, http.MethodPatch, path, ifMatch, contentType, `{"name": "Arena"}`)
		if rec.Code != http.StatusOK {
			t.Errorf("Content-Type %s: status %d, want 200; body %s", contentType, rec.Code, rec.Body)
		}
	}

	rec := sendIfMatch(t, router, http.MethodPatch, path, `"3"`, "text/plain", `{"name": "Arena"}`)
	checkProblem(t, rec, http.StatusUnsupportedMediaType, `{
		"type": "about:blank",
		"title": "Unsupported Media Type",
		"status": 415,
		"detail": "the request body must be JSON, sent with \"Content-Type: application/merge-patch+json\" or \"Content-Type: application/json\"",
		"instance": "`+path+`"
	}`)
}

func TestPatchVenue_InvalidResultIs400AndStoresNothing(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := venuesPath + "/" + insertVenue(t, pool, fullVenueParams()).ID.String()
	before := get(t, router, path).Body.String()

	// The patched venue is checked with the same rules as a new one: required fields set to
	// null are missing, and the address set to null leaves all its required fields empty.
	rec := patch(t, router, path, `"1"`, `{
		"name": null,
		"address": null,
		"time_zone": "Mars/Olympus_Mons"
	}`)

	checkProblem(t, rec, http.StatusBadRequest, `{
		"type": "about:blank",
		"title": "Bad Request",
		"status": 400,
		"detail": "the request has invalid fields; see errors",
		"instance": "`+path+`",
		"errors": [
			{"field": "name", "message": "is required"},
			{"field": "address.line1", "message": "is required"},
			{"field": "address.city", "message": "is required"},
			{"field": "address.country_code", "message": "must be an ISO 3166-1 alpha-2 country code: two capital letters, e.g. TR"},
			{"field": "time_zone", "message": "must be an IANA time zone name, e.g. Europe/Istanbul"}
		]
	}`)
	checkJSON(t, get(t, router, path).Body.String(), before) // unchanged, version included
}

func TestPatchVenue_LocationEndsWithBothCoordinatesOrNeither(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	withLocation := fullVenueParams()
	withoutLocation := fullVenueParams()
	withoutLocation.Latitude, withoutLocation.Longitude = nil, nil

	tests := []struct {
		name     string
		stored   eventdb.CreateVenueParams
		patch    string
		location string // the location afterwards, for a 200
		missing  string // the field reported as missing, for a 400
	}{
		{"set both", withoutLocation, `{"location": {"latitude": 1.5, "longitude": 2.5}}`,
			`{"latitude": 1.5, "longitude": 2.5}`, ""},
		{"change one, keep the other", withLocation, `{"location": {"longitude": 30}}`,
			`{"latitude": 41.108934, "longitude": 30}`, ""},
		{"clear it", withLocation, `{"location": null}`, `null`, ""},
		{"clear both coordinates", withLocation, `{"location": {"latitude": null, "longitude": null}}`, `null`, ""},
		{"set only one", withoutLocation, `{"location": {"latitude": 1.5}}`, "", "location.longitude"},
		{"clear only one", withLocation, `{"location": {"latitude": null}}`, "", "location.latitude"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := venuesPath + "/" + insertVenue(t, pool, tt.stored).ID.String()

			rec := patch(t, router, path, `"1"`, tt.patch)

			if tt.missing != "" {
				checkProblem(t, rec, http.StatusBadRequest, `{
					"type": "about:blank",
					"title": "Bad Request",
					"status": 400,
					"detail": "the request has invalid fields; see errors",
					"instance": "`+path+`",
					"errors": [{"field": "`+tt.missing+`", "message": "is required"}]
				}`)
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200; body %s", rec.Code, rec.Body)
			}
			var body struct {
				Location json.RawMessage `json:"location"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			checkJSON(t, string(body.Location), tt.location)
		})
	}
}

func TestPatchVenue_UnusableBodies(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := venuesPath + "/" + insertVenue(t, pool, fullVenueParams()).ID.String()

	tests := []struct {
		name   string
		body   string
		detail string
	}{
		{"empty body", "", "the request body is empty"},
		{"array instead of object", `[]`, "the request body must be a JSON object"},
		{"unknown field", `{"title": "Arena"}`, `unknown field "title"`},
		{"unknown field in a nested object", `{"address": {"cty": "Ankara"}}`, `unknown field "cty"`},
		// The version goes in If-Match, the status through archive and restore.
		{"version in the body", `{"version": 2}`, `unknown field "version"`},
		{"status in the body", `{"status": "archived"}`, `unknown field "status"`},
		{"number for a string", `{"name": 7}`, "name must be a string"},
		{"wrong type in a nested field", `{"address": {"city": 7}}`, "address.city must be a string"},
		{"number instead of an object", `{"location": 5}`, "location must be an object"},
		{"string for a coordinate", `{"location": {"latitude": "north"}}`, "location.latitude must be a number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := patch(t, router, path, `"1"`, tt.body)

			want, err := json.Marshal(problem{
				Type:     "about:blank",
				Title:    "Bad Request",
				Status:   http.StatusBadRequest,
				Detail:   tt.detail,
				Instance: path,
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			checkProblem(t, rec, http.StatusBadRequest, string(want))
		})
	}
}

// changeRequest is one of the four requests that change a venue, sent for the version in
// ifMatch to the venue at path.
type changeRequest struct {
	name   string
	suffix string // added to the venue's path: "" or the action's "/archive", "/restore"
	send   func(t *testing.T, router http.Handler, path, ifMatch string) *httptest.ResponseRecorder
}

var changeRequests = []changeRequest{
	{"PATCH", "", func(t *testing.T, router http.Handler, path, ifMatch string) *httptest.ResponseRecorder {
		return patch(t, router, path, ifMatch, `{"name": "Arena"}`)
	}},
	{"DELETE", "", func(t *testing.T, router http.Handler, path, ifMatch string) *httptest.ResponseRecorder {
		return sendIfMatch(t, router, http.MethodDelete, path, ifMatch, "", "")
	}},
	{"archive", "/archive", func(t *testing.T, router http.Handler, path, ifMatch string) *httptest.ResponseRecorder {
		return action(t, router, path, "archive", ifMatch)
	}},
	{"restore", "/restore", func(t *testing.T, router http.Handler, path, ifMatch string) *httptest.ResponseRecorder {
		return action(t, router, path, "restore", ifMatch)
	}},
}

func TestChangeVenue_WithoutIfMatchIs428AndChangesNothing(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := venuesPath + "/" + insertVenue(t, pool, fullVenueParams()).ID.String()
	before := get(t, router, path).Body.String()

	for _, req := range changeRequests {
		for name, ifMatch := range map[string]string{"no If-Match": "", "If-Match: *": "*"} {
			t.Run(req.name+", "+name, func(t *testing.T) {
				rec := req.send(t, router, path, ifMatch)

				checkProblem(t, rec, http.StatusPreconditionRequired, `{
					"type": "about:blank",
					"title": "Precondition Required",
					"status": 428,
					"detail": "this request must have an If-Match header with the ETag of your last read, e.g. If-Match: \"3\"; GET the resource to get it",
					"instance": "`+path+req.suffix+`"
				}`)
			})
		}
	}
	checkJSON(t, get(t, router, path).Body.String(), before)
}

func TestChangeVenue_MalformedIfMatchIs400(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := venuesPath + "/" + insertVenue(t, pool, fullVenueParams()).ID.String()

	for name, ifMatch := range map[string]string{
		"not quoted":       `1`,
		"weak ETag":        `W/"1"`,
		"two ETags":        `"1", "2"`,
		"not a version":    `"abc"`,
		"empty ETag":       `""`,
		"negative version": `"-1"`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := patch(t, router, path, ifMatch, `{"name": "Arena"}`)

			checkProblem(t, rec, http.StatusBadRequest, `{
				"type": "about:blank",
				"title": "Bad Request",
				"status": 400,
				"detail": "the If-Match header must be one ETag exactly as this API sent it, e.g. \"3\"",
				"instance": "`+path+`"
			}`)
		})
	}
}

// An admin who read version 1 can't change, archive or delete the venue after someone else
// changed it to version 2: the answer is 412, and nothing changes.
func TestChangeVenue_StaleIfMatchIs412AndChangesNothing(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := venuesPath + "/" + insertVenue(t, pool, fullVenueParams()).ID.String()
	if rec := patch(t, router, path, `"1"`, `{"name": "Arena Istanbul"}`); rec.Code != http.StatusOK {
		t.Fatalf("first PATCH: status %d, body %s", rec.Code, rec.Body)
	}
	before := get(t, router, path).Body.String() // version 2

	for _, req := range changeRequests {
		// A version that was never issued, even one too large for any number type, is stale too.
		for _, ifMatch := range []string{`"1"`, `"3"`, `"99999999999999999999"`} {
			t.Run(req.name+" "+ifMatch, func(t *testing.T) {
				rec := req.send(t, router, path, ifMatch)

				checkProblem(t, rec, http.StatusPreconditionFailed, `{
					"type": "about:blank",
					"title": "Precondition Failed",
					"status": 412,
					"detail": "it has changed since you read it: GET it again, then repeat your request with its new ETag in If-Match",
					"instance": "`+path+req.suffix+`"
				}`)
			})
		}
	}
	checkJSON(t, get(t, router, path).Body.String(), before)
}

func TestArchiveAndRestoreVenue(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	path := venuesPath + "/" + insertVenue(t, pool, fullVenueParams()).ID.String()

	// statusOf reads the status and version from a response body.
	statusOf := func(rec *httptest.ResponseRecorder) (string, int) {
		t.Helper()
		var body struct {
			Status  string `json:"status"`
			Version int    `json:"version"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		return body.Status, body.Version
	}

	archived := action(t, router, path, "archive", `"1"`)
	if archived.Code != http.StatusOK {
		t.Fatalf("archive: status %d, body %s", archived.Code, archived.Body)
	}
	if status, version := statusOf(archived); status != "archived" || version != 2 {
		t.Errorf("after archive: status %q, version %d; want archived, 2", status, version)
	}
	checkETag(t, archived, `"2"`)
	checkJSON(t, get(t, router, path).Body.String(), archived.Body.String()) // stored as answered

	// An archived venue is closed to changes, and archiving it again is a conflict too.
	checkProblem(t, patch(t, router, path, `"2"`, `{"name": "Arena"}`), http.StatusConflict, `{
		"type": "about:blank",
		"title": "Conflict",
		"status": 409,
		"detail": "venue is archived: restore it first",
		"instance": "`+path+`"
	}`)
	checkProblem(t, action(t, router, path, "archive", `"2"`), http.StatusConflict, `{
		"type": "about:blank",
		"title": "Conflict",
		"status": 409,
		"detail": "venue is archived: restore it first",
		"instance": "`+path+`/archive"
	}`)

	restored := action(t, router, path, "restore", `"2"`)
	if restored.Code != http.StatusOK {
		t.Fatalf("restore: status %d, body %s", restored.Code, restored.Body)
	}
	if status, version := statusOf(restored); status != "active" || version != 3 {
		t.Errorf("after restore: status %q, version %d; want active, 3", status, version)
	}
	checkETag(t, restored, `"3"`)

	checkProblem(t, action(t, router, path, "restore", `"3"`), http.StatusConflict, `{
		"type": "about:blank",
		"title": "Conflict",
		"status": 409,
		"detail": "venue is not archived",
		"instance": "`+path+`/restore"
	}`)
}

func TestChangeVenue_UnknownOrMalformedIDIs404(t *testing.T) {
	router, _, _ := newVenueRouter(t)

	for name, id := range map[string]string{
		"no venue has this id": uuid.New().String(),
		"not a UUID":           "not-a-uuid",
	} {
		for _, req := range changeRequests {
			t.Run(req.name+" "+name, func(t *testing.T) {
				path := venuesPath + "/" + id
				rec := req.send(t, router, path, `"1"`)

				checkProblem(t, rec, http.StatusNotFound, `{
					"type": "about:blank",
					"title": "Not Found",
					"status": 404,
					"detail": "venue not found",
					"instance": "`+path+req.suffix+`"
				}`)
			})
		}
	}
}

func TestDeleteVenue_Returns204AndTheVenueIsGone(t *testing.T) {
	router, pool, _ := newVenueRouter(t)
	active := venuesPath + "/" + insertVenue(t, pool, fullVenueParams()).ID.String()
	archived := venuesPath + "/" + insertVenue(t, pool, fullVenueParams()).ID.String()
	if rec := action(t, router, archived, "archive", `"1"`); rec.Code != http.StatusOK {
		t.Fatalf("archive: status %d, body %s", rec.Code, rec.Body)
	}

	for path, ifMatch := range map[string]string{active: `"1"`, archived: `"2"`} {
		rec := sendIfMatch(t, router, http.MethodDelete, path, ifMatch, "", "")

		if rec.Code != http.StatusNoContent {
			t.Fatalf("status: got %d, want %d; body %s", rec.Code, http.StatusNoContent, rec.Body)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("body: got %q, want none", rec.Body)
		}
		if got := get(t, router, path); got.Code != http.StatusNotFound {
			t.Errorf("GET after DELETE: status %d, want 404", got.Code)
		}
		again := sendIfMatch(t, router, http.MethodDelete, path, ifMatch, "", "")
		if again.Code != http.StatusNotFound {
			t.Errorf("second DELETE: status %d, want 404", again.Code)
		}
	}
}

func TestChangeVenue_DatabaseFailureIs500WithoutDetails(t *testing.T) {
	router, pool, logs := newVenueRouter(t)
	pool.Close() // every query now fails; Close may be called again by the cleanup
	path := venuesPath + "/" + uuid.New().String()

	for _, req := range changeRequests {
		rec := req.send(t, router, path, `"1"`)

		checkProblem(t, rec, http.StatusInternalServerError, `{
			"type": "about:blank",
			"title": "Internal Server Error",
			"status": 500,
			"instance": "`+path+req.suffix+`"
		}`)
	}
	// PATCH fails when it starts its transaction; the others in their query.
	for _, cause := range []string{"begin transaction", "delete venue", "set venue"} {
		if !strings.Contains(logs.String(), cause) {
			t.Errorf("the log doesn't contain %q:\n%s", cause, logs.String())
		}
	}
}
