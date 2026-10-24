package installer

import (
	"errors"
	"strings"
	"testing"
)

// The two questions that default to yes (make the recovery key, start the service that is down) are a choice between Yes and No on a full
// screen, with Yes selected, as every other question that has one. A plain run keeps the typed "[Y/n]" that a script answers.

func upgradeArgs(u *upgradeHarness) []string {
	return []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", testImage, "--yes"}
}

func TestTheQuestionsThatDefaultToYesAreAChoiceOnAFullScreen(t *testing.T) {
	t.Run("the recovery key", func(t *testing.T) {
		u := newUpgradeHarness(t, noRecipient)
		run := newScreenRun(termWidth, termHeight, "2\n") // No
		err := Install(liveContext(t), upgradeArgs(u), run.on(u.env()))
		if err == nil || !strings.Contains(err.Error(), "suiteward backup init") {
			t.Fatalf("got %v, want the refusal that names `suiteward backup init`", err)
		}
		v := run.question(t, 0)
		mustSay(t, v, "No recovery key is set", "Make one now")
		if !strings.Contains(v.row("1) Yes"), "(default)") || v.row("2) No") == "" || strings.Contains(v.text(), "[Y/n]") {
			t.Errorf("the question is not a choice between Yes, the default, and No:\n%s", v.dump())
		}
		u.noChanges()
	})
	t.Run("the service that is down", func(t *testing.T) {
		u := newUpgradeHarness(t, nil)
		u.downUntilStarted()
		run := newScreenRun(termWidth, termHeight, "\n", "\n") // Enter takes Yes, then the summary
		if err := Install(liveContext(t), upgradeArgs(u), run.on(u.env())); err != nil {
			t.Fatal(err)
		}
		v := run.question(t, 0)
		mustSay(t, v, "The service is not running", "Start the installed service now?")
		if !strings.Contains(v.row("1) Yes"), "(default)") || v.row("2) No") == "" || strings.Contains(v.text(), "[Y/n]") {
			t.Errorf("the question is not a choice between Yes, the default, and No:\n%s", v.dump())
		}
		if got := u.startCalls(ContainerServe); got != 1 {
			t.Errorf("Enter on Yes started the service %d times, want once", got)
		}
	})
	t.Run("the service that is down, answered No", func(t *testing.T) {
		u := newUpgradeHarness(t, nil)
		u.downUntilStarted()
		run := newScreenRun(termWidth, termHeight, "2\n")
		err := Install(liveContext(t), upgradeArgs(u), run.on(u.env()))
		if err == nil || !strings.Contains(err.Error(), "start suiteward-db") {
			t.Fatalf("got %v, want the refusal that names the commands that start the service", err)
		}
		if u.startCalls(ContainerDB) != 0 || u.startCalls(ContainerServe) != 0 {
			t.Error("the service was started although the answer was No")
		}
		u.noChanges()
	})
}

// On the isolated tier the migration that failed after the backup is undone by putting the unit files back, not by installing the previous
// release: the instructions are those of docs/backup.md, as the closing line of the upgrade gives them.
func TestAFailedMigrationOnTheIsolatedTierSaysToPutTheUnitsBack(t *testing.T) {
	const path, units = "/b/suiteward-backup.tar.age", "/h/.config/containers/systemd"
	err := withBackup("suiteward", migratingError{errors.New("migrate failed")}, path, units, " --tier isolated")
	text := err.Error()
	if !strings.Contains(text, goBackIsolated("suiteward", path, units, " --tier isolated")) {
		t.Errorf("the message lacks the way back of the isolated tier:\n%s", text)
	}
	if strings.Contains(text, "install the previous release") {
		t.Errorf("the message sends an isolated host to install the previous release:\n%s", text)
	}
	if !strings.Contains(text, "migrate failed") {
		t.Errorf("the message lost the error: %s", text)
	}
}
