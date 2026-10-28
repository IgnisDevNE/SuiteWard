package installer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/archive"
)

// noIdentityInCalls fails when the private key is in the arguments or the standard input of any command.
func noIdentityInCalls(t *testing.T, calls []call) {
	t.Helper()
	for _, c := range calls {
		noIdentityIn(t, "the argument list of "+c.name+" "+c.line(), c.line())
		noIdentityIn(t, "the standard input of "+c.name+" "+c.line(), c.stdin)
	}
}

// bareInstance is a co-located stack that runs and has no backup recipient yet, as an install before this release left it.
func bareInstance(t *testing.T) *upgradeHarness {
	t.Helper()
	return newUpgradeHarness(t, func(s *Settings) { s.BackupRecipient = "" })
}

// backupInit runs `backup init` on the stack of the harness; the input is not a terminal.
func (u *upgradeHarness) backupInit(args ...string) error {
	u.t.Helper()
	all := append([]string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--runtime", "podman", "--connection", "pm"}, args...)
	return BackupInit(context.Background(), all, u.env())
}

// backupInitOnTerminal runs it on a terminal that answers with input.
func (u *upgradeHarness) backupInitOnTerminal(input string, args ...string) error {
	u.t.Helper()
	env := u.env()
	env.Term = Terminal{In: strings.NewReader(input), Out: &u.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}
	all := append([]string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--runtime", "podman", "--connection", "pm"}, args...)
	return BackupInit(context.Background(), all, env)
}

func TestSettingsKeepTheBackupRecipient(t *testing.T) {
	recipient, _ := testRecoveryKey(t)
	s := DefaultSettings()
	s.BackupRecipient = recipient
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(path)
	if err != nil || got.BackupRecipient != recipient {
		t.Fatalf("LoadSettings = %q, %v; want the recipient %q", archive.Fingerprint(got.BackupRecipient), err, archive.Fingerprint(recipient))
	}
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(readFile(t, path)), &raw); err != nil || raw["backup_recipient"] != recipient {
		t.Errorf("the file keeps the recipient under backup_recipient: %v, %v", raw, err)
	}
}

func TestSettingsRefuseARecipientThatIsNotPostQuantum(t *testing.T) {
	for name, recipient := range map[string]string{"a classic age key": classicRecipient(t), "not a key": "hello", "a private key": "AGE-SECRET-KEY-PQ-1QQQQQQ"} {
		t.Run(name, func(t *testing.T) {
			s := DefaultSettings()
			s.BackupRecipient = recipient
			err := s.Validate()
			if err == nil || !strings.Contains(err.Error(), "backup_recipient") {
				t.Fatalf("Validate = %v, want a refusal that names backup_recipient", err)
			}
			if strings.Contains(err.Error(), recipient) {
				t.Errorf("the error repeats the value: %v", err)
			}
		})
	}
}

// A re-run of the install rebuilds the settings of a Podman stack from its containers, which know nothing of the recipient.
func TestInstallKeepsTheBackupRecipientOnAColocatedPodmanStack(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--runtime", "podman", "--connection", "pm", "--image", oldImage}
	if err := Install(context.Background(), args, u.env()); err != nil {
		t.Fatal(err)
	}
	want, _ := testRecoveryKey(t)
	if got := u.settings().BackupRecipient; got != want {
		t.Errorf("the settings keep the recipient %q after a re-run, want %q", archive.Fingerprint(got), archive.Fingerprint(want))
	}
}

func TestInstallKeepsTheBackupRecipientOnTheIsolatedTier(t *testing.T) {
	home := t.TempDir()
	installedStackUnits(t, home)
	writeAccountEnvFile(t, home, flowAppEnvFile)
	s := isolatedSettings()
	s.BackupRecipient, _ = testRecoveryKey(t)
	s.HTTPPort, s.MCPPort, s.JobTimeout, s.Image = 18081, 18091, "3m", oldImage
	if err := SaveSettings(settingsFile(home), s); err != nil {
		t.Fatal(err)
	}
	f := newIsoFake(home)
	f.secrets[SecretGitHubAppKey] = "existing"
	f.serviceRunning, f.everActive, f.listening = true, true, []int{18081, 18091}
	if _, err := runIsolated(t, f, nil, ""); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(settingsFile(home))
	if err != nil || got.BackupRecipient != s.BackupRecipient {
		t.Errorf("the settings keep %q after a re-run, %v; want %q", archive.Fingerprint(got.BackupRecipient), err, archive.Fingerprint(s.BackupRecipient))
	}
}

func TestBackupInitWritesTheKitAndKeepsOnlyTheRecipientInTheSettings(t *testing.T) {
	u := bareInstance(t)
	kit := filepath.Join(t.TempDir(), "kit.txt")
	if err := u.backupInit("--kit", kit, "--yes"); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, kit)
	id, err := archive.ParseIdentity(text)
	if err != nil {
		t.Fatalf("the kit is not a recovery kit: %v", err)
	}
	recipient := id.Recipient().String()
	got := u.settings()
	if got.BackupRecipient != recipient {
		t.Errorf("the settings hold %q, want the recipient of the kit %q", archive.Fingerprint(got.BackupRecipient), archive.Fingerprint(recipient))
	}
	if got.HTTPPort != u.stack.HTTPPort || got.MCPPort != u.stack.MCPPort || got.Image != oldImage || got.Connection != "pm" {
		t.Errorf("the rest of the settings changed: %+v", got)
	}
	if strings.Contains(readFile(t, u.settingsPath()), "AGE-SECRET-KEY") {
		t.Error("the private key is in the settings file")
	}
	for _, want := range []string{testInstance, archive.Fingerprint(recipient)} {
		if !strings.Contains(text, want) {
			t.Errorf("the kit lacks %q:\n%s", want, text)
		}
	}
	out := u.out.String()
	for _, want := range []string{archive.Fingerprint(recipient), kit} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	// The identity is in the kit and nowhere else.
	noIdentityIn(t, "the output", out)
	noIdentityInCalls(t, u.f.calls)
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(kit); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("the kit has mode %v, %v; want 0600", info.Mode().Perm(), err)
		}
	}
}

