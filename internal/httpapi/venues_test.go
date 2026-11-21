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

// post sends a POST request with this body and Content-Type to handler, in memory.
func post(t *testing.T, handler http.Handler, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
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
		"time_zone": "Europe/Istanbul",
		"capacity": 5000
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
		"capacity": 5000,
		"created_at": %q,
		"updated_at": %q
	}`, created.ID, created.CreatedAt, created.UpdatedAt))
	if _, err := uuid.Parse(created.ID); err != nil {
		t.Errorf("id %q is not a UUID", created.ID)
	}

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
		"time_zone": "Mars/Olympus_Mons",
		"capacity": 0
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
			{"field": "time_zone", "message": "must be an IANA time zone name, e.g. Europe/Istanbul"},
			{"field": "capacity", "message": "must be between 1 and 2147483647"}
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
		{"number as string", "application/json", `{"capacity": "5000"}`, http.StatusBadRequest, "capacity must be a whole number"},
		{"fraction for a whole number", "application/json", `{"capacity": 50.5}`, http.StatusBadRequest, "capacity must be a whole number"},
		{"wrong type in a nested field", "application/json", `{"address": {"city": 7}}`, http.StatusBadRequest,
			"address.city must be a string"},
		{"unknown field", "application/json", `{"capcity": 5000}`, http.StatusBadRequest, `unknown field "capcity"`},
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
			"capacity": null,
			"created_at": %q,
			"updated_at": %q
		}],
		"next_cursor": null
	}`, stored.ID, timeJSON(stored.CreatedAt), timeJSON(stored.UpdatedAt)))
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
