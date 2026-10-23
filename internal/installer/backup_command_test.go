package installer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/archive"
)

// backupCmd runs `suiteward backup` on the stack of the harness; the input is not a terminal.
func (u *upgradeHarness) backupCmd(args ...string) error {
	u.t.Helper()
	all := append([]string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--runtime", "podman", "--connection", "pm"}, args...)
	return Backup(context.Background(), all, u.env())
}

// backupCmdOnTerminal runs it on a terminal that answers with input.
func (u *upgradeHarness) backupCmdOnTerminal(input string, args ...string) error {
	u.t.Helper()
	env := u.env()
	env.Term = Terminal{In: strings.NewReader(input), Out: &u.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}
	all := append([]string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--runtime", "podman", "--connection", "pm"}, args...)
	return Backup(context.Background(), all, env)
}

func TestBackupCommandWritesAnEncryptedBackupOfAColocatedStackAndChangesNothing(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	dir := filepath.Join(t.TempDir(), "mine")
	if err := u.backupCmd("--to", dir); err != nil {
		t.Fatal(err)
	}
	path := oneArchive(t, dir)
	manifest, members := openBackup(t, path)
	for name, want := range map[string]string{"status.json": statusBody, "suiteward.sql": dumpText, "suiteward-artifacts.tar": "TARBYTES", "settings.yaml": readFile(t, u.settingsPath())} {
		if members[name] != want {
			t.Errorf("%s holds %q, want %q", name, members[name], want)
		}
	}
	if len(members) != 4 || manifest.Tier != "co-located" || manifest.Instance != testInstance || manifest.ImageDigest != oldDigest || manifest.SuitewardVersion != "v0.2.0" {
		t.Errorf("the backup holds %v and names %+v", memberNames(manifest), manifest.Info)
	}
	// The dump goes first: the volume taken after it holds every artifact the dump refers to; and nothing is stopped, replaced or started.
	u.assertOrder("exec -i suiteward-db pg_dump -U suiteward -d suiteward", "volume export suiteward-data --output")
	u.noChanges()
	recipient, _ := testRecoveryKey(t)
	for _, want := range []string{path, archive.Fingerprint(recipient)} {
		if !strings.Contains(u.out.String(), want) {
			t.Errorf("the output lacks %q:\n%s", want, u.out.String())
		}
	}
	for _, secret := range []string{"HUNTER2-TOTP", "TARBYTES"} {
		if strings.Contains(u.out.String(), secret) {
			t.Error("the content of the backup reached the output")
		}
	}
	u.f.assertNoSecret(t, "HUNTER2-TOTP", DatabaseURL(appRole, "x"))
	assertNoPlaintext(t, dir, "HUNTER2-TOTP", "TARBYTES")
}

func TestBackupCommandWritesBesideTheTokenCopyUnlessToldOtherwise(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	if err := u.backupCmd(); err != nil {
		t.Fatal(err)
	}
	oneArchive(t, u.backups) // filepath.Join(filepath.Dir(token), "backups"), as an upgrade does
}

func TestBackupCommandOnTheIsolatedTierKeepsTheUnits(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	before := u.units()
	args := []string{"--tier", "isolated", "--settings", settingsFile(u.home)}
	if err := Backup(context.Background(), args, u.env()); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(u.home, ".config", "suiteward", "backups")
	manifest, members := openBackup(t, oneArchive(t, dir))
	if manifest.Tier != "isolated" || manifest.ImageDigest != oldDigest {
		t.Errorf("the manifest names %+v", manifest.Info)
	}
	for name, want := range before {
		if members["units/"+name] != want {
			t.Errorf("units/%s is not the installed unit", name)
		}
	}
	if members["suiteward.sql"] != dumpText || members["suiteward-artifacts.tar"] != "TARBYTES" {
		t.Errorf("the backup holds %v", members)
	}
	u.untouched(before)
}

func TestBackupCommandOnDockerReadsTheVolumeThroughAThrowawayContainer(t *testing.T) {
	h, backups := newDockerUpgrade(t)
	args := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "docker", "--to", backups}
	if err := Backup(context.Background(), args, h.newEnv()); err != nil {
		t.Fatal(err)
	}
	if len(h.f.find("tar -cf - -C /data .")) != 1 {
		t.Errorf("the volume was not read by a throwaway container: %v", h.f.lines())
	}
	manifest, members := openBackup(t, oneArchive(t, backups))
	if members["suiteward-artifacts.tar"] != "TARBYTES" || members["suiteward.sql"] != dumpText || manifest.Tier != "co-located" {
		t.Errorf("the backup holds %v as %+v", members, manifest.Info)
	}
}

func TestBackupCommandNeedsARecoveryKeyAndOffersOneOnATerminal(t *testing.T) {
	// No terminal: refuse, and name the command; nothing is read from the engine.
	u := newUpgradeHarness(t, func(s *Settings) { s.BackupRecipient = "" })
	dir := filepath.Join(t.TempDir(), "mine")
	err := u.backupCmd("--to", dir)
	if err == nil || !strings.Contains(err.Error(), "suiteward backup init") {
		t.Fatalf("got %v, want a refusal that names `suiteward backup init`", err)
	}
	if len(u.f.find("pg_dump")) != 0 {
		t.Error("the database was dumped although no key could encrypt it")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*")); len(files) != 0 {
		t.Errorf("the refused backup left %v", files)
	}

	// A terminal is offered the key, and the backup is made with it.
	u = newUpgradeHarness(t, func(s *Settings) { s.BackupRecipient = "" })
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := u.backupCmdOnTerminal("y\nstored\n", "--to", dir); err != nil {
		t.Fatal(err)
	}
	kit := filepath.Join(cwd, "suiteward-recovery-kit-"+testInstance+".txt")
	_, members := openBackupWith(t, oneArchive(t, dir), readFile(t, kit))
	if members["suiteward.sql"] != dumpText {
		t.Errorf("the backup does not open with the key that was made: %v", members)
	}
	id, err := archive.ParseIdentity(readFile(t, kit))
	if err != nil || u.settings().BackupRecipient != id.Recipient().String() {
		t.Errorf("the settings do not hold the new key: %v", err)
	}

	// A terminal that says no gets the refusal and nothing else.
	u = newUpgradeHarness(t, func(s *Settings) { s.BackupRecipient = "" })
	dir = filepath.Join(t.TempDir(), "again")
	if err := u.backupCmdOnTerminal("n\n", "--to", dir); err == nil || !strings.Contains(err.Error(), "suiteward backup init") {
		t.Fatalf("got %v", err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*")); len(files) != 0 || u.settings().BackupRecipient != "" {
		t.Errorf("a refused offer changed something: %v", files)
	}
}

func TestBackupCommandNeedsTheServiceToAnswerStatus(t *testing.T) {
	u := newUpgradeHarness(t, func(s *Settings) { s.HTTPPort, s.MCPPort = 1, 2 }) // nothing listens there
	dir := filepath.Join(t.TempDir(), "mine")
	err := u.backupCmd("--to", dir)
	if err == nil || !strings.Contains(err.Error(), "/status") || !strings.Contains(err.Error(), "start suiteward-db") {
		t.Fatalf("got %v, want a refusal that names /status and the commands that start the service", err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*")); len(files) != 0 {
		t.Errorf("a partial backup was left: %v", files)
	}
}

func TestBackupCommandOnAFullScreenWalksTheStepsAndKeepsThePathAndTheKey(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	c := newTermConsole(&u.out)
	env := u.env()
	env.OpenScreen = c.open
	dir := filepath.Join(t.TempDir(), "mine")
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--runtime", "podman", "--connection", "pm", "--to", dir}
	if err := Backup(liveContext(t), args, env); err != nil {
		t.Fatal(err)
	}
	c.steps(t, "Status", "Dump", "Artifacts", "Encrypt")
	recipient, _ := testRecoveryKey(t)
	for _, want := range []string{oneArchive(t, dir), archive.Fingerprint(recipient)} {
		if !strings.Contains(c.tail(), want) {
			t.Errorf("%q is not in the scrollback after Close:\n%s", want, c.tail())
		}
	}
	noRepeats(t, c.tail())
	if !c.closed() {
		t.Error("the screen was not closed")
	}
}

func TestMainRoutesBackupAndTheUsageNamesIt(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	dir := filepath.Join(t.TempDir(), "mine")
	var stderr bytes.Buffer
	args := []string{"backup", "--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--runtime", "podman", "--connection", "pm", "--to", dir}
	if code := Main(context.Background(), args, u.env(), &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	oneArchive(t, dir)
	for _, want := range []string{"suiteward backup init", "suiteward backup ["} {
		if !strings.Contains(Usage, want) {
			t.Errorf("the usage lacks %q:\n%s", want, Usage)
		}
	}
	stderr.Reset()
	if code := Main(context.Background(), []string{"backup", "stray"}, u.env(), &stderr); code != 2 || !strings.Contains(stderr.String(), "suiteward backup") {
		t.Errorf("a stray argument: exit %d, stderr %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "stray")); err == nil {
		t.Error("a stray argument made something")
	}
}

func TestBackupCommandRemovesWhatAKilledBackupOrRestoreLeftAndSaysSo(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	dir := filepath.Join(t.TempDir(), "mine")
	stale, keep := leftovers(t, dir)
	if err := u.backupCmd("--to", dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range stale {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s was left", name)
		}
		if !strings.Contains(u.out.String(), name) {
			t.Errorf("the output does not say that %s was removed:\n%s", name, u.out.String())
		}
	}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
}
