package installer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

// The files of a backup (docs/backup.md).
const (
	backupDump   = "suiteward.sql"
	backupVolume = "suiteward-artifacts.tar"
	backupStatus = "status.json"
	backupUnits  = "units" // the unit files an isolated upgrade replaces
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

// backup takes the backup of an instance that keeps running (docs/backup.md without its first step): the status of the service,
// then the database dump, then the artifact volume. The dump comes first, so the volume holds every artifact the dump refers to,
// because an artifact is written before its row and never deleted. A directory that only this user can read is made under parent
// and named with the time; a backup that fails leaves nothing behind. The dump and the tarball hold the TOTP secret and are
// never printed.
type backup struct {
	rt     Runtime
	d      Deps
	parent string
	status func(ctx context.Context) ([]byte, error) // the body of GET /status
	extra  func(dir string) error                    // more files for the directory, optional
	now    func() time.Time
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

// take returns the directory the backup is in.
func (b backup) take(ctx context.Context) (string, error) {
	if err := ensureParent(b.parent); err != nil {
		return "", err
	}
	// Mkdir, not MkdirAll: an existing directory is never reused, so a backup never mixes with another.
	dir := filepath.Join(b.parent, "suiteward-backup-"+b.now().Format("20060102-150405"))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	if err := b.fill(ctx, dir); err != nil {
		return "", errors.Join(err, os.RemoveAll(dir))
	}
	return dir, nil
}

// fill writes the files of the backup into dir.
func (b backup) fill(ctx context.Context, dir string) error {
	if err := restrictPath(ctx, b.d, dir, true); err != nil {
		return err
	}
	// The status says which version these files are of, and a restore is checked against it (docs/backup.md).
	status, err := b.status(ctx)
	if err != nil {
		return fmt.Errorf("read GET /status of the service (its version belongs in the backup): %w", err)
	}
	if err := b.write(ctx, filepath.Join(dir, backupStatus), status); err != nil {
		return err
	}
	// shortcut: the dump is held in memory until it is written, so it must fit in the installer's memory, stream it to the file when a database outgrows that.
	dump, err := b.rt.Exec(ctx, ContainerDB, []string{"pg_dump", "-U", ownerRole, "-d", "suiteward"}, nil)
	if err != nil {
		return fmt.Errorf("pg_dump in %s: %w", ContainerDB, err)
	}
	// A plain-SQL dump starts with comment lines; anything else is an error message or nothing, never a backup.
	if !bytes.HasPrefix(dump, []byte("--")) {
		return fmt.Errorf("pg_dump in %s printed no SQL dump", ContainerDB)
	}
	if err := b.write(ctx, filepath.Join(dir, backupDump), dump); err != nil {
		return err
	}
	tarball := filepath.Join(dir, backupVolume)
	if err := b.rt.ExportVolume(ctx, VolumeData, tarball); err != nil {
		return err
	}
	if err := restrictPath(ctx, b.d, tarball, false); err != nil {
		return err
	}
	if b.extra != nil {
		if err := b.extra(dir); err != nil {
			return err
		}
	}
	return nil
}

func (b backup) write(ctx context.Context, path string, data []byte) error {
	if err := writePrivateFile(path, data); err != nil {
		return err
	}
	return restrictPath(ctx, b.d, path, false)
}

// takeBackup is backup.take, with the error named as a backup failure that changed nothing.
func takeBackup(ctx context.Context, b backup) (string, error) {
	dir, err := b.take(ctx)
	if err != nil {
		return "", fmt.Errorf("the backup failed, so nothing was changed: %w", err)
	}
	return dir, nil
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
func withBackup(err error, dir, unitsDir string) error {
	var migrating migratingError
	if errors.As(err, &migrating) {
		return fmt.Errorf("%w\nThe backup is in %s; restore it with the previous image to go back, and everything after the backup is lost (docs/backup.md, section 3)", err, dir)
	}
	advice := fmt.Sprintf("Migrate did not run, so the schema and the data are unchanged. The backup is in %s.", dir)
	var stopped stoppedError
	switch {
	case unitsDir == "":
		advice += " If the service is not running, run `suiteward install` with the options of this run to start the release in the settings again; `suiteward install --upgrade` retries the upgrade."
	case errors.As(err, &stopped):
		// suiteward-migrate.service stays active after it ran (RemainAfterExit), so going on restarts it; starting the service alone
		// would run the new release on the old schema.
		advice += fmt.Sprintf(" suiteward.service is stopped, and the units this upgrade replaced are in %s. To go back, copy them to %s, run `systemctl --user daemon-reload` and `systemctl --user start suiteward.service`. To go on once the cause is fixed, run `systemctl --user daemon-reload`, then `systemctl --user restart suiteward-db.service` (only if the database image changed), then `systemctl --user restart suiteward-migrate.service` and `systemctl --user start suiteward.service`.", filepath.Join(dir, backupUnits), unitsDir)
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
