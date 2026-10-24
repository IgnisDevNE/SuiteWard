package installer

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/archive"
)

// The commands other than the install explain their dangerous moments on a page too: the restore, the purge, the recovery kit and the
// enrollment again. Each says that the typed answer is a confirmation. Plain output keeps the prompts it had, except where a test says it
// changed on purpose.

// ---- restore ----

// restoreOnScreen is a restore of a backup of testInstance, made on the tier of the backup, on a full screen of w x h: the owner types the
// instance id, then Enter at the summary. The harness's own instance is co-located, so a co-located backup raises no warning about the tier.
func restoreOnScreen(t *testing.T, w, h int, backupTier config.Tier) (*screenRun, *upgradeHarness) {
	t.Helper()
	u := restoreHarness(t)
	run := newScreenRun(w, h, testInstance+"\n", "\n")
	file := aBackup(t, func(b *backup, _ *backupEngine) { b.tier = backupTier })
	if err := Restore(liveContext(t), u.restoreArgs(file, "--kit", writeTestKit(t, t.TempDir())), run.on(u.env())); err != nil {
		t.Fatalf("Restore: %v\n%s", err, run.output())
	}
	return run, u
}

// What is confirmed is on the page, whole, at 100x30: what the backup holds, what runs now, the key of the safety backup, the warnings and
// the question. The instruction for what a restore leaves behind comes after it, in the closing message.
func TestTheRestorePageSaysThatTheTypedIdIsAConfirmationAndShowsWhatIsConfirmed(t *testing.T) {
	run, u := restoreOnScreen(t, termWidth, termHeight, config.TierCoLocated)
	v := run.question(t, 0)
	if !strings.HasPrefix(v.rows[0], "Restore preview") {
		t.Errorf("the page is titled %q, want \"Restore preview\"", v.rows[0])
	}
	mustSay(t, v,
		"This is a confirmation",
		"What was approved, proposed or promoted after the backup is lost",
		"the default branch on GitHub may be ahead of the restored canonical",
		"This instance runs",
		"Safety backup the state that is replaced, encrypted to "+archive.Fingerprint(u.settings().BackupRecipient),
		"Type the instance id "+testInstance+" to confirm",
	)
	if row := v.row("Instance"); !strings.Contains(row, testInstance) {
		t.Errorf("the page does not show the instance of the backup (row %q):\n%s", row, v.dump())
	}
	if strings.Contains(v.text(), trimmed) {
		t.Errorf("the restore page is trimmed at %dx%d:\n%s", termWidth, termHeight, v.dump())
	}
	if strings.Contains(run.output(), "section 3") {
		t.Errorf("the screen points to a section of a document:\n%s", v.dump())
	}
	if strings.Contains(v.text(), "pull request") {
		t.Errorf("the instruction for the canonical is on the confirmation page, where it takes the room of what is confirmed:\n%s", v.dump())
	}
}

// At 80x24 the page keeps the title, the warnings and the question, and still shows which instance is replaced by which backup.
func TestTheRestorePageStillShowsTheInstanceAndTheReleaseAt80x24(t *testing.T) {
	run, _ := restoreOnScreen(t, 80, 24, config.TierCoLocated)
	v := run.question(t, 0)
	if !strings.HasPrefix(v.rows[0], "Restore preview") {
		t.Errorf("the page is titled %q, want \"Restore preview\"", v.rows[0])
	}
	if row := v.row("Instance"); !strings.Contains(row, testInstance) {
		t.Errorf("the page does not show the instance of the backup (row %q):\n%s", row, v.dump())
	}
	if row := v.row("Release"); !strings.Contains(row, "v0.2.0") {
		t.Errorf("the page does not show the release of the backup (row %q):\n%s", row, v.dump())
	}
	mustSay(t, v, "This is a confirmation", "This instance runs", "ahead of the restored canonical", "Type the instance id "+testInstance+" to confirm")
}

