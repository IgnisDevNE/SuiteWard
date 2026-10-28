package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noRepositories is the status of a backup of an instance that has none (GitHub off): no "github" list.
var noRepositories = strings.Replace(statusBody, `,"github":[{"repo":"octo/app","integrity":"passed"}]`, ``, 1)

// withoutRepositories is a backup of an instance that has no repository.
func withoutRepositories(b *backup, _ *backupEngine) {
	b.status = func(context.Context) ([]byte, error) { return []byte(noRepositories), nil }
}

// notRecorded is a backup of an instance that did not answer when it was taken: its status says only that, so whether the instance has a
// repository is not known, and its instance id is "unknown".
func notRecorded(b *backup, _ *backupEngine) {
	b.tolerateStatus = true
	b.status = func(context.Context) ([]byte, error) { return nil, errors.New("the service did not answer") }
}

// restoreOnScreenOf restores into the harness on a full screen of 100x30, where the person types the instance id typed, and gives the
// page the summary drew and what stayed after it.
func restoreOnScreenOf(t *testing.T, typed string, args func(u *upgradeHarness) []string) (page, scrollback string) {
	t.Helper()
	u := restoreHarness(t)
	run := newScreenRun(termWidth, termHeight, typed+"\n", "\n")
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
		_, tail := restoreOnScreenOf(t, testInstance, func(u *upgradeHarness) []string {
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
		_, tail := restoreOnScreenOf(t, testInstance, func(u *upgradeHarness) []string { return u.restoreArgs("", "--plaintext-dir", dir) })
		if !strings.Contains(tail, "Restored: plaintext directory "+dir+"\n") || strings.Contains(tail, "--plaintext-dir") {
			t.Errorf("the Restored row does not name the directory as a directory:\n%s", tail)
		}
	})
}

// A restore points out that the canonical may be behind the default branch on GitHub only when the backup names a repository: an instance
// without GitHub has no default branch to be behind.
func TestARestoreOfAnInstanceWithoutRepositoriesDoesNotSayTheCanonicalIsBehindGitHub(t *testing.T) {
	const behind = "may be behind the default branch on GitHub"
	t.Run("full screen", func(t *testing.T) {
		page, tail := restoreOnScreenOf(t, testInstance, func(u *upgradeHarness) []string {
			return u.restoreArgs(aBackup(t, withoutRepositories), "--kit", writeTestKit(t, t.TempDir()))
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
		if err := u.restore(aBackup(t, withoutRepositories), "--kit", writeTestKit(t, t.TempDir()), "--yes", "--instance", testInstance); err != nil {
			t.Fatal(err)
		}
		if out := u.out.String(); strings.Contains(out, behind) || !strings.Contains(out, "Restored the backup of the instance") {
			t.Errorf("the closing lines of a plain restore:\n%s", out)
		}
	})
}

// A backup whose status is not recorded may hold repositories, so it keeps the lines about GitHub: when it is not known, the person is told.
func TestARestoreOfABackupWhoseStatusIsNotRecordedKeepsTheCanonicalBehindLine(t *testing.T) {
	const behind = "may be behind the default branch on GitHub"
	t.Run("full screen", func(t *testing.T) {
		page, tail := restoreOnScreenOf(t, unknownValue, func(u *upgradeHarness) []string {
			return u.restoreArgs(aBackup(t, notRecorded), "--kit", writeTestKit(t, t.TempDir()))
		})
		if !strings.Contains(page, behind) || !strings.Contains(tail, behind) {
			t.Errorf("the line is missing from the summary page or the scrollback:\n%s\n%s", page, tail)
		}
	})
	t.Run("plain", func(t *testing.T) {
		u := restoreHarness(t)
		if err := u.restore(aBackup(t, notRecorded), "--kit", writeTestKit(t, t.TempDir()), "--yes", "--instance", unknownValue); err != nil {
			t.Fatal(err)
		}
		if out := u.out.String(); !strings.Contains(out, behind) {
			t.Errorf("the closing lines of a plain restore lack the canonical-behind line:\n%s", out)
		}
	})
}

// The warning before the restore says that the default branch on GitHub may be ahead of the restored canonical for a backup that holds a
// repository or may hold one (its status is not recorded), and only for those: an instance without GitHub has no default branch to be ahead.
func TestTheRestoreWarningNamesTheDefaultBranchOnlyForABackupThatMayHoldRepositories(t *testing.T) {
	const (
		replaces = "What was approved, proposed or promoted after the backup is lost"
		ahead    = "the default branch on GitHub may be ahead of the restored approved version"
	)
	for _, tc := range []struct {
		name  string
		edit  func(b *backup, e *backupEngine)
		typed string // the instance id of the backup
		ahead bool
	}{
		{"a repository", nil, testInstance, true},
		{"no repository", withoutRepositories, testInstance, false},
		{"a status that is not recorded", notRecorded, unknownValue, true},
	} {
		t.Run(tc.name+", full screen", func(t *testing.T) {
			u := restoreHarness(t)
			run := newScreenRun(termWidth, termHeight, tc.typed+"\n", "\n")
			if err := Restore(liveContext(t), u.restoreArgs(aBackup(t, tc.edit), "--kit", writeTestKit(t, t.TempDir())), run.on(u.env())); err != nil {
				t.Fatalf("Restore: %v\n%s", err, run.output())
			}
			if text := run.question(t, 0).text(); !strings.Contains(text, replaces) || strings.Contains(text, ahead) != tc.ahead {
				t.Errorf("the confirmation page (default branch named: want %v):\n%s", tc.ahead, text)
			}
		})
		t.Run(tc.name+", plain", func(t *testing.T) {
			u := restoreHarness(t)
			if err := u.restore(aBackup(t, tc.edit), "--kit", writeTestKit(t, t.TempDir()), "--yes", "--instance", tc.typed); err != nil {
				t.Fatal(err)
			}
			if out := u.out.String(); !strings.Contains(out, replaces) || strings.Contains(out, ahead) != tc.ahead {
				t.Errorf("the plain output (default branch named: want %v):\n%s", tc.ahead, out)
			}
		})
	}
}

// The credentials are not in a backup, and the executor's token is one of them: a restore leaves it as it was, so the executor keeps working.
func TestARestoreSaysThatTheExecutorTokenIsAsItWas(t *testing.T) {
	u := restoreHarness(t)
	if err := u.restore(aBackup(t, withoutRepositories), "--kit", writeTestKit(t, t.TempDir()), "--yes", "--instance", testInstance); err != nil {
		t.Fatal(err)
	}
	if out := u.out.String(); !strings.Contains(out, "the executor token") {
		t.Errorf("the closing lines of a restore lack the executor's token among the credentials that are as they were:\n%s", out)
	}
}
