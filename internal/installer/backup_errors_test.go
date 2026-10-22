package installer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func windowsDepsWith(sid func() (string, error), exec Exec) Deps {
	return Deps{
		GOOS:        "windows",
		Getenv:      func(string) string { return "" },
		UserSID:     sid,
		Interactive: func(context.Context, string, []string) error { return nil },
		Exec:        exec,
	}
}

func TestRestrictPathRefusesWhatItCannotRestrict(t *testing.T) {
	ctx := context.Background()
	ok := func(context.Context, string, []string, io.Reader) ([]byte, error) { return nil, nil }
	sid := func() (string, error) { return testSID, nil }
	cases := []struct {
		name string
		d    Deps
		want string
	}{
		{"no way to find the SID", windowsDepsWith(nil, ok), "no way to find the SID"},
		{"a SID that cannot be found", windowsDepsWith(func() (string, error) { return "", errInjected }, ok), "find the SID of this user"},
		{"a SID that is not one", windowsDepsWith(func() (string, error) { return "tester", nil }, ok), `"tester" is not a SID`},
		{"icacls that fails", windowsDepsWith(sid, func(context.Context, string, []string, io.Reader) ([]byte, error) { return nil, errInjected }), "restrict"},
		{"a path that is not there", Deps{GOOS: "linux"}, "restrict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gone")
			err := restrictPath(ctx, tc.d, path, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), path) {
				t.Fatalf("restrictPath = %v, want an error naming the path and containing %q", err, tc.want)
			}
		})
	}
}

func TestOSUserSIDNamesTheCurrentUser(t *testing.T) {
	sid, err := osUserSID()
	if err != nil {
		t.Skipf("this system cannot name the current user: %v", err)
	}
	if runtime.GOOS == "windows" {
		if !regexp.MustCompile(`^S-1-`).MatchString(sid) {
			t.Errorf("the SID of the current user is %q, want one that starts with S-1-", sid)
		}
	} else if !regexp.MustCompile(`^[0-9]+$`).MatchString(sid) {
		t.Errorf("the id of the current user is %q, want a number", sid)
	}
}

func TestWritePrivateFileNeverReplacesAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dump.sql")
	if err := writePrivateFile(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(path, []byte("two")); err == nil || !strings.Contains(err.Error(), "create "+path) {
		t.Fatalf("a file that exists was written again: %v", err)
	}
	if raw := readFile(t, path); raw != "one" {
		t.Errorf("the file was replaced: %q", raw)
	}
	if err := writePrivateFile(filepath.Join(dir, "missing", "dump.sql"), nil); err == nil {
		t.Error("a file in a directory that does not exist was written")
	}
}

func TestEnsureParentRefusesWhatIsNotADirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureParent(file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("a file as the parent: %v", err)
	}
	if err := ensureParent(filepath.Join(file, "sub")); err == nil || !strings.Contains(err.Error(), file) {
		t.Errorf("a parent below a file: %v", err)
	}
	if err := ensureParent(dir + "/bad\x00name"); err == nil {
		t.Error("a parent that cannot be examined was accepted")
	}
	made := filepath.Join(dir, "a", "b")
	if err := ensureParent(made); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(made); err != nil || !info.IsDir() {
		t.Errorf("the parent was not made: %v", err)
	}
}

// backupRuntime is the engine of a backup: what the dump prints, and what fails.
type backupRuntime struct {
	Runtime
	dump      string
	dumpErr   error
	exportErr error
}

func (r backupRuntime) Exec(context.Context, string, []string, io.Reader) ([]byte, error) {
	return []byte(r.dump), r.dumpErr
}

func (r backupRuntime) ExportVolume(_ context.Context, _, path string) error {
	if r.exportErr != nil {
		return r.exportErr
	}
	return os.WriteFile(path, []byte("TARBYTES"), 0o600)
}

func testBackup(t *testing.T, rt Runtime) backup {
	t.Helper()
	return backup{
		rt: rt, d: Deps{GOOS: "linux"}, parent: t.TempDir(), now: func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) },
		status: func(context.Context) ([]byte, error) { return []byte(`{"version":"v0.2.0"}`), nil },
	}
}

