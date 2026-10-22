// Package installer holds the building blocks of the suiteward installer: the settings file, the answers and prompts, the
// container runtime seam for Podman and Docker, secret writers, the rendered quadlet units and the adoption guard.
package installer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// Default ports published on the loopback interface (D-INSTALLER).
const (
	DefaultHTTPPort = 8081
	DefaultMCPPort  = 8091
	// MaxRepositories is the most repositories one instance serves.
	MaxRepositories = 20
)

// Container runtimes the installer can drive.
const (
	RuntimePodman = "podman"
	RuntimeDocker = "docker"
)

// ErrNoSettings means the settings file does not exist yet.
var ErrNoSettings = errors.New("no settings file")

var (
	// The same owner/name shape as SUITEWARD_GITHUB_REPOSITORIES.
	repositoryName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,39}/[A-Za-z0-9._-]{1,100}$`)
	// A connection or context name never starts with a dash, so it cannot be read as a flag.
	connectionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:/-]*$`)
)

// Settings is the installer-owned settings file: the choices of an install that `--upgrade` and `doctor` read back.
type Settings struct {
	Tier       config.Tier `yaml:"tier"`
	Runtime    string      `yaml:"runtime"`
	Connection string      `yaml:"connection,omitempty"` // Podman connection or Docker context
	HTTPPort   int         `yaml:"http_port"`
	MCPPort    int         `yaml:"mcp_port"`
	JobTimeout string      `yaml:"job_timeout,omitempty"` // a Go duration
	GitHub     GitHub      `yaml:"github,omitempty"`
}

// GitHub holds the non-secret GitHub values; they exist only after the App is created and installed.
type GitHub struct {
	AppID          int64             `yaml:"app_id,omitempty"`
	InstallationID int64             `yaml:"installation_id,omitempty"`
	Owner          string            `yaml:"owner,omitempty"`
	Repositories   []string          `yaml:"repositories,omitempty"`
	TestChecks     map[string]string `yaml:"test_checks,omitempty"` // repository -> required test check
}

// DefaultSettings returns the co-located Podman defaults.
func DefaultSettings() Settings {
	return Settings{Tier: config.TierCoLocated, Runtime: RuntimePodman, HTTPPort: DefaultHTTPPort, MCPPort: DefaultMCPPort}
}

// DefaultSettingsPath is the per-user location, os.UserConfigDir()/suiteward/settings.yaml.
func DefaultSettingsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the user configuration directory: %w", err)
	}
	return filepath.Join(dir, "suiteward", "settings.yaml"), nil
}

// Validate reports every problem of the settings in one error.
func (s Settings) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if s.Tier != config.TierCoLocated && s.Tier != config.TierIsolated {
		add("tier %q is not %s or %s", s.Tier, config.TierCoLocated, config.TierIsolated)
	}
	switch {
	case s.Runtime != RuntimePodman && s.Runtime != RuntimeDocker:
		add("runtime %q is not %s or %s", s.Runtime, RuntimePodman, RuntimeDocker)
	case s.Tier == config.TierIsolated && s.Runtime != RuntimePodman:
		add("the isolated tier runs on Podman only, not %s", s.Runtime)
	}
	if s.Connection != "" && !connectionName.MatchString(s.Connection) {
		add("connection %q is not a plain connection or context name", s.Connection)
	}
	for _, port := range []struct {
		key   string
		value int
	}{{"http_port", s.HTTPPort}, {"mcp_port", s.MCPPort}} {
		if port.value < 1 || port.value > 65535 {
			add("%s %d is outside 1-65535", port.key, port.value)
		}
	}
	if s.HTTPPort == s.MCPPort {
		add("http_port and mcp_port must be distinct")
	}
	if s.JobTimeout != "" {
		if d, err := time.ParseDuration(s.JobTimeout); err != nil || d <= 0 {
			add("job_timeout %q is not a positive Go duration", s.JobTimeout)
		}
	}
	return errors.Join(append(errs, s.GitHub.validate())...)
}

// validate checks the GitHub values; a zero GitHub is valid because the App may not exist yet.
func (g GitHub) validate() error {
	if g.AppID == 0 && g.InstallationID == 0 && g.Owner == "" && len(g.Repositories) == 0 && len(g.TestChecks) == 0 {
		return nil
	}
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if g.AppID < 1 {
		add("github app_id is required")
	}
	if g.InstallationID < 1 {
		add("github installation_id is required")
	}
	if g.Owner == "" {
		add("github owner is required")
	}
	if n := len(g.Repositories); n < 1 || n > MaxRepositories {
		add("github repositories: %d listed, between 1 and at most %d are required", n, MaxRepositories)
	}
	seen := map[string]bool{}
	for _, repo := range g.Repositories {
		owner, name, _ := strings.Cut(repo, "/")
		switch lower := strings.ToLower(repo); {
		case !repositoryName.MatchString(repo) || owner == "." || owner == ".." || name == "." || name == "..":
			add("github repository %q is not an owner/name pair", repo)
		case seen[lower]:
			add("github repository %q is listed twice", repo)
		case !strings.EqualFold(owner, g.Owner):
			add("github repository %q has another owner than %q", repo, g.Owner)
		default:
			seen[lower] = true
		}
	}
	for repo, check := range g.TestChecks {
		if !slices.Contains(g.Repositories, repo) {
			add("github test_checks names %q, which is not a listed repository", repo)
		}
		if strings.TrimSpace(check) == "" {
			add("github test_checks for %q is empty", repo)
		}
	}
	return errors.Join(errs...)
}

// LoadSettings reads path over the defaults and validates it. A missing file is ErrNoSettings.
func LoadSettings(path string) (Settings, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, fmt.Errorf("%s: %w", path, ErrNoSettings)
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}
	s := DefaultSettings()
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&s); err != nil && !errors.Is(err, io.EOF) {
		return Settings{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return Settings{}, fmt.Errorf("invalid settings in %s: %w", path, err)
	}
	return s, nil
}

// SaveSettings validates s and writes it to path (mode 0600, directory 0700), replacing the file atomically.
func SaveSettings(path string, s Settings) error {
	if err := s.Validate(); err != nil {
		return fmt.Errorf("refusing to save invalid settings: %w", err)
	}
	raw, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	return writeFileAtomic(path, raw, 0o700)
}

// writeFileAtomic replaces path with data through a temporary file in the same directory. CreateTemp makes the file 0600,
// so it is never wider than the final file; the directory is created with dirMode when it is missing.
func writeFileAtomic(path string, data []byte, dirMode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create a temporary file in %s: %w", dir, err)
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// Flushes of writeFileAtomic; tests replace them to see when they happen.
var (
	syncFile = func(*os.File) error { return nil }
	syncDir  = func(string) error { return nil }
)