// With a warning about the tier too, the explanation is trimmed before the warnings, and never the title or the question.
func TestTheRestorePageKeepsItsTitleItsWarningAndItsQuestionAt80x24(t *testing.T) {
	run, _ := restoreOnScreen(t, 80, 24, config.TierIsolated)
	v := run.question(t, 0)
	if !strings.HasPrefix(v.rows[0], "Restore preview") {
		t.Errorf("the page is titled %q, want \"Restore preview\"", v.rows[0])
	}
	mustSay(t, v, "ahead of the restored canonical", "Type the instance id "+testInstance+" to confirm")
}

// After the restore, what it leaves behind is said once, in a line that stays when the screen has gone: the canonical can be behind the
// default branch, and one corrective pull request brings it level.
func TestTheClosingMessageOfARestoreSaysHowToBringTheCanonicalLevel(t *testing.T) {
	run, _ := restoreOnScreen(t, termWidth, termHeight, config.TierCoLocated)
	tail := run.console.tail()
	for _, want := range []string{"may be behind the default branch on GitHub", "one corrective pull request", "approve the revision its check names", "details: docs/backup.md"} {
		if !strings.Contains(tail, want) {
			t.Errorf("the scrollback after the restore lacks %q:\n%s", want, tail)
		}
	}
	noRepeats(t, tail)
}

// Plain keeps the prompt of a restore: a script that types the id answers it. The warning is the short one and the instruction comes at the
// end, as in the full screen; neither points to a section of a document.
func TestThePlainRestoreKeepsItsPromptAndSaysWhatToDoAboutTheCanonicalAtTheEnd(t *testing.T) {
	u := restoreHarness(t)
	env := u.env()
	env.Term = Terminal{In: strings.NewReader(testInstance + "\n"), Out: &u.out, IsTerminal: true, ReadPassword: noSecret}
	if err := Restore(liveContext(t), u.restoreArgs(aBackup(t, nil), "--kit", writeTestKit(t, t.TempDir())), env); err != nil {
		t.Fatal(err)
	}
	out := u.out.String()
	prompt := "Type the instance id " + testInstance + " to replace the database and the artifacts of this instance with this backup: "
	if !strings.Contains(out, prompt) {
		t.Errorf("the plain prompt changed:\n%s", out)
	}
	warning := "Restoring replaces the database and the artifacts of this instance with the backup's. What was approved, proposed or promoted after the backup is lost, and the default branch on GitHub may be ahead of the restored canonical.\n"
	if !strings.Contains(out, warning) {
		t.Errorf("the plain warning is not the short one:\n%s", out)
	}
	closing := "The restored canonical may be behind the default branch on GitHub: to bring it level, open one corrective pull request from the current default branch (any small change outside the protected files), approve the revision its check names, and merge it (details: docs/backup.md).\n"
	if at := strings.Index(out, closing); at < strings.Index(out, prompt) {
		t.Errorf("the plain output lacks the closing line, or has it before the confirmation:\n%s", out)
	}
	for _, not := range []string{"section 3", "This is a confirmation", "Restore preview"} {
		if strings.Contains(out, not) {
			t.Errorf("the plain output has %q:\n%s", not, out)
		}
	}
}

