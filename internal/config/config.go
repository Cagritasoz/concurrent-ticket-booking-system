// Package config reads the application settings from environment variables into a typed Config
// and validates them.
//
// It plays the role of application.properties + @ConfigurationProperties + @Validated in Spring
// Boot. Every variable has a default, so the app starts without any environment set.
// The full list of variables is documented in docs/env.md and .env.example.
package config

import (
	"errors"
	"fmt"
	"net"
	"time"
)

// Config holds all settings of the application.
type Config struct {
	HTTP            HTTPConfig
	Log             LogConfig
	ShutdownTimeout time.Duration // SHUTDOWN_TIMEOUT: maximum time for graceful shutdown.
}

// HTTPConfig holds the HTTP server settings.
type HTTPConfig struct {
	Addr              string        // HTTP_ADDR: address the server listens on, e.g. ":8080".
	ReadHeaderTimeout time.Duration // HTTP_READ_HEADER_TIMEOUT: time allowed to read the request headers.
	ReadTimeout       time.Duration // HTTP_READ_TIMEOUT: time allowed to read the whole request.
	WriteTimeout      time.Duration // HTTP_WRITE_TIMEOUT: time allowed to write the response.
	IdleTimeout       time.Duration // HTTP_IDLE_TIMEOUT: how long an idle keep-alive connection stays open.
}

// LogConfig holds the logging settings.
type LogConfig struct {
	Level  string // LOG_LEVEL: debug, info, warn or error.
	Format string // LOG_FORMAT: json or text.
}

// Default values, used when a variable is not set or is empty.
const (
	defaultHTTPAddr          = ":8080"
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 15 * time.Second
	defaultWriteTimeout      = 15 * time.Second
	defaultIdleTimeout       = 60 * time.Second
	defaultShutdownTimeout   = 20 * time.Second
	defaultLogLevel          = "info"
	defaultLogFormat         = "json"
)

// LookupFunc looks up an environment variable. It has the same signature as os.LookupEnv,
// which is what main passes in. Tests pass a fake that reads from a map instead.
type LookupFunc func(key string) (string, bool)

// Load reads the configuration using lookup, applies defaults and validates the result.
// It reports all problems at once instead of stopping at the first one.
func Load(lookup LookupFunc) (Config, error) {
	r := reader{lookup: lookup}

	cfg := Config{
		HTTP: HTTPConfig{
			Addr:              r.string("HTTP_ADDR", defaultHTTPAddr),
			ReadHeaderTimeout: r.duration("HTTP_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout),
			ReadTimeout:       r.duration("HTTP_READ_TIMEOUT", defaultReadTimeout),
			WriteTimeout:      r.duration("HTTP_WRITE_TIMEOUT", defaultWriteTimeout),
			IdleTimeout:       r.duration("HTTP_IDLE_TIMEOUT", defaultIdleTimeout),
		},
		Log: LogConfig{
			Level:  r.string("LOG_LEVEL", defaultLogLevel),
			Format: r.string("LOG_FORMAT", defaultLogFormat),
		},
		ShutdownTimeout: r.duration("SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
	}

	// errors.Join combines several errors into one (nil if all of them are nil meaning everything is valid).
	// r.errs... to expand the error slice into individual arguments.
	if err := errors.Join(errors.Join(r.errs...), cfg.Validate()); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

// Validate checks that the values make sense and returns all problems joined into one error,
// or nil if everything is valid.
func (c Config) Validate() error {
	var errs []error

	if _, _, err := net.SplitHostPort(c.HTTP.Addr); err != nil {
		errs = append(errs, fmt.Errorf("HTTP_ADDR %q must look like host:port or :port", c.HTTP.Addr))
	}

	// Define anonymous struct type with two fields and initialize the slice.
	positive := []struct {
		name  string
		value time.Duration
	}{
		{"HTTP_READ_HEADER_TIMEOUT", c.HTTP.ReadHeaderTimeout},
		{"HTTP_READ_TIMEOUT", c.HTTP.ReadTimeout},
		{"HTTP_WRITE_TIMEOUT", c.HTTP.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT", c.HTTP.IdleTimeout},
		{"SHUTDOWN_TIMEOUT", c.ShutdownTimeout},
	}
	for _, p := range positive {
		if p.value <= 0 {
			errs = append(errs, fmt.Errorf("%s must be greater than zero, got %s", p.name, p.value))
		}
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("LOG_LEVEL %q must be one of debug, info, warn, error", c.Log.Level))
	}

	switch c.Log.Format {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("LOG_FORMAT %q must be one of json, text", c.Log.Format))
	}

	return errors.Join(errs...)
}

// reader reads variables and collects parse errors, so Load can report all of them together.
type reader struct {
	lookup LookupFunc
	errs   []error // Collect the errors.
}

// string returns the variable's value, or def if it is not set or empty.
func (r *reader) string(key, def string) string {
	if v, ok := r.lookup(key); ok && v != "" {
		return v
	}
	return def
}

// duration parses the variable as a Go duration such as "15s", "500ms" or "2m".
// It returns def if the variable is not set or empty, and records an error if it can't be parsed.
func (r *reader) duration(key string, def time.Duration) time.Duration {
	v, ok := r.lookup(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v) // Try to parse the handwritten variable, .env must therefore use go syntax for durations.
	if err != nil {
		// %q quotes the variable v.
		r.errs = append(r.errs, fmt.Errorf("%s %q is not a valid duration (examples: 500ms, 15s, 2m)", key, v))
		return def
	}
	return d
}
