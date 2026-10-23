package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
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
