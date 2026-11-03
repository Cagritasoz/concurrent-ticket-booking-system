package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/telemetry"
)

// newTestRouter builds a router whose logs are thrown away (io.Discard), so test output stays clean.
func newTestRouter(readiness *Readiness) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRouter(logger, readiness)
}

// get sends a GET request to handler in memory, without opening a real port, and returns the
// recorded response. httptest.NewRecorder plays the role of MockMvc's result in Spring.
func get(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHealthz_ReturnsOK(t *testing.T) {
	rec := get(t, newTestRouter(&Readiness{}), "/healthz")

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if want := `{"status":"ok"}`; rec.Body.String() != want {
		t.Errorf("body: got %s, want %s", rec.Body.String(), want)
	}
}

func TestReadyz_FollowsReadiness(t *testing.T) {
	readiness := &Readiness{} // starts not ready
	router := newTestRouter(readiness)

	if rec := get(t, router, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("before ready: got %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	readiness.SetReady(true)
	if rec := get(t, router, "/readyz"); rec.Code != http.StatusOK {
		t.Errorf("after SetReady(true): got %d, want %d", rec.Code, http.StatusOK)
	}

	readiness.SetReady(false)
	if rec := get(t, router, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("after SetReady(false): got %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// newLoggingRouter builds a router whose logs are written into logs as JSON lines, so a test can
// check what was logged.
func newLoggingRouter(t *testing.T, logs *bytes.Buffer) *gin.Engine {
	t.Helper()
	logger, err := telemetry.NewLogger("debug", "json", logs)
	if err != nil {
		t.Fatalf("NewLogger: unexpected error: %v", err)
	}
	return NewRouter(logger, &Readiness{})
}

// logEntry is one parsed JSON log line; only the fields the tests check are listed.
type logEntry struct {
	Level     string `json:"level"`
	Msg       string `json:"msg"`
	Status    int    `json:"status"`
	Panic     string `json:"panic"`
	Stack     string `json:"stack"`
	RequestID string `json:"request_id"`
}

// findLog returns the first log line with the given message.
func findLog(t *testing.T, logs, msg string) logEntry {
	t.Helper()
	for line := range strings.SplitSeq(strings.TrimSpace(logs), "\n") {
		var entry logEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not valid JSON: %v\nline: %s", err, line)
		}
		if entry.Msg == msg {
			return entry
		}
	}
	t.Fatalf("no %q log line in:\n%s", msg, logs)
	return logEntry{}
}

func TestUnknownRoute_Returns404(t *testing.T) {
	var logs bytes.Buffer
	rec := get(t, newLoggingRouter(t, &logs), "/does-not-exist")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusNotFound)
	}
	// The middleware runs for unknown routes too: the response has a request ID and the request
	// is logged as a warning.
	if rec.Header().Get(requestIDHeader) == "" {
		t.Errorf("response has no %s header", requestIDHeader)
	}
	entry := findLog(t, logs.String(), "http request")
	if entry.Level != "WARN" || entry.Status != http.StatusNotFound {
		t.Errorf("request log: got level %s status %d, want WARN 404", entry.Level, entry.Status)
	}
}

func TestPanicInHandler_Returns500(t *testing.T) {
	var logs bytes.Buffer
	router := newLoggingRouter(t, &logs)
	// A route that exists only in this test, to prove the recovery middleware catches panics.
	router.GET("/panic", func(c *gin.Context) {
		panic("something broke")
	})

	rec := get(t, router, "/panic")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	// The client gets a generic message; the panic details only go to the logs.
	if want := `{"error":"internal server error"}`; rec.Body.String() != want {
		t.Errorf("body: got %s, want %s", rec.Body.String(), want)
	}
	panicLog := findLog(t, logs.String(), "panic recovered")
	if panicLog.Level != "ERROR" || panicLog.Panic != "something broke" || panicLog.Stack == "" {
		t.Errorf("panic log: got level %s, panic %q, stack empty: %t; want ERROR, the panic value and a stack",
			panicLog.Level, panicLog.Panic, panicLog.Stack == "")
	}
	requestLog := findLog(t, logs.String(), "http request")
	if requestLog.Level != "ERROR" || requestLog.Status != http.StatusInternalServerError {
		t.Errorf("request log: got level %s status %d, want ERROR 500", requestLog.Level, requestLog.Status)
	}
	// Both lines belong to the same request.
	if panicLog.RequestID == "" || panicLog.RequestID != requestLog.RequestID {
		t.Errorf("request IDs differ: panic log %q, request log %q", panicLog.RequestID, requestLog.RequestID)
	}

	// The server keeps working after a panic.
	if rec := get(t, router, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("after the panic, /healthz: got %d, want %d", rec.Code, http.StatusOK)
	}
}
