package installer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A hint names the program as the person can run it from where they are: `suiteward` where their shell finds the program by that name, else the
// path of the executable that is running, quoted for a shell. On Windows the release is a file in Downloads that is not on the PATH, so a
// hint that said `suiteward doctor` named a command that does not exist.

// swCmd is a name that no hint spells by itself, so that a test sees which hints were built with the name of the run.
const swCmd = "/opt/sw/suiteward"

// aProgram is a file that stands for the running executable.
func aProgram(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// realPath is the path commandName shows for an executable: with its links resolved.
func realPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func running(path string) func() (string, error) { return func() (string, error) { return path, nil } }

func finds(path string) func(string) (string, error) {
	return func(name string) (string, error) {
		if name != "suiteward" {
			return "", errors.New("asked for " + name + ", not suiteward")
		}
		return path, nil
	}
}

func TestCommandNameIsSuitewardWhereTheShellFindsThisProgramByThatName(t *testing.T) {
	exe := aProgram(t, t.TempDir(), "suiteward")
	if got := commandName("linux", running(exe), finds(exe)); got != "suiteward" {
		t.Errorf("commandName = %q for a program on the PATH, want suiteward", got)
	}
	link := filepath.Join(t.TempDir(), "suiteward")
	if err := os.Link(exe, link); err != nil {
		t.Skipf("no hard links here: %v", err)
	}
	if got := commandName("linux", running(exe), finds(link)); got != "suiteward" {
		t.Errorf("commandName = %q for a PATH entry that is another name of this file, want suiteward", got)
	}
}

func TestCommandNameIsThePathOfThisProgramWhereTheShellDoesNotFindIt(t *testing.T) {
	exe := aProgram(t, filepath.Join(t.TempDir(), "downloads"), "suiteward_v0.4.0-alpha.1_windows_amd64.exe")
	other := aProgram(t, filepath.Join(t.TempDir(), "elsewhere"), "suiteward")
	for name, lookPath := range map[string]func(string) (string, error){
		"not on the PATH":          func(string) (string, error) { return "", exec.ErrNotFound },
		"another program of it":    finds(other),
		"found only in the folder": func(string) (string, error) { return exe, exec.ErrDot }, // a shell does not run a program of the current folder by its name
	} {
		t.Run(name, func(t *testing.T) {
			got := commandName("linux", running(exe), lookPath)
			if want := quoteArg(realPath(t, exe)); got != want {
				t.Errorf("commandName = %q, want the path %q", got, want)
			}
			if got == "suiteward" {
				t.Error("a hint names a command that the shell does not find")
			}
		})
	}
}

func TestCommandNameQuotesAPathWithASpaceAndPowerShellNeedsTheCallOperatorForIt(t *testing.T) {
	exe := aProgram(t, filepath.Join(t.TempDir(), "My Downloads"), "suiteward_v0.4.0-alpha.1_windows_amd64 (1).exe")
	notFound := func(string) (string, error) { return "", exec.ErrNotFound }
	quoted := quoteArg(realPath(t, exe))
	if !strings.HasPrefix(quoted, "'") {
		t.Fatalf("quoteArg(%q) = %q, want a quoted path", exe, quoted)
	}
	if got := commandName("linux", running(exe), notFound); got != quoted {
		t.Errorf("on Linux commandName = %q, want %q", got, quoted)
	}
	// A quoted string at the start of a PowerShell line is an expression, not a command: `& 'C:\...\x (1).exe' doctor` runs it.
	if got := commandName("windows", running(exe), notFound); got != "& "+quoted {
		t.Errorf("on Windows commandName = %q, want %q", got, "& "+quoted)
	}
	// A path with nothing to quote: one that does not exist is not resolved, so the temp directory of the machine (the runner of CI has a `~` in
	// it, which is quoted) is not involved.
	plain := `C:\Tools\suiteward.exe`
	if got := commandName("windows", running(plain), notFound); got != plain {
		t.Errorf("a path with nothing to quote is run as it is on Windows, got %q", got)
	}
}

func TestCommandNameIsSuitewardWhenThePathOfThisProgramIsNotKnown(t *testing.T) {
	failed := func() (string, error) { return "", errors.New("no executable") }
	if got := commandName("linux", failed, func(string) (string, error) { return "", exec.ErrNotFound }); got != "suiteward" {
		t.Errorf("commandName = %q, want suiteward: there is no better name", got)
	}
}

func TestADepsWithoutACommandNamesTheProgramSuiteward(t *testing.T) {
	if got := (Deps{}).command(); got != "suiteward" {
		t.Errorf("command() = %q, want suiteward", got)
	}
	if got := (Deps{Command: swCmd}).command(); got != swCmd {
		t.Errorf("command() = %q, want %q", got, swCmd)
	}
}

// ---- the builders of the hints take the name of the run ----

func TestTheHintBuildersNameTheProgramAsTheRunReachesIt(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"doctor", doctorHint(swCmd, "/s.yaml", true), swCmd + " doctor"},
		{"doctor with settings", doctorHint(swCmd, "/home/me/My Settings/s.yaml", false), swCmd + " doctor --settings '/home/me/My Settings/s.yaml'"},
		{"enroll", enrollCommand(swCmd, &Resolver{flags: map[string]string{"settings": "/s.yaml"}}, false), swCmd + " enroll --settings /s.yaml"},
		{"enroll isolated", enrollCommand(swCmd, &Resolver{}, true), swCmd + " enroll --tier isolated"},
		{"no key", noKeyError(swCmd, false).Error(), "Run `" + swCmd + " backup init` first"},
		{"no key in an upgrade", noKeyError(swCmd, true).Error(), "Run `" + swCmd + " backup init` first"},
		{"the way back of a co-located upgrade", goBackColocated(swCmd, "podman", "ghcr.io/x@sha256:1", "/b/one.tar.age", " --settings /s.yaml"),
			"`" + swCmd + " uninstall --yes --settings /s.yaml`"},
		{"the way back, install", goBackColocated(swCmd, "podman", "ghcr.io/x@sha256:1", "/b/one.tar.age", ""), "`" + swCmd + " install --image ghcr.io/x@sha256:1`"},
		{"the way back, restore", goBackColocated(swCmd, "podman", "ghcr.io/x@sha256:1", "/b/one.tar.age", ""), "`" + swCmd + " restore /b/one.tar.age`"},
		{"the way back of an isolated upgrade", goBackIsolated(swCmd, "/b/one.tar.age", "/u", " --tier isolated"), "`" + swCmd + " restore /b/one.tar.age --tier isolated`"},
		{"a co-located upgrade that migrated", withBackup(swCmd, migratingError{errors.New("boom")}, "/b/one.tar.age", "", "").Error(), "run `" + swCmd + " restore /b/one.tar.age`"},
		{"a co-located upgrade before migrate", withBackup(swCmd, errors.New("boom"), "/b/one.tar.age", "", "").Error(), "run `" + swCmd + " install` with the same options as this run"},
		{"a co-located upgrade before migrate, retry", withBackup(swCmd, errors.New("boom"), "/b/one.tar.age", "", "").Error(), "`" + swCmd + " install --upgrade` retries the upgrade"},
		{"an isolated upgrade that went on", withBackup(swCmd, errors.New("boom"), "/b/one.tar.age", "/u", "").Error(), "run `" + swCmd + " install --upgrade` again"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(c.got, c.want) {
				t.Errorf("got %q, want it to contain %q", c.got, c.want)
			}
			if strings.Contains(strings.ReplaceAll(c.got, swCmd, ""), "suiteward install") || strings.Contains(strings.ReplaceAll(c.got, swCmd, ""), "suiteward restore") ||
				strings.Contains(strings.ReplaceAll(c.got, swCmd, ""), "suiteward doctor") || strings.Contains(strings.ReplaceAll(c.got, swCmd, ""), "suiteward backup") {
				t.Errorf("a hint still names suiteward by itself: %q", c.got)
			}
		})
	}
}

