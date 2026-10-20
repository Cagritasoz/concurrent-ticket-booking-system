// Package telemetry sets up the application's observability tools. For now that is the logger;
// Prometheus metrics are added in M9.
//
// It only builds things; it contains no business rules.
package telemetry

import (
	"fmt"
	"io"
	"log/slog"
)

// NewLogger builds a structured logger that writes to w.
//
//   - level:  "debug", "info", "warn" or "error". Messages below this level are dropped.
//   - format: "json" (one JSON object per line, for production and log tools) or
//     "text" (key=value pairs, easier to read in a terminal).
//
// The returned logger is passed to the parts of the app that need it through their constructors.
// We don't call slog.SetDefault, so there is no global logger (see the conventions in CLAUDE.md).
func NewLogger(level, format string, w io.Writer) (*slog.Logger, error) {
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}

	// HandlerOptions configures the handler. Level is the minimum level that gets written.
	opts := &slog.HandlerOptions{Level: lvl}

	// A handler decides how a log record is formatted and where it goes, similar to a
	// Logback appender + encoder. The *slog.Logger in front of it is what code calls.
	var handler slog.Handler
	switch format {
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	case "text":
		handler = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("unknown log format %q (want json or text)", format)
	}

	return slog.New(handler), nil
}

// parseLevel turns a level name into a slog.Level.
func parseLevel(level string) (slog.Level, error) {
	switch level {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", level)
	}
}
