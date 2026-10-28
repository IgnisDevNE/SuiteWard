package installer

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/archive"
)

// openBackup opens the archive at path with the identity of the test key, and returns its manifest and its members by name, as the
// restore reads them.
func openBackup(t *testing.T, path string) (archive.Manifest, map[string]string) {
	t.Helper()
	_, identity := testRecoveryKey(t)
	return openBackupWith(t, path, identity)
}

func openBackupWith(t *testing.T, path, identity string) (archive.Manifest, map[string]string) {
	t.Helper()
	id, err := archive.ParseIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	r, err := archive.Open(f, id)
	if err != nil {
		t.Fatalf("the backup %s does not open with the recovery key: %v", path, err)
	}
	dir := t.TempDir()
	manifest, err := r.Extract(dir)
	if err != nil {
		t.Fatal(err)
	}
	members := map[string]string{}
	if err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		members[filepath.ToSlash(rel)] = readFile(t, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return manifest, members
}

// memberNames lists the names of the members of a backup, in the order the manifest has them.
func memberNames(m archive.Manifest) []string {
	names := make([]string, len(m.Members))
	for i, member := range m.Members {
		names[i] = member.Name
	}
	return names
}

// backupEngine is the engine of a backup: what the dump prints, what the volume holds and what fails.
type backupEngine struct {
	Runtime
	dump      string
	dumpErr   error
	exportErr error
	instance  string            // what the database says its instance id is, "" when it says nothing
	volume    string            // what the volume holds, "TARBYTES" when empty
	onExport  func(path string) // called before the volume is written
	execs     [][]string        // the argument lists of the commands run in a container
}

func (e *backupEngine) Exec(_ context.Context, container string, argv []string, stdin io.Reader) ([]byte, error) {
	e.execs = append(e.execs, append([]string{container}, argv...))
	if stdin != nil { // a query of the instance table
		if sql, err := io.ReadAll(stdin); err == nil && strings.Contains(string(sql), "suiteward_instance") {
			return []byte(e.instance + "\n"), nil
		}
	}
	return []byte(e.dump), e.dumpErr
}

func (e *backupEngine) ExportVolume(_ context.Context, _, path string) error {
	if e.onExport != nil {
		e.onExport(path)
	}
	if e.exportErr != nil {
		return e.exportErr
	}
	return os.WriteFile(path, []byte(cmp.Or(e.volume, "TARBYTES")), 0o600)
}

const backupSettingsText = "tier: isolated\nruntime: podman\nhttp_port: 8081\nmcp_port: 8091\n"

var backupTime = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// newBackup is a backup of an isolated stack with every member, into a directory that does not exist yet.
func newBackup(t *testing.T, rt Runtime) backup {
	t.Helper()
	recipient, _ := testRecoveryKey(t)
	rec, err := archive.ParseRecipient(recipient)
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settings, []byte(backupSettingsText), 0o600); err != nil {
		t.Fatal(err)
	}
	return backup{
		rt: rt, d: Deps{GOOS: "linux"}, dir: filepath.Join(t.TempDir(), "backups"), recipient: rec,
		status: func(context.Context) ([]byte, error) { return []byte(statusBody), nil },
		image:  oldImage, tier: config.TierIsolated, settings: settings,
		units: map[string][]byte{"suiteward.container": []byte("[Container]\nImage=x\n"), "suiteward-data.volume": []byte("[Volume]\n")},
		now:   func() time.Time { return backupTime },
	}
}

