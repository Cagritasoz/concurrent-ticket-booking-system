package config

import (
	"strings"
	"testing"
	"time"
)

// fakeEnv returns a LookupFunc that reads from a map instead of the real environment,
// so tests don't depend on (or change) the variables of the machine running them.
func fakeEnv(vars map[string]string) LookupFunc {
	return func(key string) (string, bool) {
		v, ok := vars[key]
		return v, ok
	}
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load(fakeEnv(nil))
	if err != nil {
		t.Fatalf("Load with no variables set: unexpected error: %v", err)
	}

	want := Config{
		HTTP: HTTPConfig{
			Addr:              ":8080",
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		Log:             LogConfig{Level: "info", Format: "json"},
		ShutdownTimeout: 20 * time.Second,
	}
	// Structs made of comparable fields can be compared with ==, like a record's equals() in Java.
	if cfg != want {
		t.Errorf("defaults:\n got  %+v\n want %+v", cfg, want)
	}
}

func TestLoad_Overrides(t *testing.T) {
	cfg, err := Load(fakeEnv(map[string]string{
		"HTTP_ADDR":                "127.0.0.1:9090",
		"HTTP_READ_HEADER_TIMEOUT": "2s",
		"HTTP_READ_TIMEOUT":        "10s",
		"HTTP_WRITE_TIMEOUT":       "30s",
		"HTTP_IDLE_TIMEOUT":        "2m",
		"SHUTDOWN_TIMEOUT":         "500ms",
		"LOG_LEVEL":                "debug",
		"LOG_FORMAT":               "text",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := Config{
		HTTP: HTTPConfig{
			Addr:              "127.0.0.1:9090",
			ReadHeaderTimeout: 2 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       2 * time.Minute,
		},
		Log:             LogConfig{Level: "debug", Format: "text"},
		ShutdownTimeout: 500 * time.Millisecond,
	}
	if cfg != want {
		t.Errorf("overrides:\n got  %+v\n want %+v", cfg, want)
	}
}

func TestLoad_EmptyValueUsesDefault(t *testing.T) {
	cfg, err := Load(fakeEnv(map[string]string{"HTTP_ADDR": "", "HTTP_READ_TIMEOUT": ""}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTP.Addr != ":8080" {
		t.Errorf("HTTP_ADDR: got %q, want default %q", cfg.HTTP.Addr, ":8080")
	}
	if cfg.HTTP.ReadTimeout != 15*time.Second {
		t.Errorf("HTTP_READ_TIMEOUT: got %s, want default 15s", cfg.HTTP.ReadTimeout)
	}
}

// Table-driven test: each row is one case, like @ParameterizedTest with a @MethodSource in JUnit.
func TestLoad_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		vars    map[string]string
		wantErr string // a part of the error message we expect
	}{
		{"duration without unit", map[string]string{"HTTP_READ_TIMEOUT": "15"}, "HTTP_READ_TIMEOUT"},
		{"duration not a number", map[string]string{"SHUTDOWN_TIMEOUT": "soon"}, "SHUTDOWN_TIMEOUT"},
		{"zero timeout", map[string]string{"HTTP_WRITE_TIMEOUT": "0s"}, "HTTP_WRITE_TIMEOUT must be greater than zero"},
		{"negative timeout", map[string]string{"HTTP_IDLE_TIMEOUT": "-1s"}, "HTTP_IDLE_TIMEOUT must be greater than zero"},
		{"address without port", map[string]string{"HTTP_ADDR": "localhost"}, "HTTP_ADDR"},
		{"unknown log level", map[string]string{"LOG_LEVEL": "verbose"}, "LOG_LEVEL"},
		{"log level is case sensitive", map[string]string{"LOG_LEVEL": "INFO"}, "LOG_LEVEL"},
		{"unknown log format", map[string]string{"LOG_FORMAT": "xml"}, "LOG_FORMAT"},
	}

	for _, tt := range tests {
		// t.Run runs each case as a named subtest, so a failure shows which row broke.
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(fakeEnv(tt.vars))
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_ReportsAllErrorsAtOnce(t *testing.T) {
	_, err := Load(fakeEnv(map[string]string{
		"HTTP_READ_TIMEOUT": "abc",
		"LOG_LEVEL":         "loud",
		"LOG_FORMAT":        "xml",
	}))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	for _, want := range []string{"HTTP_READ_TIMEOUT", "LOG_LEVEL", "LOG_FORMAT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
