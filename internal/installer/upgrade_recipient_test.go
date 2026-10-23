package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/archive"
)

// An upgrade needs the key before it changes anything, and never backs up in plaintext in its place: from the settings, or from
// --backup-recipient and the answers file where nobody can be asked, or from a new key that a terminal is offered.

func noRecipient(s *Settings) { s.BackupRecipient = "" }

func TestUpgradeColocatedWithoutAKeyAndWithoutATerminalRefusesBeforeAnyChange(t *testing.T) {
	u := newUpgradeHarness(t, noRecipient)
	u.downUntilStarted() // a stopped service must not be started for an upgrade that cannot be backed up
	err := u.upgradeAt(false, "", "--yes")
	if err == nil {
		t.Fatal("the upgrade went on without a key to encrypt the backup to")
	}
	for _, want := range []string{"suiteward backup init", "--backup-recipient", "backup_recipient"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
	u.noChanges()
	if u.startCalls(ContainerDB) != 0 || u.startCalls(ContainerServe) != 0 {
		t.Error("the service was started for an upgrade that was refused")
	}
	if files, _ := filepath.Glob(filepath.Join(u.backups, "*")); len(files) != 0 {
		t.Errorf("a backup was made: %v", files)
	}
}

func TestUpgradeColocatedWithNothingToUpgradeDoesNotNeedAKey(t *testing.T) {
	u := newUpgradeHarness(t, noRecipient)
	if err := u.upgradeAt(false, "", "--yes", "--image", oldImage); err != nil {
		t.Fatalf("an upgrade that has nothing to do needs no backup: %v", err)
	}
	if !strings.Contains(u.out.String(), "Nothing to upgrade") {
		t.Errorf("the output does not say so:\n%s", u.out.String())
	}
}

func TestUpgradeColocatedOffersANewKeyOnATerminal(t *testing.T) {
	u := newUpgradeHarness(t, noRecipient)
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := u.upgradeAt(true, "y\nstored\n", "--yes"); err != nil {
		t.Fatal(err)
	}
	kit := readFile(t, filepath.Join(cwd, "suiteward-recovery-kit-"+testInstance+".txt"))
	id, err := archive.ParseIdentity(kit)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.settings().BackupRecipient; got != id.Recipient().String() {
		t.Errorf("the settings do not hold the new key: %q", archive.Fingerprint(got))
	}
	if _, members := openBackupWith(t, u.backup(), kit); members["suiteward.sql"] != dumpText {
		t.Errorf("the backup does not open with the key that was made: %v", members)
	}
	if got := u.settings().Image; got != testImage {
		t.Errorf("the upgrade did not go on: the settings keep %s", got)
	}
	noIdentityIn(t, "the output", u.out.String())
	noIdentityInCalls(t, u.f.calls)
}

func TestUpgradeColocatedANoToTheNewKeyRefusesAndChangesNothing(t *testing.T) {
	u := newUpgradeHarness(t, noRecipient)
	t.Chdir(t.TempDir())
	err := u.upgradeAt(true, "n\n", "--yes")
	if err == nil || !strings.Contains(err.Error(), "suiteward backup init") {
		t.Fatalf("got %v, want a refusal that names `suiteward backup init`", err)
	}
	u.noChanges()
	if files, _ := filepath.Glob(filepath.Join(u.backups, "*")); len(files) != 0 || u.settings().BackupRecipient != "" {
		t.Errorf("a refused offer changed something: %v", files)
	}
}

func TestUpgradeColocatedTakesTheKeyFromTheFlagAndStoresIt(t *testing.T) {
	recipient, identity := testRecoveryKey(t)
	u := newUpgradeHarness(t, noRecipient)
	if err := u.upgradeAt(false, "", "--yes", "--backup-recipient", recipient); err != nil {
		t.Fatal(err)
	}
	if _, members := openBackupWith(t, u.backup(), identity); members["suiteward.sql"] != dumpText {
		t.Errorf("the backup is not encrypted to the key of the flag: %v", members)
	}
	if got := u.settings().BackupRecipient; got != recipient {
		t.Errorf("the settings hold %q, want the key of the flag stored", archive.Fingerprint(got))
	}
}

func TestUpgradeColocatedTakesTheKeyFromTheAnswersFile(t *testing.T) {
	recipient, identity := testRecoveryKey(t)
	u := newUpgradeHarness(t, noRecipient)
	answers := filepath.Join(t.TempDir(), "answers.yaml")
	if err := os.WriteFile(answers, []byte("backup_recipient: "+recipient+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := u.upgradeAt(false, "", "--yes", "--answers", answers); err != nil {
		t.Fatal(err)
	}
	if _, members := openBackupWith(t, u.backup(), identity); members["suiteward.sql"] != dumpText {
		t.Errorf("the backup is not encrypted to the key of the answers file: %v", members)
	}
	if got := u.settings().BackupRecipient; got != recipient {
		t.Errorf("the settings hold %q", archive.Fingerprint(got))
	}
}

func TestUpgradeRefusesAKeyThatIsNotPostQuantumOrThatReplacesTheOneInUse(t *testing.T) {
	another, _, err := archive.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		edit func(s *Settings)
		flag string
		want string
	}{
		"a classic key":                {noRecipient, classicRecipient(t), "post-quantum"},
		"a key that is not a key":      {noRecipient, "hello", "post-quantum"},
		"another key than the one set": {nil, another, "--replace"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			u := newUpgradeHarness(t, tc.edit)
			err := u.upgradeAt(false, "", "--yes", "--backup-recipient", tc.flag)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want a refusal that says %q", err, tc.want)
			}
			if strings.Contains(err.Error(), tc.flag) {
				t.Errorf("the error repeats the key: %v", err)
			}
			u.noChanges()
		})
	}
}

