package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"uuid"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/telemetry"
)

// getWithHeader sends a GET request with an optional X-Request-ID header and returns the response
// plus everything the router logged (as JSON lines).
func getWithHeader(t *testing.T, path, requestIDValue string) (*httptest.ResponseRecorder, string) {
	t.Helper()

	var logs bytes.Buffer
	logger, err := telemetry.NewLogger("debug", "json", &logs)
	if err != nil {
		t.Fatalf("NewLogger: unexpected error: %v", err)
	}
	router := NewRouter(logger, &Readiness{})

	req := httptest.NewRequest(http.MethodGet, path, nil)
	if requestIDValue != "" {
		req.Header.Set(requestIDHeader, requestIDValue)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec, logs.String()
}

// loggedRequestID returns the request_id of the "http request" log line.
func loggedRequestID(t *testing.T, logs string) string {
	t.Helper()
	for line := range strings.SplitSeq(strings.TrimSpace(logs), "\n") {
		var entry struct {
			Msg       string `json:"msg"`
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not valid JSON: %v\nline: %s", err, line)
		}
		if entry.Msg == "http request" {
			return entry.RequestID
		}
	}
	t.Fatalf("no \"http request\" log line in:\n%s", logs)
	return ""
}

func TestRequestID_GeneratedWhenMissing(t *testing.T) {
	rec, logs := getWithHeader(t, "/healthz", "")

	id := rec.Header().Get(requestIDHeader)
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("response %s header %q is not a UUID: %v", requestIDHeader, id, err)
	}
	if got := loggedRequestID(t, logs); got != id {
		t.Errorf("logged request_id %q, want the response header value %q", got, id)
	}
}

func TestRequestID_ReusesValidIncomingID(t *testing.T) {
	rec, logs := getWithHeader(t, "/healthz", "abc-123_X.9")

	if got := rec.Header().Get(requestIDHeader); got != "abc-123_X.9" {
		t.Errorf("response header: got %q, want the incoming ID", got)
	}
	if got := loggedRequestID(t, logs); got != "abc-123_X.9" {
		t.Errorf("logged request_id: got %q, want the incoming ID", got)
	}
}

func TestRequestID_ReplacesInvalidIncomingID(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{"contains a space", "hello world"},
		{"contains a quote", `abc"def`},
		{"too long", strings.Repeat("a", maxRequestIDLength+1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, logs := getWithHeader(t, "/healthz", tt.id)

			id := rec.Header().Get(requestIDHeader)
			if id == tt.id {
				t.Fatalf("invalid incoming ID %q was reused", tt.id)
			}
			if _, err := uuid.Parse(id); err != nil {
				t.Errorf("replacement %q is not a UUID: %v", id, err)
			}
			if got := loggedRequestID(t, logs); got != id {
				t.Errorf("logged request_id %q, want %q", got, id)
			}
		})
	}
}
