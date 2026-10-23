package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