func TestUpgradePlanNamesTheKeyTheBackupIsEncryptedTo(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	if err := u.upgradeAt(false, "", "--yes"); err != nil {
		t.Fatal(err)
	}
	recipient, _ := testRecoveryKey(t)
	if !strings.Contains(u.out.String(), archive.Fingerprint(recipient)) {
		t.Errorf("the plan does not name the key:\n%s", u.out.String())
	}
}

func TestUpgradeIsolatedKeyFlowsAreTheSame(t *testing.T) {
	recipient, identity := testRecoveryKey(t)
	t.Run("no key and no terminal", func(t *testing.T) {
		u := newIsoUpgrade(t, nil)
		u.withoutRecipient()
		before := u.units()
		err := u.upgradeAt(false, "", nil, "--yes")
		if err == nil || !strings.Contains(err.Error(), "suiteward backup init") || !strings.Contains(err.Error(), "--backup-recipient") {
			t.Fatalf("got %v", err)
		}
		u.untouched(before)
		if files, _ := filepath.Glob(filepath.Join(u.backups, "*")); len(files) != 0 {
			t.Errorf("a backup was made: %v", files)
		}
	})
	t.Run("the flag", func(t *testing.T) {
		u := newIsoUpgrade(t, nil)
		u.withoutRecipient()
		if err := u.upgradeAt(false, "", nil, "--yes", "--backup-recipient", recipient); err != nil {
			t.Fatal(err)
		}
		if _, members := openBackupWith(t, oneArchive(t, u.backups), identity); members["suiteward.sql"] != dumpText {
			t.Errorf("the backup is not encrypted to the key of the flag: %v", members)
		}
		if got, err := LoadSettings(settingsFile(u.home)); err != nil || got.BackupRecipient != recipient {
			t.Errorf("the key of the flag was not stored: %v", err)
		}
	})
	t.Run("a new key on a terminal", func(t *testing.T) {
		u := newIsoUpgrade(t, nil)
		u.withoutRecipient()
		cwd := t.TempDir()
		t.Chdir(cwd)
		if err := u.upgradeAt(true, "y\nstored\n", nil, "--yes"); err != nil {
			t.Fatal(err)
		}
		kit := readFile(t, filepath.Join(cwd, "suiteward-recovery-kit-"+testInstance+".txt"))
		if _, members := openBackupWith(t, oneArchive(t, u.backups), kit); members["suiteward.sql"] != dumpText {
			t.Errorf("the backup does not open with the key that was made: %v", members)
		}
		noIdentityIn(t, "the output", u.out.String())
	})
}
