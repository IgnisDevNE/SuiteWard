package installer

import (
	"path/filepath"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// installationsFile is the list of the installations that installs made on this computer, beside the default settings file.
const installationsFile = "installations.yaml"

// installationsVersion is the only format of the list this program reads and writes.
const installationsVersion = 1

// installation is one entry of the list: where an installation keeps its settings and token copy, what it is made of, and the two ports it
// holds. It holds paths and ports, never a secret. Settings is the key.
type installation struct {
	Settings   string      `yaml:"settings"`
	TokenFile  string      `yaml:"token_file,omitempty"`
	Tier       config.Tier `yaml:"tier"`
	Runtime    string      `yaml:"runtime"`
	Connection string      `yaml:"connection,omitempty"`
	Instance   string      `yaml:"instance,omitempty"` // the 8 characters /status shows; empty when the run did not know them
	HTTPPort   int         `yaml:"http_port"`
	MCPPort    int         `yaml:"mcp_port"`
}

// installationList is the file.
type installationList struct {
	Version       int            `yaml:"version"`
	Installations []installation `yaml:"installations"`
}

// installationsPath is the list, beside the default settings file.
func installationsPath() (string, error) {
	settings, err := DefaultSettingsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(settings), installationsFile), nil
}

// loadInstallations reads the list; a file that is not there is an empty list.
func loadInstallations(path string) (installationList, error) {
	return installationList{}, nil
}

// samePath says whether two settings files are the same file by name: the same absolute path once cleaned, and on Windows whatever the case.
func samePath(goos, a, b string) bool { return false }

// recordInstallation adds the installation whose settings were just saved to the list, or refreshes its entry: one entry is changed in a
// list that was read and is replaced atomically. An empty instance keeps the one the entry had.
func recordInstallation(goos, settingsPath, tokenFile string, s Settings, instance string) error {
	return nil
}

// forgetInstallation takes the entry of a settings file that a purge removed out of the list.
func forgetInstallation(goos, settingsPath string) error { return nil }

// candidate is an installation a command may work on.
type candidate struct {
	installation
	isDefault bool
}

// installationCandidates are the installations whose settings file exists, the default one first, and how many entries were left out
// because their settings file is gone.
func installationCandidates(goos string) (found []candidate, stale int, err error) {
	return nil, 0, nil
}