// A backup that fails part way leaves nothing behind: a half backup that looks like a whole one is worse than none.
func TestBackupThatFailsLeavesNothingBehind(t *testing.T) {
	good := backupRuntime{dump: "-- dump\n"}
	cases := []struct {
		name string
		edit func(b *backup)
		want string
	}{
		{"the status cannot be read", func(b *backup) {
			b.status = func(context.Context) ([]byte, error) { return nil, errInjected }
		}, "read GET /status"},
		{"the dump fails", func(b *backup) { b.rt = backupRuntime{dumpErr: errInjected} }, "pg_dump in suiteward-db"},
		{"the dump is not SQL", func(b *backup) { b.rt = backupRuntime{dump: "ERROR: the database is down\n"} }, "printed no SQL dump"},
		{"the volume cannot be exported", func(b *backup) { b.rt = backupRuntime{dump: good.dump, exportErr: errInjected} }, errInjected.Error()},
		{"the exported volume cannot be restricted", func(b *backup) {
			b.d = windowsDepsWith(func() (string, error) { return testSID, nil }, func(_ context.Context, _ string, args []string, _ io.Reader) ([]byte, error) {
				if strings.HasSuffix(args[0], backupVolume) {
					return nil, errInjected
				}
				return nil, nil
			})
		}, "restrict"},
		{"the directory cannot be restricted", func(b *backup) {
			b.d = windowsDepsWith(func() (string, error) { return testSID, nil }, func(context.Context, string, []string, io.Reader) ([]byte, error) { return nil, errInjected })
		}, "restrict"},
		{"an extra file fails", func(b *backup) { b.extra = func(string) error { return errInjected } }, errInjected.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := testBackup(t, good)
			tc.edit(&b)
			dir, err := takeBackup(context.Background(), b)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "nothing was changed") {
				t.Fatalf("takeBackup = %q, %v; want an error containing %q", dir, err, tc.want)
			}
			if entries, _ := os.ReadDir(b.parent); len(entries) != 0 {
				t.Errorf("a failed backup left %v", entries)
			}
			if strings.Contains(err.Error(), "dump") && strings.Contains(err.Error(), "-- dump") {
				t.Errorf("the dump reached the error: %v", err)
			}
		})
	}
}

func TestBackupNeverReusesADirectory(t *testing.T) {
	b := testBackup(t, backupRuntime{dump: "-- dump\n"})
	existing := filepath.Join(b.parent, "suiteward-backup-20261022-120000")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(existing, "keep")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := takeBackup(context.Background(), b); err == nil || !strings.Contains(err.Error(), existing) {
		t.Fatalf("takeBackup = %v, want a refusal that names the directory", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the directory of an earlier backup was disturbed: %v", err)
	}
}

func TestBackupFailureWithAParentThatIsAFile(t *testing.T) {
	b := testBackup(t, backupRuntime{dump: "-- dump\n"})
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	b.parent = file
	if _, err := takeBackup(context.Background(), b); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("takeBackup = %v", err)
	}
}

// The advice after a failed upgrade says how far it got, and an error of a step keeps its cause.
func TestWithBackupAdviseByHowFarTheUpgradeGot(t *testing.T) {
	cause := errors.New("boom")
	if !errors.Is(migratingError{cause}, cause) || !errors.Is(stoppedError{cause}, cause) {
		t.Error("the marker errors do not unwrap to their cause")
	}
	cases := []struct {
		name     string
		err      error
		unitsDir string
		want     []string
		not      []string
	}{
		{"after migrate started", migratingError{cause}, "", []string{"restore it with the previous image", "lost"}, []string{"Migrate did not run"}},
		{"before migrate, co-located", cause, "", []string{"Migrate did not run", "suiteward install --upgrade"}, []string{"daemon-reload"}},
		{"before migrate, isolated, service still running", cause, "/units", []string{"Migrate did not run", "Fix the cause and run `suiteward install --upgrade` again"}, []string{"daemon-reload"}},
		{"before migrate, isolated, service stopped", stoppedError{cause}, "/units", []string{"suiteward.service is stopped", "daemon-reload", "/units"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := withBackup(tc.err, "/backups/one", tc.unitsDir)
			if !errors.Is(err, cause) {
				t.Errorf("the cause was lost: %v", err)
			}
			for _, want := range append(tc.want, "/backups/one") {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the advice lacks %q:\n%s", want, err)
				}
			}
			for _, not := range tc.not {
				if strings.Contains(err.Error(), not) {
					t.Errorf("the advice has %q although the upgrade did not get that far:\n%s", not, err)
				}
			}
		})
	}
}