func TestBackupInitAsksTheServiceForTheInstanceThatNamesTheKit(t *testing.T) {
	u := bareInstance(t)
	dir := t.TempDir()
	t.Chdir(dir)
	if err := u.backupInit("--yes"); err != nil {
		t.Fatal(err)
	}
	kit := filepath.Join(dir, "suiteward-recovery-kit-"+testInstance+".txt")
	if _, err := archive.ParseIdentity(readFile(t, kit)); err != nil {
		t.Fatalf("the default kit is not in the current directory as %s: %v", kit, err)
	}
	if !strings.Contains(u.out.String(), kit) {
		t.Errorf("the output does not name the kit %s:\n%s", kit, u.out.String())
	}
}

func TestBackupInitNeverOverwritesAKit(t *testing.T) {
	u := bareInstance(t)
	kit := filepath.Join(t.TempDir(), "kit.txt")
	if err := os.WriteFile(kit, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := u.backupInit("--kit", kit, "--yes")
	if err == nil || !strings.Contains(err.Error(), kit) {
		t.Fatalf("got %v, want a refusal that names the file", err)
	}
	if got := readFile(t, kit); got != "mine" {
		t.Errorf("the file was overwritten: %q", got)
	}
	if got := u.settings().BackupRecipient; got != "" {
		t.Errorf("a recipient was stored although the kit was not written: %q", archive.Fingerprint(got))
	}
}

// With no terminal nobody can type the word, so the flag stands for it and the warning is still printed.
func TestBackupInitWithoutATerminalNeedsYesAndStillWarns(t *testing.T) {
	u := bareInstance(t)
	kit := filepath.Join(t.TempDir(), "kit.txt")
	err := u.backupInit("--kit", kit)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("got %v, want a refusal that names --yes", err)
	}
	if _, statErr := os.Stat(kit); statErr == nil {
		t.Error("a kit was left behind by a run that was not confirmed")
	}
	if got := u.settings().BackupRecipient; got != "" {
		t.Errorf("a recipient was stored without the confirmation: %q", archive.Fingerprint(got))
	}

	u = bareInstance(t)
	if err := u.backupInit("--kit", filepath.Join(t.TempDir(), "kit.txt"), "--yes"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.out.String(), "off this computer") {
		t.Errorf("--yes does not hide the warning about keeping the kit off the computer:\n%s", u.out.String())
	}
}

func TestBackupInitOnATerminalNeedsTheOwnerToTypeStored(t *testing.T) {
	u := bareInstance(t)
	kit := filepath.Join(t.TempDir(), "kit.txt")
	if err := u.backupInitOnTerminal("stored\n", "--kit", kit); err != nil {
		t.Fatal(err)
	}
	if u.settings().BackupRecipient == "" {
		t.Error("the recipient was not stored after the confirmation")
	}

	// --yes does not type the word on a terminal; anything else, and an input that ends, cancel and leave nothing behind.
	for name, input := range map[string]string{"another word": "yes\n", "no answer": "", "an empty answer": "\n"} {
		t.Run(name, func(t *testing.T) {
			u := bareInstance(t)
			kit := filepath.Join(t.TempDir(), "kit.txt")
			err := u.backupInitOnTerminal(input, "--kit", kit, "--yes")
			if err == nil || !strings.Contains(err.Error(), "stored") {
				t.Fatalf("got %v, want a refusal that names the word", err)
			}
			if _, statErr := os.Stat(kit); statErr == nil {
				t.Error("the kit was kept although the owner did not confirm it: a re-run could not write it again")
			}
			if got := u.settings().BackupRecipient; got != "" {
				t.Errorf("a recipient was stored without the confirmation: %q", archive.Fingerprint(got))
			}
		})
	}
}

