package installer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/archive"
)

// The members of a backup (docs/backup.md).
const (
	backupDump     = "suiteward.sql"
	backupVolume   = "suiteward-artifacts.tar"
	backupStatus   = "status.json"
	backupSettings = "settings.yaml"
	backupUnits    = "units/" // the unit files of the isolated tier
)

// backup is one encrypted backup of an installed instance that keeps running (docs/backup.md without its first step): the status of
// the service, then the database dump, then the artifact volume. The dump comes first, so the volume holds every artifact the dump
// refers to, because an artifact is written before its row and never deleted. The archive is written to dir as
// suiteward-backup-<time>.tar.age, readable by this user only, and only the holder of the recovery kit can open it: the dump holds
// the TOTP secret, so it is never a file of its own (Runtime.Exec holds it in memory), and the volume, which the engine writes to a
// path, waits in a private directory under dir until it is in the archive. A backup that fails leaves nothing behind.
type backup struct {
	rt             Runtime
	d              Deps
	dir            string                                    // where the archive goes; made when it is missing
	recipient      *age.HybridRecipient                      // the key the archive is encrypted to
	status         func(ctx context.Context) ([]byte, error) // the body of GET /status
	image          string                                    // the release image that runs: its digest goes in the manifest
	tier           config.Tier
	settings       string                     // the settings file; its bytes are a member when it exists
	units          map[string][]byte          // isolated tier: the unit files as installed, which are members too
	beside         func(archive string) error // more to write beside the archive, optional
	tolerateStatus bool
	now            func() time.Time
}

// backupFailed names the error of a backup as one that changed nothing: the instance only ran.
func backupFailed(err error) error {
	return fmt.Errorf("the backup failed, so nothing was changed: %w", err)
}

// takeBackup writes the archive and returns its path.
func takeBackup(ctx context.Context, b backup) (string, error) {
	run, err := b.start(ctx)
	if err != nil {
		return "", backupFailed(err)
	}
	for _, stage := range run.stages() {
		if err := stage.run(ctx); err != nil {
			return "", backupFailed(run.abort(err))
		}
	}
	path, err := run.finish()
	if err != nil {
		return "", backupFailed(run.abort(err))
	}
	return path, nil
}

// backupRun is a backup in progress. `suiteward backup` runs its stages one at a time, a step of the screen each.
type backupRun struct {
	backup
	spool      string // a private directory under dir, for the volume
	path       string // the archive, once this run has created it: only then may a failure remove it
	statusBody []byte
	doc        statusDoc // statusBody, read
	dump       []byte
	volume     string // the spooled volume
}

type backupStage struct {
	label string
	run   func(ctx context.Context) error
}

// start makes the directory and the private spool under it.
func (b backup) start(ctx context.Context) (*backupRun, error) {
	if err := ensureParent(b.dir); err != nil {
		return nil, err
	}
	spool, err := os.MkdirTemp(b.dir, ".suiteward-spool-")
	if err != nil {
		return nil, fmt.Errorf("create a private directory in %s: %w", b.dir, err)
	}
	r := &backupRun{backup: b, spool: spool}
	if err := restrictPath(ctx, b.d, spool, true); err != nil {
		return nil, r.abort(err)
	}
	return r, nil
}

func (r *backupRun) stages() []backupStage {
	return []backupStage{
		{"Reading the status of the service", r.readStatus},
		{"Dumping the database", r.dumpDatabase},
		{"Exporting the artifacts", r.exportVolume},
		{"Encrypting the backup", r.seal},
	}
}

// readStatus records which version the backup is of: a restore is checked against it.
func (r *backupRun) readStatus(ctx context.Context) error {
	body, err := r.status(ctx)
	if err != nil {
		return fmt.Errorf("read GET /status of the service (its version and instance id belong in the backup): %w", err)
	}
	doc, err := parseStatus(body)
	if err != nil {
		return err
	}
	r.statusBody, r.doc = body, doc
	return nil
}

func (r *backupRun) dumpDatabase(ctx context.Context) error {
	// shortcut: the dump is held in memory until it is in the archive, so it must fit in the installer's memory; spool it to a private
	// file and stream it from there when a database outgrows that. It is not spooled now: that would be a plaintext copy of the TOTP secret.
	dump, err := r.rt.Exec(ctx, ContainerDB, []string{"pg_dump", "-U", ownerRole, "-d", "suiteward"}, nil)
	if err != nil {
		return fmt.Errorf("pg_dump in %s: %w", ContainerDB, err)
	}
	// A plain-SQL dump starts with comment lines; anything else is an error message or nothing, never a backup.
	if !bytes.HasPrefix(dump, []byte("--")) {
		return fmt.Errorf("pg_dump in %s printed no SQL dump", ContainerDB)
	}
	r.dump = dump
	return nil
}

