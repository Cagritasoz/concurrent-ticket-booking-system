package telemetry

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewLogger_JSONFormat(t *testing.T) {
	// bytes.Buffer is an in-memory io.Writer, like a ByteArrayOutputStream in Java.
	// The logger writes into it instead of the terminal, so the test can read the output.
	var buf bytes.Buffer
	logger, err := NewLogger("info", "json", &buf)
	if err != nil {
		t.Fatalf("NewLogger: unexpected error: %v", err)
	}

	logger.Info("seat booked", "seat_id", "A1", "attempt", 3)

	// Every log line must be one valid JSON object.
	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, buf.String())
	}

	want := map[string]any{
		"level":   "INFO",
		"msg":     "seat booked",
		"seat_id": "A1",
		"attempt": float64(3), // encoding/json decodes every JSON number into float64.
	}
	for key, wantValue := range want {
		if entry[key] != wantValue {
			t.Errorf("field %q: got %v, want %v", key, entry[key], wantValue)
		}
	}
	if _, ok := entry["time"]; !ok {
		t.Errorf("missing field %q in %s", "time", buf.String())
	}
}

func TestNewLogger_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	logger, err := NewLogger("info", "text", &buf)
	if err != nil {
		t.Fatalf("NewLogger: unexpected error: %v", err)
	}

	logger.Info("seat booked", "seat_id", "A1")

	out := buf.String()
	for _, want := range []string{"level=INFO", `msg="seat booked"`, "seat_id=A1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}
}

// Table-driven: for each configured level, check which messages get through.
func TestNewLogger_LevelFiltering(t *testing.T) {
	tests := []struct {
		level string
		want  []string // messages expected in the output, in order
	}{
		{"debug", []string{"debug-msg", "info-msg", "warn-msg", "error-msg"}},
		{"info", []string{"info-msg", "warn-msg", "error-msg"}},
		{"warn", []string{"warn-msg", "error-msg"}},
		{"error", []string{"error-msg"}},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			var buf bytes.Buffer
			logger, err := NewLogger(tt.level, "json", &buf)
			if err != nil {
				t.Fatalf("NewLogger: unexpected error: %v", err)
			}

			logger.Debug("debug-msg")
			logger.Info("info-msg")
			logger.Warn("warn-msg")
			logger.Error("error-msg")

			got := messages(t, buf.String())
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("level %s: got messages %v, want %v", tt.level, got, tt.want)
			}
		})
	}
}

func TestNewLogger_Invalid(t *testing.T) {
	tests := []struct {
		name          string
		level, format string
	}{
		{"unknown level", "verbose", "json"},
		{"unknown format", "info", "xml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, err := NewLogger(tt.level, tt.format, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if logger != nil {
				t.Errorf("expected a nil logger on error, got %v", logger)
			}
		})
	}
}

// messages returns the "msg" field of every JSON log line in out.
// t.Helper marks this as a helper, so a failure is reported at the caller's line.
func messages(t *testing.T, out string) []string {
	t.Helper()

	var msgs []string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var entry struct {
			Msg string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("line is not valid JSON: %v\nline: %s", err, line)
		}
		msgs = append(msgs, entry.Msg)
	}
	return msgs
}