func TestBackupInitRefusesToReplaceTheRecipientWithoutTheFlag(t *testing.T) {
	u := newUpgradeHarness(t, nil) // the instance has a recipient
	old, _ := testRecoveryKey(t)
	kit := filepath.Join(t.TempDir(), "kit.txt")
	err := u.backupInit("--kit", kit, "--yes")
	if err == nil || !strings.Contains(err.Error(), "--replace") || !strings.Contains(err.Error(), archive.Fingerprint(old)) {
		t.Fatalf("got %v, want a refusal that names --replace and the fingerprint of the key in use", err)
	}
	if _, statErr := os.Stat(kit); statErr == nil {
		t.Error("a kit was written for a key that was refused")
	}
	if got := u.settings().BackupRecipient; got != old {
		t.Errorf("the recipient changed to %q", archive.Fingerprint(got))
	}

	if err := u.backupInit("--kit", kit, "--yes", "--replace"); err != nil {
		t.Fatal(err)
	}
	if got := u.settings().BackupRecipient; got == old || got == "" {
		t.Errorf("--replace kept the old recipient: %q", archive.Fingerprint(got))
	}
	for _, want := range []string{"still need", archive.Fingerprint(old)} {
		if !strings.Contains(u.out.String(), want) {
			t.Errorf("the output does not warn that the old backups need the old kit (%q):\n%s", want, u.out.String())
		}
	}
}

func TestBackupInitNeedsAServiceThatAnswersAndSettingsThatExist(t *testing.T) {
	u := newUpgradeHarness(t, func(s *Settings) { s.HTTPPort, s.MCPPort, s.BackupRecipient = 1, 2, "" }) // nothing listens there
	kit := filepath.Join(t.TempDir(), "kit.txt")
	err := u.backupInit("--kit", kit, "--yes")
	if err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("got %v, want a refusal that says the instance id comes from /status", err)
	}
	if _, statErr := os.Stat(kit); statErr == nil {
		t.Error("a kit was written for an instance that could not be named")
	}

	u = bareInstance(t)
	if err := os.Remove(u.settingsPath()); err != nil {
		t.Fatal(err)
	}
	err = u.backupInit("--kit", kit, "--yes")
	if err == nil || !strings.Contains(err.Error(), "settings") {
		t.Fatalf("got %v, want a refusal that names the missing settings", err)
	}
	if _, statErr := os.Stat(kit); statErr == nil {
		t.Error("a kit was written although there is nowhere to store the recipient")
	}
}

func TestBackupInitOnTheIsolatedTier(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	u.withoutRecipient()
	kit := filepath.Join(t.TempDir(), "kit.txt")
	args := []string{"--tier", "isolated", "--settings", settingsFile(u.home), "--kit", kit, "--yes"}
	if err := BackupInit(context.Background(), args, u.env()); err != nil {
		t.Fatal(err)
	}
	id, err := archive.ParseIdentity(readFile(t, kit))
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(settingsFile(u.home))
	if err != nil || got.BackupRecipient != id.Recipient().String() || got.HTTPPort != u.port {
		t.Errorf("settings %+v, %v", got, err)
	}
	noIdentityInCalls(t, u.f.calls)
	noIdentityIn(t, "the output", u.out.String())
}

func TestMainRoutesBackupInit(t *testing.T) {
	u := bareInstance(t)
	kit := filepath.Join(t.TempDir(), "kit.txt")
	var stderr bytes.Buffer
	args := []string{"backup", "init", "--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--runtime", "podman", "--connection", "pm", "--kit", kit, "--yes"}
	if code := Main(context.Background(), args, u.env(), &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if _, err := os.Stat(kit); err != nil {
		t.Errorf("the kit was not written: %v", err)
	}
	for _, want := range []string{"suiteward backup init"} {
		if !strings.Contains(Usage, want) {
			t.Errorf("the usage lacks %q:\n%s", want, Usage)
		}
	}
	stderr.Reset()
	if code := Main(context.Background(), []string{"backup", "init", "--bogus"}, u.env(), &stderr); code != 2 || !strings.Contains(stderr.String(), "suiteward backup init") {
		t.Errorf("a bad flag: exit %d, stderr %q", code, stderr.String())
	}
}