func TestBackupWritesOneEncryptedArchiveWithTheMembersOfTheInstance(t *testing.T) {
	engine := &backupEngine{dump: dumpText}
	b := newBackup(t, engine)
	path, err := takeBackup(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(b.dir, "suiteward-backup-20261022-120000.tar.age"); path != want {
		t.Errorf("the archive is %s, want %s", path, want)
	}
	manifest, members := openBackup(t, path)
	want := map[string]string{
		"status.json": statusBody, "suiteward.sql": dumpText, "suiteward-artifacts.tar": "TARBYTES", "settings.yaml": backupSettingsText,
		"units/suiteward.container": "[Container]\nImage=x\n", "units/suiteward-data.volume": "[Volume]\n",
	}
	for name, content := range want {
		if members[name] != content {
			t.Errorf("%s holds %q, want %q", name, members[name], content)
		}
	}
	if len(members) != len(want) {
		t.Errorf("the archive holds %v, want only %d members", memberNames(manifest), len(want))
	}
	// The dump comes before the volume: the volume holds every artifact the dump refers to.
	if names := memberNames(manifest); slices.Index(names, "suiteward.sql") > slices.Index(names, "suiteward-artifacts.tar") {
		t.Errorf("the volume is before the dump in %v", names)
	}
	if manifest.SuitewardVersion != "v0.2.0" || manifest.Instance != testInstance || manifest.Tier != "isolated" ||
		manifest.ImageDigest != oldDigest || !manifest.CreatedAt.Equal(backupTime) {
		t.Errorf("the manifest names %+v", manifest.Info)
	}
	if len(engine.execs) != 1 || strings.Join(engine.execs[0], " ") != "suiteward-db pg_dump -U suiteward -d suiteward" {
		t.Errorf("the database was dumped with %v", engine.execs)
	}
}

func TestBackupOfTheColocatedTierHasNoUnitsAndWorksWithoutASettingsFile(t *testing.T) {
	b := newBackup(t, &backupEngine{dump: dumpText})
	b.tier, b.units, b.settings = config.TierCoLocated, nil, filepath.Join(t.TempDir(), "absent.yaml")
	path, err := takeBackup(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := openBackup(t, path)
	if names := memberNames(manifest); !slices.Equal(names, []string{"status.json", "suiteward.sql", "suiteward-artifacts.tar"}) || manifest.Tier != "co-located" {
		t.Errorf("the archive holds %v as %s", names, manifest.Tier)
	}
}

// The backup is encrypted to the recipient and to nothing else.
func TestBackupOpensOnlyWithTheKeyOfItsRecipient(t *testing.T) {
	path, err := takeBackup(context.Background(), newBackup(t, &backupEngine{dump: dumpText}))
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := archive.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	id, err := archive.ParseIdentity(other)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := archive.Open(f, id); !errors.Is(err, archive.ErrWrongKey) {
		t.Errorf("another key opened the backup: %v", err)
	}
}

// The dump holds the TOTP secret: it is in the archive only, never in a file of its own, and nothing is left of the spooled volume.
func TestBackupHoldsNothingInPlaintextAndLeavesNoSpoolBehind(t *testing.T) {
	var spool string
	engine := &backupEngine{dump: dumpText}
	b := newBackup(t, engine)
	engine.onExport = func(path string) {
		spool = filepath.Dir(path)
		if !strings.HasPrefix(spool, b.dir+string(filepath.Separator)) {
			t.Errorf("the volume is spooled to %s, which is not under %s", path, b.dir)
		}
		if runtime.GOOS != "windows" {
			if info, err := os.Stat(spool); err != nil || info.Mode().Perm() != 0o700 {
				t.Errorf("the spool directory has mode %v, %v; want 0700", info.Mode().Perm(), err)
			}
		}
		if entries, _ := os.ReadDir(spool); len(entries) != 0 {
			t.Errorf("the spool holds %v before the volume is exported: the dump must not be written in plaintext", entries)
		}
	}
	path, err := takeBackup(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if spool == "" {
		t.Fatal("the volume was never spooled, so the test proves nothing")
	}
	entries, err := os.ReadDir(b.dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("%s holds %v, %v; want only the archive", b.dir, entries, err)
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{b.dir: 0o700, path: 0o600} {
			if info, err := os.Stat(p); err != nil || info.Mode().Perm() != want {
				t.Errorf("%s has mode %v, %v; want %v", p, info.Mode().Perm(), err, want)
			}
		}
	}
	raw := readFile(t, path)
	for _, plain := range []string{"HUNTER2-TOTP", "TARBYTES", "PostgreSQL"} {
		if strings.Contains(raw, plain) {
			t.Errorf("the archive holds %q in plaintext", plain)
		}
	}
}

// A backup that fails part way leaves nothing behind: a half backup that looks like a whole one is worse than none.
func TestBackupThatFailsLeavesNothingBehind(t *testing.T) {
	sidOK := func() (string, error) { return testSID, nil }
	cases := []struct {
		name string
		edit func(b *backup)
		want string
	}{
		{"the status cannot be read", func(b *backup) { b.status = func(context.Context) ([]byte, error) { return nil, errInjected } }, "read /status of the service"},
		{"the status names no instance", func(b *backup) {
			b.status = func(context.Context) ([]byte, error) { return []byte(`{"version":"v0.2.0"}`), nil }
		}, "instance"},
		{"the dump fails", func(b *backup) { b.rt = &backupEngine{dumpErr: errInjected} }, "pg_dump in suiteward-db"},
		{"the dump is not SQL", func(b *backup) { b.rt = &backupEngine{dump: "ERROR: the database is down\n"} }, "produced no database dump"},
		{"the volume cannot be exported", func(b *backup) { b.rt = &backupEngine{dump: dumpText, exportErr: errInjected} }, errInjected.Error()},
		{"the spool directory cannot be restricted", func(b *backup) {
			b.d = windowsDepsWith(sidOK, func(context.Context, string, []string, io.Reader) ([]byte, error) { return nil, errInjected })
		}, "private to this user"},
		{"the exported volume cannot be restricted", func(b *backup) {
			b.d = windowsDepsWith(sidOK, func(_ context.Context, _ string, args []string, _ io.Reader) ([]byte, error) {
				if strings.HasSuffix(args[0], backupVolume) {
					return nil, errInjected
				}
				return nil, nil
			})
		}, "private to this user"},
		{"the archive cannot be restricted", func(b *backup) {
			b.d = windowsDepsWith(sidOK, func(_ context.Context, _ string, args []string, _ io.Reader) ([]byte, error) {
				if strings.HasSuffix(args[0], ".tar.age") {
					return nil, errInjected
				}
				return nil, nil
			})
		}, "private to this user"},
		{"what goes beside the archive fails", func(b *backup) { b.beside = func(string) error { return errInjected } }, errInjected.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBackup(t, &backupEngine{dump: dumpText})
			tc.edit(&b)
			path, err := takeBackup(context.Background(), b)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "nothing was changed") {
				t.Fatalf("takeBackup = %q, %v; want an error containing %q", path, err, tc.want)
			}
			if entries, _ := os.ReadDir(b.dir); len(entries) != 0 {
				t.Errorf("a failed backup left %v", entries)
			}
			for _, secret := range []string{"HUNTER2-TOTP", "-- PostgreSQL"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the dump reached the error: %v", err)
				}
			}
		})
	}
}

