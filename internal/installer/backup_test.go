package installer

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupRefusesAParentDirectoryThatIsASymbolicLink(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "backups")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("this system does not let the test create a symbolic link: %v", err)
	}
	before := u.units()
	err := u.upgrade("--image", testDigest, "--yes", "--backup-dir", link)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("a backup directory behind a link could send the dump anywhere, got %v", err)
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Errorf("a backup was written through the link: %v", entries)
	}
	u.untouched(before)
}

// aclRuntime answers the two calls a backup makes of the engine.
type aclRuntime struct{ Runtime }

func (aclRuntime) Exec(context.Context, string, []string, io.Reader) ([]byte, error) {
	return []byte("-- dump\n"), nil
}

func (aclRuntime) ExportVolume(_ context.Context, _, path string) error {
	return os.WriteFile(path, []byte("TARBYTES"), 0o600)
}

func TestBackupRestrictsItsDirectoryAndEveryFileToThisUserOnWindows(t *testing.T) {
	var acl [][]string
	d := Deps{
		GOOS:        "windows",
		Getenv:      func(k string) string { return map[string]string{"USERNAME": "tester"}[k] },
		Interactive: func(context.Context, string, []string) error { return nil },
		Exec: func(_ context.Context, name string, args []string, _ io.Reader) ([]byte, error) {
			if name != "icacls" {
				t.Errorf("unexpected command %s %v", name, args)
			}
			acl = append(acl, append([]string{name}, args...))
			return nil, nil
		},
	}
	dir, err := takeBackup(context.Background(), backup{
		rt: aclRuntime{}, d: d, parent: t.TempDir(), now: func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) },
		status: func(context.Context) ([]byte, error) { return []byte(`{"version":"v0.2.0"}`), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"icacls", dir, "/inheritance:r", "/grant:r", "tester:(OI)(CI)F"},
		{"icacls", filepath.Join(dir, backupStatus), "/inheritance:r", "/grant:r", "tester:F"},
		{"icacls", filepath.Join(dir, backupDump), "/inheritance:r", "/grant:r", "tester:F"},
		{"icacls", filepath.Join(dir, backupVolume), "/inheritance:r", "/grant:r", "tester:F"},
	}
	if len(acl) != len(want) {
		t.Fatalf("icacls ran %d times, want %d: %v", len(acl), len(want), acl)
	}
	for i := range want {
		if strings.Join(acl[i], "\x00") != strings.Join(want[i], "\x00") {
			t.Errorf("icacls call %d is %q, want %q", i, acl[i], want[i])
		}
	}
}
