package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

// newJSONLogger returns an info-level JSON logger and the buffer it writes into.
func newJSONLogger(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger, err := NewLogger("info", "json", &buf)
	if err != nil {
		t.Fatalf("NewLogger: unexpected error: %v", err)
	}
	return logger, &buf
}

// decodeLine parses a single JSON log line into a map of its fields.
func decodeLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not one valid JSON line: %v\noutput: %s", err, buf.String())
	}
	return entry
}

func TestLogger_AddsRequestIDFromContext(t *testing.T) {
	logger, buf := newJSONLogger(t)
	ctx := ContextWithRequestID(context.Background(), "req-123")

	logger.InfoContext(ctx, "hello")

	if got := decodeLine(t, buf)["request_id"]; got != "req-123" {
		t.Errorf("request_id: got %v, want %q", got, "req-123")
	}
}

func TestLogger_NoRequestIDWithoutContextValue(t *testing.T) {
	logger, buf := newJSONLogger(t)

	logger.InfoContext(context.Background(), "context without an ID")
	logger.Info("no context at all")

	if bytes.Contains(buf.Bytes(), []byte("request_id")) {
		t.Errorf("expected no request_id field, got: %s", buf.String())
	}
}

func TestLogger_With_KeepsRequestID(t *testing.T) {
	logger, buf := newJSONLogger(t)
	ctx := ContextWithRequestID(context.Background(), "req-456")

	// logger.With returns a new logger with extra fields; it must still add the request ID.
	logger.With("module", "booking").InfoContext(ctx, "hello")

	entry := decodeLine(t, buf)
	if entry["request_id"] != "req-456" || entry["module"] != "booking" {
		t.Errorf("got request_id=%v module=%v, want req-456 and booking", entry["request_id"], entry["module"])
	}
}