func TestBackupNeverReusesAFile(t *testing.T) {
	b := newBackup(t, &backupEngine{dump: dumpText})
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(b.dir, archive.FileName(backupTime))
	if err := os.WriteFile(existing, []byte("an earlier backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := takeBackup(context.Background(), b); err == nil || !strings.Contains(err.Error(), existing) {
		t.Fatalf("takeBackup = %v, want a refusal that names the file", err)
	}
	if got := readFile(t, existing); got != "an earlier backup" {
		t.Errorf("the file of an earlier backup was disturbed: %q", got)
	}
	if entries, _ := os.ReadDir(b.dir); len(entries) != 1 {
		t.Errorf("the refused backup left %v", entries)
	}
}

func TestBackupFailureWithAParentThatIsAFile(t *testing.T) {
	b := newBackup(t, &backupEngine{dump: dumpText})
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	b.dir = file
	if _, err := takeBackup(context.Background(), b); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("takeBackup = %v", err)
	}
}

func TestBackupRestrictsEveryFileToThisUserOnWindows(t *testing.T) {
	var acl [][]string
	b := newBackup(t, &backupEngine{dump: dumpText})
	b.d = windowsDepsWith(func() (string, error) { return testSID, nil }, func(_ context.Context, name string, args []string, _ io.Reader) ([]byte, error) {
		acl = append(acl, append([]string{name}, args...))
		return nil, nil
	})
	path, err := takeBackup(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(acl) != 3 {
		t.Fatalf("icacls ran %d times, want 3 (the spool directory, the volume, the archive): %v", len(acl), acl)
	}
	for i, call := range acl {
		if call[0] != "icacls" || call[2] != "/inheritance:r" || call[3] != "/grant:r" {
			t.Errorf("call %d is %q", i, call)
		}
	}
	if !strings.HasPrefix(acl[0][1], b.dir) || acl[0][4] != "*"+testSID+":(OI)(CI)F" {
		t.Errorf("the spool directory: %q", acl[0])
	}
	if filepath.Base(acl[1][1]) != backupVolume || acl[1][4] != "*"+testSID+":F" {
		t.Errorf("the volume: %q", acl[1])
	}
	if acl[2][1] != path || acl[2][4] != "*"+testSID+":F" {
		t.Errorf("the archive: %q, want it restricted before it is written", acl[2])
	}
}

// The backup of the state that a restore replaces is taken whether or not the service answers: its status is recorded as unavailable,
// with the reason, and the manifest names the installed image and what is known of the instance, never something made up.
func TestBackupThatMayLackAStatusRecordsItAsUnavailable(t *testing.T) {
	down := func(context.Context) ([]byte, error) { return nil, errors.New("connection refused") }
	b := newBackup(t, &backupEngine{dump: dumpText, instance: "0a0b0c0d"})
	b.tolerateStatus, b.status = true, down
	path, err := takeBackup(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	manifest, members := openBackup(t, path)
	var recorded struct {
		Unavailable bool   `json:"unavailable"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(members[backupStatus]), &recorded); err != nil || !recorded.Unavailable || recorded.Reason != "connection refused" {
		t.Errorf("status.json is %q (%v)", members[backupStatus], err)
	}
	if manifest.SuitewardVersion != "unknown" || manifest.Instance != "0a0b0c0d" || manifest.ImageDigest != oldDigest {
		t.Errorf("the manifest names %+v: the instance is the one the database knows, the release is not made up", manifest.Info)
	}

	// An instance the database does not name, or names in another shape, is "unknown".
	for name, answer := range map[string]string{"nothing": "", "a shape that is not an instance id": "1"} {
		t.Run(name, func(t *testing.T) {
			b := newBackup(t, &backupEngine{dump: dumpText, instance: answer})
			b.tolerateStatus, b.status = true, down
			path, err := takeBackup(context.Background(), b)
			if err != nil {
				t.Fatal(err)
			}
			if manifest, _ := openBackup(t, path); manifest.Instance != "unknown" {
				t.Errorf("the instance is %q", manifest.Instance)
			}
		})
	}

	// A backup that does not tolerate it still needs the status.
	strict := newBackup(t, &backupEngine{dump: dumpText})
	strict.status = down
	if _, err := takeBackup(context.Background(), strict); err == nil || !strings.Contains(err.Error(), "read /status of the service") {
		t.Errorf("a backup without a status was taken: %v", err)
	}
}

// leftovers makes the directory of a backup with what a backup and a restore that were killed leave, and what is not theirs.
func leftovers(t *testing.T, dir string) (stale, keep []string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale = []string{".suiteward-spool-111", ".suiteward-restore-222"}
	for _, name := range stale {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, backupVolume), []byte("plaintext"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keep = []string{".suiteward-spool-a-file", "mine", "suiteward-backup-20250101-000000.tar.age"}
	if err := os.WriteFile(filepath.Join(dir, keep[0]), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, keep[1]), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, keep[2]), []byte("an older backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	return stale, keep
}

// A backup or a restore that was killed leaves a private directory with plaintext in it. The next one removes the directories that
// carry the names of the two, and only those, and says which.
func TestRemoveLeftoversRemovesWhatAKilledBackupOrRestoreLeft(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	stale, keep := leftovers(t, dir)
	var said []string
	if err := removeLeftovers(dir, func(format string, args ...any) { said = append(said, fmt.Sprintf(format, args...)) }); err != nil {
		t.Fatal(err)
	}
	for _, name := range stale {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s was left", name)
		}
		if !strings.Contains(strings.Join(said, "\n"), name) {
			t.Errorf("nothing says that %s was removed: %v", name, said)
		}
	}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
	// A directory that does not exist yet has none, and without a place to say it they are removed all the same.
	if err := removeLeftovers(filepath.Join(t.TempDir(), "absent"), nil); err != nil {
		t.Errorf("a directory that does not exist: %v", err)
	}
	quiet := filepath.Join(t.TempDir(), "quiet")
	stale, _ = leftovers(t, quiet)
	if err := removeLeftovers(quiet, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range stale {
		if _, err := os.Stat(filepath.Join(quiet, name)); err == nil {
			t.Errorf("%s was left when there was nowhere to say so", name)
		}
	}
}