// compareStatus is what a restore says about the integrity check that has not passed yet.
func TestTheNoteOfARestoreNamesDoctorAsTheRunReachesIt(t *testing.T) {
	after, err := parseStatus([]byte(`{"version":"v0.2.0","instance":"1a2b3c4d","github":[{"repo":"acme/api","integrity":"pending"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, notes := compareStatus(swCmd, after, after)
	if len(notes) != 1 || !strings.Contains(notes[0], "run `"+swCmd+" doctor` in a moment") {
		t.Errorf("notes = %q, want one that names `%s doctor`", notes, swCmd)
	}
}

// ---- the commands print the name of the run ----

func TestBackupNamesTheRestoreAsTheRunReachesIt(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	env := u.env()
	env.Deps.Command = swCmd
	dir := filepath.Join(t.TempDir(), "mine")
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--runtime", "podman", "--connection", "pm", "--to", dir}
	if err := Backup(context.Background(), args, env); err != nil {
		t.Fatal(err)
	}
	path := oneArchive(t, dir)
	if want := "To restore it, run `" + swCmd + " restore " + quoteArg(path) + "` with that kit."; !strings.Contains(u.out.String(), want) {
		t.Errorf("the output lacks %q:\n%s", want, u.out.String())
	}
	if strings.Contains(u.out.String(), "`suiteward restore") {
		t.Errorf("the output still names suiteward by itself:\n%s", u.out.String())
	}
}

func TestABackupWithoutAKeyNamesBackupInitAsTheRunReachesIt(t *testing.T) {
	u := newUpgradeHarness(t, func(s *Settings) { s.BackupRecipient = "" })
	env := u.env()
	env.Deps.Command = swCmd
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--runtime", "podman", "--connection", "pm", "--to", t.TempDir()}
	err := Backup(context.Background(), args, env)
	if err == nil || !strings.Contains(err.Error(), "Run `"+swCmd+" backup init` first") {
		t.Fatalf("got %v, want a refusal that names `%s backup init`", err, swCmd)
	}
}

func TestAnUpgradeNamesItsNextCommandsAsTheRunReachesThem(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	env := u.env()
	env.Deps.Command = swCmd
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", testImage, "--yes"}
	if err := Install(context.Background(), args, env); err != nil {
		t.Fatal(err)
	}
	out := u.out.String()
	for _, want := range []string{"`" + swCmd + " uninstall --yes --settings ", "`" + swCmd + " install --image ", "`" + swCmd + " restore ", "Check the instance with `" + swCmd + " doctor --settings "} {
		if !strings.Contains(out, want) {
			t.Errorf("the output of the upgrade lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "`suiteward ") {
		t.Errorf("the output still names suiteward by itself:\n%s", out)
	}
}

func TestAnUpgradeThatHasNothingToDoNamesDoctorAsTheRunReachesIt(t *testing.T) {
	u := newUpgradeHarness(t, func(s *Settings) { s.Image = testImage })
	u.restack(testImage, nil)
	env := u.env()
	env.Deps.Command = swCmd
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", testImage, "--yes"}
	if err := Install(context.Background(), args, env); err != nil {
		t.Fatal(err)
	}
	if want := "Check the instance with `" + swCmd + " doctor --settings "; !strings.Contains(u.out.String(), want) {
		t.Errorf("the output lacks %q:\n%s", want, u.out.String())
	}
}

func TestARestoreNamesDoctorAsTheRunReachesIt(t *testing.T) {
	u := restoreHarness(t)
	env := u.env()
	env.Deps.Command = swCmd
	if err := Restore(context.Background(), u.restoreArgs(aBackup(t, nil), confirmed(t, t.TempDir())...), env); err != nil {
		t.Fatal(err)
	}
	if want := "Check the instance with `" + swCmd + " doctor`."; !strings.Contains(u.out.String(), want) {
		t.Errorf("the output lacks %q:\n%s", want, u.out.String())
	}
	if strings.Contains(u.out.String(), "`suiteward ") {
		t.Errorf("the output still names suiteward by itself:\n%s", u.out.String())
	}
}

func TestTheMessagesOfARestoreNameTheProgramAsTheRunReachesIt(t *testing.T) {
	var out bytes.Buffer
	x := &restoreRun{env: Env{Deps: Deps{Command: swCmd}}, file: "/b/one.tar.age", safety: "/b/safety.tar.age", stage: stageLoad, scr: screenOn(&out)}
	x.info.ImageDigest = testDigest
	_, noKey := x.chooseSafetyKey()
	x.finish()
	for name, c := range map[string]struct{ got, want string }{
		"another release": {x.otherRelease("the release x").Error(), "(`" + swCmd + " install --image " + releaseImageRepo + "@" + testDigest + "`)"},
		"no recovery key": {noKey.Error(), "Run `" + swCmd + " backup init` first"},
		"run again":       {x.failed(errors.New("boom")).Error(), "To run the restore again: `" + swCmd + " restore /b/one.tar.age`."},
		"way back":        {x.failed(errors.New("boom")).Error(), "To go back to the state before the restore: `" + swCmd + " restore /b/safety.tar.age`, with"},
		"after":           {out.String(), "run `" + swCmd + " backup init` so that it can be backed up."},
	} {
		if !strings.Contains(c.got, c.want) {
			t.Errorf("%s: got %q, want it to contain %q", name, c.got, c.want)
		}
	}
}

func TestAnUpgradeRefusedForAnUnfinishedInstallNamesInstallAsTheRunReachesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml.pending-app")
	if err := savePendingApp(path, PendingApp{AppID: 7, Slug: "suiteward-acme", Account: "acme", AccountType: "Organization"}); err != nil {
		t.Fatal(err)
	}
	err := refuseUnfinishedInstall(swCmd, path, func() (GitHub, error) { return GitHub{AppID: 3}, nil })
	if err == nil || !strings.Contains(err.Error(), "with a plain `"+swCmd+" install`") {
		t.Errorf("got %v, want a refusal that names `%s install`", err, swCmd)
	}
}

func TestOSEnvNamesTheProgramAsTheShellReachesIt(t *testing.T) {
	want := commandName(runtime.GOOS, os.Executable, exec.LookPath)
	if got := OSEnv(&bytes.Buffer{}).Deps.Command; got != want {
		t.Errorf("OSEnv carries the command name %q, want %q", got, want)
	}
}