func (r *backupRun) exportVolume(ctx context.Context) error {
	path := filepath.Join(r.spool, backupVolume)
	if err := r.rt.ExportVolume(ctx, VolumeData, path); err != nil {
		return err
	}
	if err := restrictPath(ctx, r.d, path, false); err != nil {
		return err
	}
	r.volume = path
	return nil
}

// seal writes the archive.
func (r *backupRun) seal(ctx context.Context) error {
	path := filepath.Join(r.dir, archive.FileName(r.now()))
	// O_EXCL: a backup never replaces a file. Only a file this run made is its to remove.
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	r.path = path
	err = r.write(ctx, f)
	if cerr := f.Close(); err == nil { // closed before a failure removes it: Windows cannot delete an open file
		err = cerr
	}
	if err != nil {
		return err
	}
	if r.beside != nil {
		return r.beside(path)
	}
	return nil
}

func (r *backupRun) write(ctx context.Context, f *os.File) error {
	if err := restrictPath(ctx, r.d, r.path, false); err != nil { // before any data is in it
		return err
	}
	w, err := archive.NewWriter(f, r.recipient, archive.Info{
		SuitewardVersion: r.doc.Version, ImageDigest: digestOf(r.image), Instance: r.doc.Instance, Tier: string(r.tier), CreatedAt: r.now(),
	})
	if err != nil {
		return err
	}
	if err := r.addMembers(w); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return f.Sync()
}

func (r *backupRun) addMembers(w *archive.Writer) error {
	inMemory := []struct {
		name string
		data []byte
	}{{backupStatus, r.statusBody}, {backupDump, r.dump}}
	for _, m := range inMemory {
		if err := w.Add(m.name, int64(len(m.data)), bytes.NewReader(m.data)); err != nil {
			return err
		}
	}
	if err := r.addVolume(w); err != nil {
		return err
	}
	if err := r.addSettings(w); err != nil {
		return err
	}
	for _, name := range sortedKeys(r.units) {
		if err := w.Add(backupUnits+name, int64(len(r.units[name])), bytes.NewReader(r.units[name])); err != nil {
			return err
		}
	}
	return nil
}

func (r *backupRun) addVolume(w *archive.Writer) error {
	f, err := os.Open(filepath.Clean(r.volume))
	if err != nil {
		return fmt.Errorf("read the exported volume: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("read the exported volume: %w", err)
	}
	return w.Add(backupVolume, info.Size(), io.Reader(f))
}

// addSettings adds the settings file when there is one: a stack that has none yet is converted by the upgrade that saves them.
func (r *backupRun) addSettings(w *archive.Writer) error {
	if r.settings == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Clean(r.settings))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("read the settings %s: %w", r.settings, err)
	}
	return w.Add(backupSettings, int64(len(raw)), bytes.NewReader(raw))
}

// finish removes the spool and returns the archive.
func (r *backupRun) finish() (string, error) {
	if err := os.RemoveAll(r.spool); err != nil {
		return "", fmt.Errorf("remove the temporary directory %s of the backup: %w", r.spool, err)
	}
	return r.path, nil
}

// abort removes what this run made, the spool and a partial archive, and returns err with whatever it could not remove.
func (r *backupRun) abort(err error) error {
	errs := []error{err, os.RemoveAll(r.spool)}
	if r.path != "" {
		errs = append(errs, os.Remove(r.path))
	}
	return errors.Join(errs...)
}

// unitsKeptBeside is the directory beside an archive where the units an upgrade replaced are kept as plain files: they hold only the
// names of secrets, and a rollback copies them back.
func unitsKeptBeside(archivePath string) string {
	return strings.TrimSuffix(archivePath, ".tar.age") + ".units"
}

// keepUnitsBeside writes the units to unitsKeptBeside(archivePath).
func keepUnitsBeside(archivePath string, units map[string][]byte) error {
	dir := unitsKeptBeside(archivePath)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	for _, name := range sortedKeys(units) {
		if err := writePrivateFile(filepath.Join(dir, name), units[name]); err != nil {
			return errors.Join(err, os.RemoveAll(dir))
		}
	}
	return nil
}

// removeLeftovers removes the private directories that a backup or a restore that was killed left in dir.
func removeLeftovers(_ string, _ func(format string, args ...any)) error { return nil }