// The line at the end of an upgrade that says how to go back names the first step of the tier. A restore alone cannot undo an upgrade that
// migrated: the previous release must run first, and on a co-located stack that means removing the stack and its volumes, on an isolated one
// putting the replaced units back.
func TestTheEndOfAnUpgradeSaysHowToGoBackOnItsTier(t *testing.T) {
	const pointer = "the steps: docs/backup.md, Going back after an upgrade"
	t.Run("co-located", func(t *testing.T) {
		u := newUpgradeHarness(t, nil)
		if err := u.upgradeAt(false, "", "--yes"); err != nil {
			t.Fatal(err)
		}
		text, path := u.out.String(), oneArchive(t, u.backups)
		for _, want := range []string{
			"`suiteward uninstall --yes`", "podman volume rm " + VolumeData + " " + VolumeDBData,
			"`suiteward install --image " + oldImage + "`", "`suiteward restore " + path + "` with that kit", "migrations only go forward", pointer,
		} {
			if !strings.Contains(text, want) {
				t.Errorf("the line that says how to go back lacks %q:\n%s", want, text)
			}
		}
		if strings.Contains(text, "section 3") || strings.Contains(text, "systemctl") {
			t.Errorf("the line is not the co-located one:\n%s", text)
		}
	})
	t.Run("isolated", func(t *testing.T) {
		u := newIsoUpgrade(t, nil)
		if err := u.upgrade("--image", testDigest, "--yes"); err != nil {
			t.Fatal(err)
		}
		text, path := u.out.String(), oneArchive(t, u.backups)
		for _, want := range []string{
			unitsKeptBeside(path), "`systemctl --user daemon-reload`", "`systemctl --user stop suiteward.service`",
			"`suiteward restore " + path + "` with that kit", "migrations only go forward", pointer,
		} {
			if !strings.Contains(text, want) {
				t.Errorf("the line that says how to go back lacks %q:\n%s", want, text)
			}
		}
		if strings.Contains(text, "section 3") || strings.Contains(text, "volume rm") {
			t.Errorf("the line is not the isolated one:\n%s", text)
		}
	})
}

