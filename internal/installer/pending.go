package installer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// PendingApp is the GitHub App an install created and has not finished with: the record that lets a re-run take it up instead
// of creating a second one. It never holds the key, which is in the secret store.
type PendingApp struct {
	AppID       int64  `yaml:"app_id"`
	Slug        string `yaml:"slug"`
	Account     string `yaml:"account"`      // the login that owns the App
	AccountType string `yaml:"account_type"` // User or Organization
}

// pendingAppPath is where an install records the GitHub App it created before it has finished with it, beside the settings file.
func pendingAppPath(settingsPath string) string { return settingsPath + ".pending-app" }

func (p PendingApp) validate() error {
	switch {
	case p.AppID < 1:
		return errors.New("app_id is not a positive number")
	case !slugPattern.MatchString(p.Slug):
		return errors.New("slug is not an App slug")
	case !ghLoginPattern.MatchString(p.Account):
		return errors.New("account is not a GitHub login")
	case p.AccountType != "User" && p.AccountType != "Organization":
		return errors.New("account_type is not User or Organization")
	}
	return nil
}

// savePendingApp writes the record, replacing the one that was there.
func savePendingApp(path string, p PendingApp) error {
	if err := p.validate(); err != nil {
		return fmt.Errorf("refusing to record an invalid App: %w", err)
	}
	raw, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode the record of the App: %w", err)
	}
	return writeFileAtomic(path, raw, 0o700)
}

// loadPendingApp reads the record; found is false when there is none. A record that cannot be read is an error that names the
// file: a re-run that ignored it would create a second App.
func loadPendingApp(path string) (p PendingApp, found bool, err error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		return PendingApp{}, false, nil
	}
	if err != nil {
		return PendingApp{}, false, fmt.Errorf("read the record of the App: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&p); err != nil && !errors.Is(err, io.EOF) {
		return PendingApp{}, false, fmt.Errorf("parse %s (delete it if its App is gone): %w", path, err)
	}
	if err := p.validate(); err != nil {
		return PendingApp{}, false, fmt.Errorf("invalid record of an App in %s (delete it if its App is gone): %w", path, err)
	}
	return p, true, nil
}
