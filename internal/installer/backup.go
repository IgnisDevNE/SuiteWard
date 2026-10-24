package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// restrictPath makes a file or a directory readable by this user only: an ACL on Windows, mode 0600 or 0700 elsewhere.
func restrictPath(ctx context.Context, d Deps, path string, dir bool) error {
	path = filepath.Clean(path)
	if d.GOOS == "windows" {
		// The SID, not the name: a domain or Entra ID account may not resolve by name in icacls.
		if d.UserSID == nil {
			return errors.New("the installer has no way to find the SID of this user, so " + path + " cannot be restricted to it")
		}
		sid, err := d.UserSID()
		if err == nil && !strings.HasPrefix(sid, "S-1-") {
			err = fmt.Errorf("%q is not a SID", sid)
		}
		if err != nil {
			return fmt.Errorf("find the SID of this user, so that %s can be restricted to it: %w", path, err)
		}
		grant := "*" + sid + ":F"
		if dir {
			grant = "*" + sid + ":(OI)(CI)F"
		}
		if _, err := d.Exec(ctx, "icacls", []string{path, "/inheritance:r", "/grant:r", grant}, nil); err != nil {
			return fmt.Errorf("restrict %s to this user: %w", path, err)
		}
		return nil
	}
	mode := os.FileMode(0o600)
	if dir {
		mode = 0o700
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("restrict %s to this user: %w", path, err)
	}
	return nil
}

// osUserSID is the SID of the current user, which Windows reports as the user id.
func osUserSID() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Uid, nil
}

// writePrivateFile creates path, which must not exist yet, with mode 0600 and writes data to it.
func writePrivateFile(path string, data []byte) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ensureParent makes the directory the backup directory goes in. A symbolic link is refused: the backup holds the TOTP secret,
// and a link could send it anywhere. Only the last component is looked at, which is the one the installer chooses.
func ensureParent(parent string) error {
	info, err := os.Lstat(parent)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", parent, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("look at %s: %w", parent, err)
	case info.Mode().Type() != fs.ModeDir:
		return fmt.Errorf("%s is a symbolic link or not a directory: a backup is never written through a link, so name a real directory with --backup-dir", parent)
	}
	return nil
}

// An error of an upgrade step that came after the backup says how far the upgrade got, which decides what to advise. A step
// before migrate leaves the database as it was; stoppedError is that, on the isolated tier, once suiteward.service was stopped.
type (
	stoppedError   struct{ error }
	migratingError struct{ error }
)

func (e stoppedError) Unwrap() error   { return e.error }
func (e migratingError) Unwrap() error { return e.error }

// withBackup adds where the backup is, and the way back or on that fits how far the upgrade got, to the error of a step that came
// after the backup. unitsDir is the account's unit directory on the isolated tier, empty on the co-located one. Before migrate
// the schema and the data are unchanged; a rerun of --upgrade does not help from there, because the stack no longer looks like
// the installed old release (the co-located service is gone, the isolated units name the new release).
func withBackup(err error, path, unitsDir string) error {
	var migrating migratingError
	if errors.As(err, &migrating) {
		return fmt.Errorf("%w\nThe backup is in %s. To go back, install the previous release and run `suiteward restore %s` with your recovery kit (the restore names the release to install); everything after the backup is lost (%s)", err, path, quoteArg(path), goingBack)
	}
	advice := fmt.Sprintf("Migrate did not run, so the schema and the data are unchanged. The backup is in %s.", path)
	var stopped stoppedError
	switch {
	case unitsDir == "":
		advice += " If the service is not running, run `suiteward install` with the options of this run to start the release in the settings again; `suiteward install --upgrade` retries the upgrade."
	case errors.As(err, &stopped):
		// suiteward-migrate.service stays active after it ran (RemainAfterExit), so going on restarts it; starting the service alone
		// would run the new release on the old schema.
		advice += fmt.Sprintf(" suiteward.service is stopped, and the units this upgrade replaced are in %s. To go back, copy them to %s, run `systemctl --user daemon-reload` and `systemctl --user start suiteward.service`. To go on once the cause is fixed, run `systemctl --user daemon-reload`, then `systemctl --user restart suiteward-db.service` (only if the database image changed), then `systemctl --user restart suiteward-migrate.service` and `systemctl --user start suiteward.service`.", unitsKeptBeside(path), unitsDir)
	default:
		advice += " Fix the cause and run `suiteward install --upgrade` again."
	}
	return fmt.Errorf("%w\n%s", err, advice)
}

// writeRestrictedFile creates path, which must not exist, restricts it to this user and only then writes data to it, so that data
// that must stay private is never in a file that others can read. A file it could not finish is removed.
func writeRestrictedFile(ctx context.Context, d Deps, path string, data []byte) error {
	path = filepath.Clean(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return errors.Join(fmt.Errorf("create %s: %w", path, err), os.Remove(path))
	}
	err = restrictPath(ctx, d, path, false)
	if err == nil {
		if err = os.WriteFile(path, data, 0o600); err != nil {
			err = fmt.Errorf("write %s: %w", path, err)
		}
	}
	if err != nil {
		return errors.Join(err, os.Remove(path))
	}
	return nil
}
