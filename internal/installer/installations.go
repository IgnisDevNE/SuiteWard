package installer

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

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

// loadInstallations reads the list; a file that is not there is an empty list. A file that cannot be read, or that another release wrote, is
// an error: a write must never replace a list this program does not understand.
func loadInstallations(path string) (installationList, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return installationList{Version: installationsVersion}, nil
	}
	if err != nil {
		return installationList{}, fmt.Errorf("read the list of installations %s: %w", path, err)
	}
	var list installationList
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&list); err != nil && !errors.Is(err, io.EOF) {
		return installationList{}, fmt.Errorf("read the list of installations %s: %w", path, err)
	}
	if list.Version != installationsVersion {
		return installationList{}, fmt.Errorf("the list of installations %s says version %d, and this program reads version %d: another release of SuiteWard wrote it", path, list.Version, installationsVersion)
	}
	return list, nil
}

// writeInstallations replaces the list atomically.
func writeInstallations(path string, list installationList) error {
	raw, err := yaml.Marshal(list)
	if err != nil {
		return fmt.Errorf("encode the list of installations: %w", err)
	}
	return writeFileAtomic(path, raw, 0o700)
}

// absolute is path as an absolute, cleaned path. shortcut: a path that cannot be made absolute (the working directory is gone) is only cleaned.
func absolute(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

// samePath says whether two settings files are the same file by name: the same absolute path once cleaned, and on Windows whatever the case,
// as isDefaultSettings compares.
func samePath(goos, a, b string) bool {
	if goos == "windows" {
		return strings.EqualFold(absolute(a), absolute(b))
	}
	return absolute(a) == absolute(b)
}

// recordInstallation adds the installation whose settings were just saved to the list, or refreshes its entry: one entry is changed in a
// list that was read and is replaced atomically, and the last write wins (there is no lock: a lost entry returns at the next upgrade). An
// empty instance keeps the one the entry had.
func recordInstallation(goos, settingsPath, tokenFile string, s Settings, instance string) error {
	path, err := installationsPath()
	if err != nil {
		return err
	}
	list, err := loadInstallations(path)
	if err != nil {
		return err
	}
	entry := installation{Settings: absolute(settingsPath), Tier: s.Tier, Runtime: s.Runtime, Connection: s.Connection, Instance: instance, HTTPPort: s.HTTPPort, MCPPort: s.MCPPort}
	if tokenFile != "" {
		entry.TokenFile = absolute(tokenFile)
	}
	if i := slices.IndexFunc(list.Installations, func(e installation) bool { return samePath(goos, e.Settings, entry.Settings) }); i >= 0 {
		entry.Instance = cmp.Or(instance, list.Installations[i].Instance)
		list.Installations[i] = entry
	} else {
		list.Installations = append(list.Installations, entry)
	}
	return writeInstallations(path, list)
}

// forgetInstallation takes the entry of a settings file that a purge removed out of the list. A file that holds no such entry is left as it is.
func forgetInstallation(goos, settingsPath string) error {
	path, err := installationsPath()
	if err != nil {
		return err
	}
	list, err := loadInstallations(path)
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(slices.Clone(list.Installations), func(e installation) bool { return samePath(goos, e.Settings, settingsPath) })
	if len(kept) == len(list.Installations) {
		return nil
	}
	list.Installations = kept
	return writeInstallations(path, list)
}

// candidate is an installation a command may work on.
type candidate struct {
	installation
	isDefault bool
}

// installationCandidates are the installations whose settings file exists, the default one first, and how many entries were left out
// because their settings file is gone. The default settings file is a candidate whenever it exists, with no entry too.
func installationCandidates(goos string) (found []candidate, stale int, err error) {
	path, err := installationsPath()
	if err != nil {
		return nil, 0, err
	}
	list, err := loadInstallations(path)
	if err != nil {
		return nil, 0, err
	}
	def, err := DefaultSettingsPath()
	if err != nil {
		return nil, 0, err
	}
	if settingsExist(def) {
		entry := installation{Settings: def}
		if i := slices.IndexFunc(list.Installations, func(e installation) bool { return samePath(goos, e.Settings, def) }); i >= 0 {
			entry = list.Installations[i]
		} else if s, err := LoadSettings(def); err == nil { // a file that cannot be read is still a candidate, and the command says what is wrong with it
			entry = installation{Settings: def, Tier: s.Tier, Runtime: s.Runtime, Connection: s.Connection, HTTPPort: s.HTTPPort, MCPPort: s.MCPPort}
		}
		found = append(found, candidate{installation: entry, isDefault: true})
	}
	for _, e := range list.Installations {
		switch {
		case slices.ContainsFunc(found, func(c candidate) bool { return samePath(goos, c.Settings, e.Settings) }):
		case !settingsExist(e.Settings):
			stale++
		default:
			found = append(found, candidate{installation: e})
		}
	}
	return found, stale, nil
}
