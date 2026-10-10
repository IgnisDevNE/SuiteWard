// Package config turns an injected environment into validated settings for
// `suiteward serve` and `suiteward probe` (see docs/contracts/runtime.md).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	prefix   = "SUITEWARD_"
	redacted = "[redacted]"
)

// toolingPrefixes name variables that development shells and CI set; they are never warned about.
var toolingPrefixes = []string{prefix + "TEST_", prefix + "TOOLS_", prefix + "HOOK_"}

// Secret is a setting value that must never be rendered. Reveal returns it.
type Secret string

// Reveal returns the secret value for the code that must use it.
func (s Secret) Reveal() string { return string(s) }

// String implements fmt.Stringer and hides the value.
func (Secret) String() string { return redacted }

// GoString implements fmt.GoStringer and hides the value.
func (Secret) GoString() string { return redacted }

// MarshalText hides the value from JSON and slog encoders.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Config holds every validated setting.
type Config struct {
	DatabaseURL        Secret
	ArtifactDir        string
	HTTPAddr           string
	ShutdownTimeout    time.Duration
	LogLevel           slog.Level
	JobWorkers         int
	JobTimeout         time.Duration
	JobMaxAttempts     int
	OutboxMaxAttempts  int
	OutboxPollInterval time.Duration
	OutboxLease        time.Duration
}

// Load validates the environment against the settings table of the runtime
// contract. lookup reads one variable, environ lists them all as NAME=value
// (only to warn about unrecognized SUITEWARD_* names) and readFile serves the
// _FILE form of secrets. Every problem is reported in one error that names the
// variable; secret values are never included. The warnings are returned even
// when err is not nil.
func Load(lookup func(string) (string, bool), environ []string, readFile func(string) ([]byte, error)) (Config, []string, error) {
	l := &loader{lookup: lookup, readFile: readFile, known: map[string]bool{}}
	var c Config

	c.DatabaseURL = Secret(l.required("DATABASE_URL", true))
	if c.DatabaseURL != "" && !validDatabaseURL(c.DatabaseURL.Reveal()) {
		// No cause: url.Parse errors quote the whole URL, password included.
		l.fail("DATABASE_URL", errors.New("must be a postgres:// or postgresql:// URL"))
	}
	c.ArtifactDir = l.required("ARTIFACT_DIR", false)
	if c.ArtifactDir != "" && !filepath.IsAbs(c.ArtifactDir) {
		l.fail("ARTIFACT_DIR", errors.New("must be an absolute path"))
	}
	c.HTTPAddr = l.optional("HTTP_ADDR", "127.0.0.1:8080")
	if err := validHTTPAddr(c.HTTPAddr); err != nil {
		l.fail("HTTP_ADDR", err)
	}
	c.LogLevel = l.logLevel("LOG_LEVEL", "info")

	c.ShutdownTimeout = number(l, "SHUTDOWN_TIMEOUT", "30s", time.Second, 10*time.Minute, time.ParseDuration)
	c.JobWorkers = number(l, "JOB_WORKERS", "4", 1, 64, strconv.Atoi)
	c.JobTimeout = number(l, "JOB_TIMEOUT", "1m", 1, 0, time.ParseDuration)
	c.JobMaxAttempts = number(l, "JOB_MAX_ATTEMPTS", "5", 1, 25, strconv.Atoi)
	c.OutboxMaxAttempts = number(l, "OUTBOX_MAX_ATTEMPTS", "8", 1, 25, strconv.Atoi)
	c.OutboxPollInterval = number(l, "OUTBOX_POLL_INTERVAL", "5s", time.Second, time.Hour, time.ParseDuration)
	c.OutboxLease = number(l, "OUTBOX_LEASE", "1m", 5*time.Second, 0, time.ParseDuration)

	warnings := l.unrecognized(environ)
	if len(l.errs) > 0 {
		return Config{}, warnings, errors.Join(l.errs...)
	}
	return c, warnings, nil
}

// OS wires the real process environment and filesystem for main: config.Load(config.OS()).
func OS() (func(string) (string, bool), []string, func(string) ([]byte, error)) {
	return os.LookupEnv, os.Environ(), os.ReadFile
}

type loader struct {
	lookup   func(string) (string, bool)
	readFile func(string) ([]byte, error)
	known    map[string]bool
	errs     []error
}

func (l *loader) fail(name string, err error) {
	l.errs = append(l.errs, fmt.Errorf("%s%s: %w", prefix, name, err))
}

// raw returns the value of a setting from NAME or, for a secret, from the file
// named by NAME_FILE. ok is false when the value is absent, empty or unusable;
// an unusable one has already been reported.
func (l *loader) raw(name string, secret bool) (value string, ok bool) {
	l.known[prefix+name], l.known[prefix+name+"_FILE"] = true, true
	direct, hasDirect := l.lookup(prefix + name)
	path, hasFile := l.lookup(prefix + name + "_FILE")
	switch {
	case hasFile && !secret:
		l.fail(name+"_FILE", errors.New("only secret settings can be read from a file"))
	case hasFile && hasDirect:
		l.fail(name+"_FILE", fmt.Errorf("must not be set together with %s%s", prefix, name))
	case hasFile:
		content, err := l.readFile(path)
		if err != nil {
			l.fail(name+"_FILE", fmt.Errorf("cannot read the file: %w", err))
			return "", false
		}
		value = strings.TrimSuffix(strings.TrimSuffix(string(content), "\n"), "\r")
		if value == "" {
			l.fail(name+"_FILE", errors.New("the file is empty"))
		}
		return value, value != ""
	default:
		return direct, direct != ""
	}
	return "", false
}

// required returns a mandatory setting, or "" after reporting why it is missing.
func (l *loader) required(name string, secret bool) string {
	reported := len(l.errs)
	v, ok := l.raw(name, secret)
	if !ok && len(l.errs) == reported {
		l.fail(name, errors.New("is required"))
	}
	return v
}

func (l *loader) optional(name, def string) string {
	if v, ok := l.raw(name, false); ok {
		return v
	}
	return def
}

func (l *loader) logLevel(name, def string) slog.Level {
	switch v := l.optional(name, def); v {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		l.fail(name, fmt.Errorf("%q is not one of debug, info, warn, error", v))
		return slog.LevelInfo
	}
}

// number reads an optional integer or duration in [lo, hi]; hi 0 means no upper bound.
func number[T int | time.Duration](l *loader, name, def string, lo, hi T, parse func(string) (T, error)) T {
	v, err := parse(l.optional(name, def))
	switch {
	case err != nil:
	case v < lo || (hi != 0 && v > hi):
		err = fmt.Errorf("%v is outside the range %v to %v", v, lo, hi)
		if hi == 0 {
			err = fmt.Errorf("%v is below the minimum %v", v, lo)
		}
	default:
		return v
	}
	l.fail(name, err)
	return 0
}

// unrecognized lists the SUITEWARD_* names in environ that no setting claimed.
func (l *loader) unrecognized(environ []string) []string {
	var warnings []string
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, prefix) || l.known[name] ||
			slices.ContainsFunc(toolingPrefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
			continue
		}
		warnings = append(warnings, "unrecognized setting "+name)
	}
	slices.Sort(warnings)
	return slices.Compact(warnings)
}

func validDatabaseURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql")
}

func validHTTPAddr(s string) error {
	_, port, err := net.SplitHostPort(s)
	if err != nil {
		return err
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("port %q is not a number from 1 to 65535", port)
	}
	return nil
}
