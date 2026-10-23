package installer

import (
	"context"
	"errors"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// backup is one encrypted backup of an installed instance, written to dir as suiteward-backup-<time>.tar.age.
type backup struct {
	rt        Runtime
	d         Deps
	dir       string                                    // where the archive goes; made when it is missing
	recipient *age.HybridRecipient                      // the key the archive is encrypted to
	status    func(ctx context.Context) ([]byte, error) // the body of GET /status
	image     string                                    // the release image that runs: its digest goes in the manifest
	tier      config.Tier
	settings  string                     // the settings file; its bytes are a member when it exists
	units     map[string][]byte          // isolated tier: the unit files as installed, which are members too
	beside    func(archive string) error // more to write beside the archive, optional
	now       func() time.Time
}

// takeBackup writes the archive and returns its path.
func takeBackup(_ context.Context, _ backup) (string, error) {
	return "", errors.New("the encrypted backup is not implemented")
}

// unitsKeptBeside is the directory beside an archive where the units an upgrade replaced are kept as plain files: they hold only the
// names of secrets, and a rollback copies them back.
func unitsKeptBeside(archivePath string) string {
	return strings.TrimSuffix(archivePath, ".tar.age") + ".units"
}
