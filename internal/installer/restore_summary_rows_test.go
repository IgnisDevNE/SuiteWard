package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noRepositories is the status of a backup of an instance that has none (GitHub off): no "github" list.
var noRepositories = strings.Replace(statusBody, `,"github":[{"repo":"octo/app","integrity":"passed"}]`, ``, 1)

// restoreOnScreenOf restores into the harness on a full screen of 100x30 and gives the page the summary drew and what stayed after it.
func restoreOnScreenOf(t *testing.T, args func(u *upgradeHarness) []string) (page, scrollback string) {
	t.Helper()
	u := restoreHarness(t)
	run := newScreenRun(termWidth, termHeight, testInstance+"\n", "\n")
	if err := Restore(liveContext(t), args(u), run.on(u.env())); err != nil {
		t.Fatalf("Restore: %v\n%s", err, run.output())
	}
	out := run.output()
	return pageText(t, out[:strings.LastIndex(out, leaveAlt)], termWidth, termHeight), run.console.tail()
}

// The Restored row of the summary names the backup as a person reads it: the path as it is, which a shell would need quoted when it has a
// space, and a directory of an earlier release as a directory and not as a flag. A command that is printed keeps its quoting.
func TestTheRestoredRowOfTheSummaryShowsThePlainPathOfTheBackup(t *testing.T) {
	t.Run("a file", func(t *testing.T) {
		var file string
		_, tail := restoreOnScreenOf(t, func(u *upgradeHarness) []string {
			file = aBackup(t, func(b *backup, _ *backupEngine) {
				b.dir = filepath.Join(t.TempDir(), "my backups")
				if err := os.Mkdir(b.dir, 0o700); err != nil {
					t.Fatal(err)
				}
			})
			return u.restoreArgs(file, "--kit", writeTestKit(t, t.TempDir()))
		})
		if !strings.Contains(tail, "Restored: "+file+"\n") || strings.Contains(tail, "'"+file+"'") {
			t.Errorf("the Restored row is not the plain path %q:\n%s", file, tail)
		}
	})
	t.Run("a directory of an earlier release", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "old backup")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{backupDump: completeDump, backupVolume: tarBytes(t), backupStatus: statusBody} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		_, tail := restoreOnScreenOf(t, func(u *upgradeHarness) []string { return u.restoreArgs("", "--plaintext-dir", dir) })
		if !strings.Contains(tail, "Restored: plaintext directory "+dir+"\n") || strings.Contains(tail, "--plaintext-dir") {
			t.Errorf("the Restored row does not name the directory as a directory:\n%s", tail)
		}
	})
}

// A restore points out that the canonical may be behind the default branch on GitHub only when the backup names a repository: an instance
// without GitHub has no default branch to be behind.
func TestARestoreOfAnInstanceWithoutRepositoriesDoesNotSayTheCanonicalIsBehindGitHub(t *testing.T) {
	const behind = "may be behind the default branch on GitHub"
	withoutRepos := func(b *backup, _ *backupEngine) {
		b.status = func(context.Context) ([]byte, error) { return []byte(noRepositories), nil }
	}
	t.Run("full screen", func(t *testing.T) {
		page, tail := restoreOnScreenOf(t, func(u *upgradeHarness) []string {
			return u.restoreArgs(aBackup(t, withoutRepos), "--kit", writeTestKit(t, t.TempDir()))
		})
		if strings.Contains(page, behind) || strings.Contains(tail, behind) {
			t.Errorf("a backup that names no repository is told its canonical may be behind GitHub:\n%s\n%s", page, tail)
		}
		if !strings.Contains(page, "Restored") {
			t.Errorf("the summary page lacks its rows:\n%s", page)
		}
	})
	t.Run("plain", func(t *testing.T) {
		u := restoreHarness(t)
		if err := u.restore(aBackup(t, withoutRepos), "--kit", writeTestKit(t, t.TempDir()), "--yes", "--instance", testInstance); err != nil {
			t.Fatal(err)
		}
		if out := u.out.String(); strings.Contains(out, behind) || !strings.Contains(out, "Restored the backup of the instance") {
			t.Errorf("the closing lines of a plain restore:\n%s", out)
		}
	})
}
