package installer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		user := d.Getenv("USERNAME")
		if user == "" {
			return errors.New("USERNAME is not set, so " + path + " cannot be restricted to this user")
		}
		grant := user + ":F"
		if dir {
			grant = user + ":(OI)(CI)F"
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
	say    func(format string, args ...any)
	now    func() time.Time
}

// take returns the directory the backup is in.
func (b backup) take(ctx context.Context) (string, error) {
	if err := os.MkdirAll(b.parent, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", b.parent, err)
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
	// The status says which version these files are of. A service that does not answer is no reason to refuse the backup of its
	// data, and often the reason for the upgrade.
	if status, serr := b.status(ctx); serr != nil {
		b.say("Warning: %s was not written, the service did not answer GET /status (%v). The backup holds the data.\n", backupStatus, serr)
	} else if err := b.write(ctx, filepath.Join(dir, backupStatus), status); err != nil {
		return err
	}
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

// withBackup adds where the backup is to the error of a step that came after it.
func withBackup(err error, dir string) error {
	return fmt.Errorf("%w\nThe backup is in %s; restore it with the previous image to go back (docs/install.md, section 11.6)", err, dir)
}
