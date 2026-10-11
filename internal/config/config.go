// Package config turns an injected environment into validated settings for
// `suiteward serve` and `suiteward probe` (see docs/contracts/runtime.md).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	prefix   = "SUITEWARD_"
	redacted = "[redacted]"

	maxGitHubRepositories = 20 // D-SYNC
)

var gitHubRepository = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

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
	// GitHub is nil when the synchronization settings are not configured.
	GitHub *GitHub
}

// GitHub holds the interim M1.4 operator configuration of the GitHub App
// (see docs/contracts/github.md); it does not establish a trusted installation.
type GitHub struct {
	AppID          int64
	AppKey         Secret
	InstallationID int64
	// Repositories are owner/name items, trimmed, all of one owner.
	Repositories []string
	APIURL       string
}

// Load validates the environment against the settings table of the runtime
// contract. lookup reads one variable, environ lists them all as NAME=value
// (only to warn about unrecognized SUITEWARD_* names) and readFile serves the
// _FILE form of secrets. Every problem is reported in one error that names the
// variable; secret values are never included. The warnings are returned even
// when err is not nil.
func Load(lookup func(string) (string, bool), environ []string, readFile func(string) ([]byte, error)) (Config, []string, error) {
	// Injected dependencies are trust boundaries: fail loudly instead of panicking later.
	if lookup == nil {
		return Config{}, nil, errors.New("config: lookup function is required")
	}
	if readFile == nil {
		return Config{}, nil, errors.New("config: readFile function is required")
	}
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
	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		l.fail("HTTP_ADDR", err)
	}
	c.LogLevel = l.logLevel("LOG_LEVEL", "info")

	c.ShutdownTimeout = number(l, "SHUTDOWN_TIMEOUT", "30s", time.Second, 10*time.Minute, time.ParseDuration)
	c.JobWorkers = number(l, "JOB_WORKERS", "4", 1, 64, strconv.Atoi)
	c.JobTimeout = number(l, "JOB_TIMEOUT", "1m", time.Second, time.Hour, time.ParseDuration)
	c.JobMaxAttempts = number(l, "JOB_MAX_ATTEMPTS", "5", 1, 25, strconv.Atoi)
	c.OutboxMaxAttempts = number(l, "OUTBOX_MAX_ATTEMPTS", "8", 1, 25, strconv.Atoi)
	c.OutboxPollInterval = number(l, "OUTBOX_POLL_INTERVAL", "5s", time.Second, time.Hour, time.ParseDuration)
	c.OutboxLease = number(l, "OUTBOX_LEASE", "1m", 5*time.Second, 0, time.ParseDuration)
	c.GitHub = l.github()

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
// named by NAME_FILE. A variable that is set but empty counts as unset. ok is
// false when the value is absent or unusable; an unusable one has already
// been reported.
func (l *loader) raw(name string, secret bool) (value string, ok bool) {
	l.known[prefix+name], l.known[prefix+name+"_FILE"] = true, true
	direct, _ := l.lookup(prefix + name)
	path, _ := l.lookup(prefix + name + "_FILE")
	hasDirect, hasFile := direct != "", path != ""
	switch {
	case hasFile && !secret:
		l.fail(name+"_FILE", errors.New("only secret settings can be read from a file"))
	case hasFile && hasDirect:
		l.fail(name+"_FILE", fmt.Errorf("must not be set together with %s%s", prefix, name))
	case hasFile:
		content, err := l.readFile(path)
		if err != nil {
			// The path is dropped: an operator may have put the secret itself in NAME_FILE.
			var pathErr *fs.PathError
			if errors.As(err, &pathErr) {
				err = pathErr.Err
			}
			l.fail(name+"_FILE", fmt.Errorf("cannot read the file: %w", err))
			return "", false
		}
		value = strings.TrimSuffix(strings.TrimSuffix(string(content), "\n"), "\r")
		if value == "" {
			l.fail(name+"_FILE", errors.New("the file is empty"))
		}
		return value, value != ""
	default:
		return direct, hasDirect
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

// present is raw for a setting that may be absent: set is also true when the
// value is set but unusable, which raw has already reported.
func (l *loader) present(name string, secret bool) (value string, set bool) {
	reported := len(l.errs)
	value, ok := l.raw(name, secret)
	return value, ok || len(l.errs) > reported
}

// github reads the interim M1.4 GitHub settings: the first four are all set
// or all unset (nil), the API URL is checked either way.
func (l *loader) github() *GitHub {
	apiURL := l.optional("GITHUB_API_URL", "https://api.github.com")
	if !validAPIURL(apiURL) {
		// No cause and no value: url.Parse errors quote the whole URL.
		l.fail("GITHUB_API_URL", errors.New("must be an absolute https URL (http only for a loopback host) without query, fragment or user info"))
	}

	g := GitHub{APIURL: apiURL}
	appID, hasID := l.present("GITHUB_APP_ID", false)
	key, hasKey := l.present("GITHUB_APP_KEY", true)
	installationID, hasInstallation := l.present("GITHUB_INSTALLATION_ID", false)
	repositories, hasRepositories := l.present("GITHUB_REPOSITORIES", false)
	var missing []string
	for _, s := range []struct {
		name string
		set  bool
	}{{"GITHUB_APP_ID", hasID}, {"GITHUB_APP_KEY", hasKey}, {"GITHUB_INSTALLATION_ID", hasInstallation}, {"GITHUB_REPOSITORIES", hasRepositories}} {
		if !s.set {
			missing = append(missing, prefix+s.name)
		}
	}
	if len(missing) == 4 {
		return nil
	}
	if len(missing) > 0 {
		l.fail("GITHUB_*", fmt.Errorf("set all of the GitHub settings or none; missing %s", strings.Join(missing, ", ")))
	}

	// An empty value here was already reported by raw.
	g.AppKey = Secret(key)
	if appID != "" {
		g.AppID = l.positive("GITHUB_APP_ID", appID)
	}
	if installationID != "" {
		g.InstallationID = l.positive("GITHUB_INSTALLATION_ID", installationID)
	}
	if repositories != "" {
		g.Repositories = l.repositories("GITHUB_REPOSITORIES", repositories)
	}
	return &g
}

func (l *loader) positive(name, value string) int64 {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 1 {
		l.fail(name, errors.New("must be a positive integer"))
		return 0
	}
	return n
}

// repositories parses comma-separated owner/name items: at most
// maxGitHubRepositories, no duplicates, one owner (an installation token covers one account).
func (l *loader) repositories(name, value string) []string {
	items := strings.Split(value, ",")
	if len(items) > maxGitHubRepositories {
		l.fail(name, fmt.Errorf("%d items, at most %d are allowed", len(items), maxGitHubRepositories))
		return nil
	}
	seen := map[string]bool{}
	var owner string
	for i, item := range items {
		item = strings.TrimSpace(item)
		items[i] = item
		itemOwner, repo, _ := strings.Cut(item, "/")
		switch lower := strings.ToLower(item); {
		case !gitHubRepository.MatchString(item) || itemOwner == "." || itemOwner == ".." || repo == "." || repo == "..":
			l.fail(name, fmt.Errorf("%q is not an owner/name pair", item))
		case seen[lower]:
			l.fail(name, fmt.Errorf("%q is listed twice", item))
		case owner != "" && !strings.EqualFold(owner, itemOwner):
			l.fail(name, fmt.Errorf("%q has another owner than the first item", item))
		default:
			seen[lower] = true
			owner = itemOwner
		}
	}
	return items
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

// unrecognized lists (names only, sorted) the SUITEWARD_* names in environ that no setting claimed.
func (l *loader) unrecognized(environ []string) []string {
	var warnings []string
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, prefix) || l.known[name] ||
			slices.ContainsFunc(toolingPrefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
			continue
		}
		warnings = append(warnings, name)
	}
	slices.Sort(warnings)
	return slices.Compact(warnings)
}

func validDatabaseURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql")
}

func validAPIURL(s string) bool {
	u, err := url.Parse(s)
	// A User of nil is how net/url says there is no user info.
	if err != nil || strings.ContainsAny(s, "?#") || u.User != nil || u.Hostname() == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		addr, err := netip.ParseAddr(u.Hostname())
		return strings.EqualFold(u.Hostname(), "localhost") || (err == nil && addr.IsLoopback())
	default:
		return false
	}
}
