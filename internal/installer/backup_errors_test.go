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
		{"no way to find the SID", windowsDepsWith(nil, ok), "cannot find the Windows account ID (SID)"},
		{"a SID that cannot be found", windowsDepsWith(func() (string, error) { return "", errInjected }, ok), "could not find this user's Windows account ID"},
		{"a SID that is not one", windowsDepsWith(func() (string, error) { return "tester", nil }, ok), `"tester" is not a Windows account ID (SID)`},
		{"icacls that fails", windowsDepsWith(sid, func(context.Context, string, []string, io.Reader) ([]byte, error) { return nil, errInjected }), "private to this user"},
		{"a path that is not there", Deps{GOOS: "linux"}, "private to this user"},
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
	if err := ensureParent(file); err == nil || !strings.Contains(err.Error(), "not a folder") {
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
		{"after migrate started", migratingError{cause}, "", []string{"suiteward restore /backups/one.tar.age", "lost"}, []string{"did not run, so your data is unchanged"}},
		{"before migrate, co-located", cause, "", []string{"did not run, so your data is unchanged", "suiteward install --upgrade"}, []string{"daemon-reload"}},
		{"before migrate, isolated, service still running", cause, "/units", []string{"did not run, so your data is unchanged", "Fix the cause, then run `suiteward install --upgrade` again"}, []string{"daemon-reload"}},
		{"before migrate, isolated, service stopped", stoppedError{cause}, "/units", []string{"suiteward.service is stopped", "daemon-reload", "/units", "/backups/one.units"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := withBackup("suiteward", tc.err, "/backups/one.tar.age", tc.unitsDir, "")
			if !errors.Is(err, cause) {
				t.Errorf("the cause was lost: %v", err)
			}
			for _, want := range append(tc.want, "/backups/one.tar.age") {
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
