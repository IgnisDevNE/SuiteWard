// Package installer holds the building blocks of the suiteward installer: the settings file, the answers and prompts, the
// container runtime seam for Podman and Docker, secret writers, the rendered quadlet units and the adoption guard.
package installer

import (
	"errors"

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

var errNotImplemented = errors.New("not implemented")

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
func DefaultSettingsPath() (string, error) { return "", errNotImplemented }

// Validate reports every problem of the settings in one error.
func (s Settings) Validate() error { return errNotImplemented }

// LoadSettings reads path over the defaults and validates it. A missing file is ErrNoSettings.
func LoadSettings(path string) (Settings, error) { return Settings{}, errNotImplemented }

// SaveSettings validates s and writes it to path (mode 0600, directory 0700), replacing the file atomically.
func SaveSettings(path string, s Settings) error { return errNotImplemented }