// Every message that pointed to a section of a document says what to do, and keeps the document as a reference for the details.
func TestAMessageThatPointedToASectionOfADocumentSaysWhatToDo(t *testing.T) {
	t.Run("a migration that failed after the backup", func(t *testing.T) {
		err := withBackup(migratingError{errors.New("migrate failed")}, "/b/backup.tar.age", "")
		text := err.Error()
		// The steps of the way back are those of the tier: the message points to them instead of saying them in one line.
		for _, want := range []string{"install the previous release and run `suiteward restore /b/backup.tar.age`", "everything after the backup is lost", "the steps: docs/backup.md, Going back after an upgrade"} {
			if !strings.Contains(text, want) {
				t.Errorf("the message lacks %q: %s", want, text)
			}
		}
		if strings.Contains(text, "section 3") {
			t.Errorf("the message points to a section: %s", text)
		}
	})
	t.Run("a backup that cannot be taken because the service does not start", func(t *testing.T) {
		u := newUpgradeHarness(t, nil)
		u.downUntilStarted()
		u.f.rules = append([]rule{{match: "start " + ContainerDB, err: errors.New("no such container")}}, u.f.rules...)
		err := u.upgradeAt(true, "\n", "--yes")
		if err == nil || !strings.Contains(err.Error(), "details: docs/backup.md") || strings.Contains(err.Error(), "section 2") {
			t.Fatalf("got %v, want the manual backup as a sentence, with the document as its details", err)
		}
	})
	t.Run("a release other than the backup's", func(t *testing.T) {
		x := &restoreRun{info: archive.Info{SuitewardVersion: "v0.1.0", ImageDigest: oldDigest}}
		err := x.otherRelease("v0.2.0")
		if err == nil || !strings.Contains(err.Error(), "Install the release of the backup first") || strings.Contains(err.Error(), "section 3") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("a database without the application role", func(t *testing.T) {
		u := restoreHarness(t)
		u.roleExists = false
		err := u.restore(aBackup(t, nil), confirmed(t, t.TempDir())...)
		if err == nil || !strings.Contains(err.Error(), "install the release of the backup first, which creates it") || strings.Contains(err.Error(), "section 3") {
			t.Errorf("got %v", err)
		}
	})
}

// ---- the recovery kit ----

// kitOnScreen is `backup init` of an instance with no key, on a full screen of w x h, whose owner types the word and then Enter.
func kitOnScreen(t *testing.T, w, h int) *screenRun {
	t.Helper()
	u := bareInstance(t)
	kit := filepath.Join(t.TempDir(), "kit.txt")
	run := newScreenRun(w, h, "stored\n", "\n")
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--runtime", "podman", "--connection", "pm", "--kit", kit}
	if err := BackupInit(liveContext(t), args, run.on(u.env())); err != nil {
		t.Fatalf("BackupInit: %v\n%s", err, run.output())
	}
	return run
}

func TestTheRecoveryKitWarningIsACriticalBoxAndTheTypedWordIsAConfirmation(t *testing.T) {
	run := kitOnScreen(t, termWidth, termHeight)
	v := run.question(t, 0)
	if !strings.HasPrefix(v.rows[0], "Keep the recovery kit") {
		t.Errorf("the page is titled %q, want \"Keep the recovery kit\"", v.rows[0])
	}
	mustSay(t, v, "This is a confirmation", "holds the private key that opens every backup", "Without it no backup can be restored", `Type "stored" to confirm that the kit is copied off this machine`)
	if v.row("╭") == "" || v.row("╰") == "" || !strings.HasPrefix(strings.TrimSpace(v.row("holds the private key")), "│") {
		t.Errorf("the kit warning is not in a box:\n%s", v.dump())
	}
	if strings.Contains(v.text(), "private key of") || strings.Contains(run.output(), "AGE-SECRET-KEY") {
		t.Errorf("a page shows the private key:\n%s", v.dump())
	}
}

// A critical warning is a Warn in plain mode: the text, as it was, and the prompt that follows.
func TestThePlainRecoveryKitWarningAndPromptAreWhatTheyWere(t *testing.T) {
	u := bareInstance(t)
	kit := filepath.Join(t.TempDir(), "kit.txt")
	if err := u.backupInitOnTerminal("stored\n", "--kit", kit); err != nil {
		t.Fatal(err)
	}
	out := u.out.String()
	for _, want := range []string{fmt.Sprintf(kitWarning+"\n", kit), `Type "stored" once the kit is copied off this machine: `} {
		if !strings.Contains(out, want) {
			t.Errorf("the plain output lacks %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"This is a confirmation", "Keep the recovery kit", "╭"} {
		if strings.Contains(out, not) {
			t.Errorf("the plain output has %q:\n%s", not, out)
		}
	}
}

// ---- uninstall ----

func uninstallOnScreen(t *testing.T, answers []string, flags ...string) (*screenRun, error) {
	t.Helper()
	h := purgeHarness(t)
	run := newScreenRun(termWidth, termHeight, answers...)
	args := append([]string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm"}, flags...)
	return run, Uninstall(liveContext(t), args, run.on(h.newEnv()))
}

func TestThePurgePageSaysWhatItDeletesInABoxAndThatTheTypedIdIsAConfirmation(t *testing.T) {
	run, err := uninstallOnScreen(t, []string{testInstance + "\n", "\n"}, "--purge")
	if err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, run.output())
	}
	v := run.question(t, 0)
	if !strings.HasPrefix(v.rows[0], "Remove everything of this instance") {
		t.Errorf("the page is titled %q, want \"Remove everything of this instance\"", v.rows[0])
	}
	mustSay(t, v, "Nothing is removed until you confirm", testInstance, "this deletes the database and the artifacts", "Type the instance id "+testInstance+" to confirm")
	if v.row("╭") == "" || !strings.HasPrefix(strings.TrimSpace(v.row("this deletes the database")), "│") {
		t.Errorf("what the purge deletes is not in a box:\n%s", v.dump())
	}
}

func TestTheUninstallThatKeepsTheDataAsksAYesOrNoAfterItsPage(t *testing.T) {
	run, err := uninstallOnScreen(t, []string{"1\n", "\n"})
	if err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, run.output())
	}
	v := run.question(t, 0)
	if !strings.HasPrefix(v.rows[0], "Remove the containers") {
		t.Errorf("the page is titled %q, want \"Remove the containers\"", v.rows[0])
	}
	mustSay(t, v, testInstance, "The data stays", "Yes", "No")
	if strings.Contains(v.text(), "[y/N]") {
		t.Errorf("the question is typed:\n%s", v.dump())
	}
}

func TestThePlainPurgeKeepsItsPromptAndItsLines(t *testing.T) {
	h := purgeHarness(t)
	if err := h.uninstallOn(true, testInstance+"\n", "--purge"); err != nil {
		t.Fatal(err)
	}
	out := h.out.String()
	for _, want := range []string{
		"This is the instance " + testInstance + " on ",
		"Remove the containers, the volumes (this deletes the database and the artifacts), the secrets, the network, the MCP token copy and the settings file?\n",
		"Type the instance id " + testInstance + " to remove it: ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plain output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Nothing is removed until you confirm") || strings.Contains(out, "╭") {
		t.Errorf("the plain output carries the page:\n%s", out)
	}
}

// ---- enroll ----

func TestTheEnrollAgainPageSaysWhatItReplacesAndThatTheTypedWordIsAConfirmation(t *testing.T) {
	h := enrollHarness(t, statusEnrolled)
	run := newScreenRun(termWidth, termHeight, "enroll\n", "\n")
	var stderr bytes.Buffer
	if code := Main(liveContext(t), []string{"enroll", "--settings", h.settingsPath(), "--owner", "alice"}, run.on(h.newEnv()), &stderr); code != 0 {
		t.Fatalf("exit %d: %s\n%s", code, stderr.String(), run.output())
	}
	v := run.question(t, 0)
	if !strings.HasPrefix(v.rows[0], "Enroll the owner again") {
		t.Errorf("the page is titled %q, want \"Enroll the owner again\"", v.rows[0])
	}
	mustSay(t, v, "An owner (bob) is already enrolled", "the old authenticator entry stops working", "you scan a new one", "This is a confirmation", `Type "enroll" to confirm`)
}

func TestThePlainEnrollAgainPromptIsWhatItWas(t *testing.T) {
	h := enrollHarness(t, statusEnrolled)
	if code, stderr := h.enroll("enroll\n", "--owner", "alice"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	want := "An owner (bob) is already enrolled. Enrolling again replaces the owner bob with alice and the TOTP secret: the old authenticator entry stops working, and you scan a new one.\nType \"enroll\" to replace it: "
	if !strings.Contains(h.out.String(), want) {
		t.Errorf("the plain prompt changed:\n%s", h.out.String())
	}
}

// ---- the tier of a host with no settings file ----

// The message for enroll is pinned by TestEnrollOnLinuxWithoutSettingsNeedsTheTier. The other commands keep offering both tiers.
func TestTheOtherCommandsOfAHostWithNoSettingsStillOfferBothTiers(t *testing.T) {
	for name, args := range map[string][]string{"install --upgrade": {"install", "--upgrade", "--yes"}, "backup": {"backup"}} {
		t.Run(name, func(t *testing.T) {
			useDefaultConfigDir(t)
			h := newHarness(t, "linux")
			var stderr bytes.Buffer
			code := Main(liveContext(t), args, h.newEnv(), &stderr)
			if code != 1 || !strings.Contains(stderr.String(), "pass --tier isolated (as the service account) or --tier co-located") {
				t.Fatalf("exit %d, stderr %q", code, stderr.String())
			}
			h.mustNotMutate()
		})
	}
}

// ---- the hidden paste of the recovery key ----

// Without --kit a terminal is asked to paste the private key. The page says what to paste and that it is hidden; the key itself is never drawn.
func TestTheRecoveryKeyOfARestoreIsAskedOnAPageThatSaysWhatToPaste(t *testing.T) {
	_, identity := testRecoveryKey(t)
	u := restoreHarness(t)
	run := newScreenRun(termWidth, termHeight, testInstance+"\n", "\n")
	env := run.on(u.env())
	var page frameView
	env.Term.ReadPassword = func() ([]byte, error) {
		page = viewOf(t, run.output(), termWidth, termHeight)
		return []byte(identity), nil
	}
	if err := Restore(liveContext(t), u.restoreArgs(aBackup(t, nil)), env); err != nil {
		t.Fatalf("Restore: %v\n%s", err, run.output())
	}
	if !strings.HasPrefix(page.rows[0], "Open the backup") {
		t.Errorf("the page of the hidden paste is titled %q, want \"Open the backup\"", page.rows[0])
	}
	mustSay(t, page, "recovery key of its instance", "last line of the recovery kit", "The input is hidden", "--kit PATH", "Recovery key: the last line of your recovery kit")
	noIdentityIn(t, "the screen", run.output())
}
