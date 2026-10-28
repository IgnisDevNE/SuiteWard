package installer

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
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
	if len(bytes.TrimSpace(raw)) == 0 {
		return installationList{}, fmt.Errorf("the list of installations %s is empty: it must start with `version: %d`", path, installationsVersion)
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
	for i, e := range list.Installations {
		if problem := e.problem(); problem != "" {
			return installationList{}, fmt.Errorf("the list of installations %s cannot be used: entry %d %s", path, i+1, problem)
		}
	}
	return list, nil
}

// problem says what is wrong with an entry that an install could not have written, or "" when there is nothing: the list is a file that anyone who can
// write beside the settings can edit, and what it holds is printed and acted on. The text never repeats a path, which may hold a control character.
func (e installation) problem() string {
	switch {
	case !filepath.IsAbs(e.Settings):
		return "has a settings file that is not an absolute path"
	case e.TokenFile != "" && !filepath.IsAbs(e.TokenFile):
		return "has a token file that is not an absolute path"
	case printable(e.Settings) != e.Settings || printable(e.TokenFile) != e.TokenFile:
		return "has a path with a control character in it"
	case e.Tier != config.TierCoLocated && e.Tier != config.TierIsolated:
		return fmt.Sprintf("has the type %q, which is not %s or %s", e.Tier, config.TierCoLocated, config.TierIsolated)
	case e.Runtime != RuntimePodman && e.Runtime != RuntimeDocker:
		return fmt.Sprintf("has the container program %q, which is not %s or %s", e.Runtime, RuntimePodman, RuntimeDocker)
	case e.Connection != "" && !connectionName.MatchString(e.Connection):
		return fmt.Sprintf("has the connection %q, which is not a plain name", e.Connection)
	case e.Instance != "" && !instanceID.MatchString(e.Instance):
		return fmt.Sprintf("has the instance id %q, which is not 8 hexadecimal characters", e.Instance)
	case e.HTTPPort < 1 || e.HTTPPort > 65535 || e.MCPPort < 1 || e.MCPPort > 65535:
		return "has a port outside 1-65535"
	}
	return ""
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
	// An entry that the list would refuse when it is read is not written: it would make every later command refuse the whole list.
	if problem := entry.problem(); problem != "" {
		return fmt.Errorf("this installation's entry %s, so it was not listed", problem)
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

// listInstallation adds the installation whose settings were just saved to the list. A list that cannot be written is a warning and not a
// failure: the installation works, and its entry returns at the next upgrade.
func listInstallation(scr *ui.Screen, d Deps, settingsPath, tokenFile string, s Settings, instance string) {
	if err := recordInstallation(d.GOOS, settingsPath, tokenFile, s, instance); err != nil {
		scr.Warn("Could not update the list of installations: %v. The installation itself works. To add it to the list, run `%s` later.", err, d.suggest("install", "--upgrade"))
	}
}

// unlistInstallation takes the installation whose settings a purge removed out of the list. A list that cannot be written is a warning: an entry
// whose settings file is gone is skipped by every command.
func unlistInstallation(scr *ui.Screen, d Deps, settingsPath string) {
	if err := forgetInstallation(d.GOOS, settingsPath); err != nil {
		scr.Warn("Could not take the installation out of the list of installations: %v. This does no harm: an entry whose settings file is gone is skipped.", err)
	}
}

// noteInstance wraps a reader of /status so that the instance id of the first answer that names one is kept in id: the list records what the run
// reads anyway, and the run asks for nothing more.
func noteInstance(id *string, status func(context.Context) ([]byte, error)) func(context.Context) ([]byte, error) {
	return func(ctx context.Context) ([]byte, error) {
		body, err := status(ctx)
		if err == nil && *id == "" {
			if doc, parseErr := parseStatus(body); parseErr == nil {
				*id = doc.Instance
			}
		}
		return body, err
	}
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
