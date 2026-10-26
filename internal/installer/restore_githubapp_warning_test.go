package installer

import (
	"strings"
	"testing"
)

// The warning to stop any other instance that uses the same GitHub App is about repositories that two instances would publish and promote for:
// it comes with the same rule as the other GitHub lines of a restore. A backup that holds a repository, or whose status is not recorded (and so
// may hold one), has it; a backup of an instance without GitHub does not.
func TestTheRestoreWarnsAboutAnotherInstanceOfTheGitHubAppOnlyForABackupThatMayHoldRepositories(t *testing.T) {
	const stop = "Stop any other running instance that uses the same GitHub App"
	for _, tc := range []struct {
		name  string
		edit  func(b *backup, e *backupEngine)
		typed string // the instance id of the backup
		stop  bool
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
			if text := run.question(t, 0).text(); strings.Contains(text, stop) != tc.stop {
				t.Errorf("the confirmation page (the other instance named: want %v):\n%s", tc.stop, text)
			}
		})
		t.Run(tc.name+", plain", func(t *testing.T) {
			u := restoreHarness(t)
			if err := u.restore(aBackup(t, tc.edit), "--kit", writeTestKit(t, t.TempDir()), "--yes", "--instance", tc.typed); err != nil {
				t.Fatal(err)
			}
			if out := u.out.String(); strings.Contains(out, stop) != tc.stop {
				t.Errorf("the plain output (the other instance named: want %v):\n%s", tc.stop, out)
			}
		})
	}
}
