package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The hints that every command builds name the program as the run reaches it (Deps.Command): swCmd stands for a program that is not on the PATH,
// so a hint that still says `suiteward` names a command that does not exist there.

func TestBackupInitNamesTheProgramAsTheRunReachesIt(t *testing.T) {
	recipient, _ := testRecoveryKey(t)
	var out bytes.Buffer
	r, err := NewResolver(nil, nil, Terminal{In: strings.NewReader(""), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	k := &keyInit{r: r, env: Env{Deps: Deps{Command: swCmd}}, inst: &instance{s: Settings{BackupRecipient: recipient}}, recipient: recipient}
	if err := k.keys(context.Background()); err == nil || !strings.Contains(err.Error(), "run `"+swCmd+" backup init --replace`") {
		t.Errorf("keys = %v, want a refusal that names `%s backup init --replace`", err, swCmd)
	}
	k.kept()
	if want := "`" + swCmd + " backup` and `" + swCmd + " install --upgrade` lock their backups"; !strings.Contains(out.String(), want) {
		t.Errorf("the output lacks %q:\n%s", want, out.String())
	}
}

func TestAnInstallOfAnotherReleaseSaysToUpgradeWithTheProgramOfTheRun(t *testing.T) {
	h := purgeHarness(t)
	env := h.newEnv()
	env.Deps.Command = swCmd
	args := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--image", oldImage}
	if err := Install(context.Background(), args, env); err == nil || !strings.Contains(err.Error(), "run `"+swCmd+" install --upgrade`") {
		t.Errorf("Install = %v, want a refusal that says to run `%s install --upgrade`", err, swCmd)
	}
}

func TestTheRefusalsOfTheSettingsAndOfTheRecoveryKeyNameTheProgramAsTheRunReachesIt(t *testing.T) {
	if _, _, err := loadSaved(swCmd, filepath.Join(t.TempDir(), "gone.yaml"), true); err == nil || !strings.Contains(err.Error(), "only `"+swCmd+" install` makes a settings file") {
		t.Errorf("loadSaved = %v, want a refusal that names `%s install`", err, swCmd)
	}
	h := newHarness(t, "linux")
	env := h.newEnv()
	env.Deps.Command = swCmd
	err := Install(context.Background(), []string{"--settings", h.settingsPath(), "--backup-recipient", "age1x"}, env)
	if err == nil || !strings.Contains(err.Error(), "run `"+swCmd+" backup init`") {
		t.Errorf("Install = %v, want a refusal that names `%s backup init`", err, swCmd)
	}
}

// ---- doctor ----

// doctorAs runs doctor --json with the settings of s as the run that is named cmd.
func doctorAs(t *testing.T, f *fakeEngine, cmd, path string) map[string]doctorResult {
	t.Helper()
	deps := f.deps()
	deps.Command = cmd
	var out bytes.Buffer
	Doctor(context.Background(), deps, path, "", &out, true)
	var list []doctorResult
	if err := json.Unmarshal(out.Bytes(), &list); err != nil {
		t.Fatalf("doctor --json printed %q: %v", out.String(), err)
	}
	byName := map[string]doctorResult{}
	for _, r := range list {
		byName[r.Name] = r
	}
	return byName
}

func savedAt(t *testing.T, s Settings) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDoctorNamesTheProgramAsTheRunReachesIt(t *testing.T) {
	t.Run("no settings file", func(t *testing.T) {
		res := doctorAs(t, newFake("linux"), swCmd, filepath.Join(t.TempDir(), "settings.yaml"))
		if fix := res["settings"].Fix; !strings.Contains(fix, "Run `"+swCmd+" install` first") {
			t.Errorf("the fix is %q, want one that names `%s install`", fix, swCmd)
		}
	})
	t.Run("settings that cannot be read", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.yaml")
		if err := os.WriteFile(path, []byte("tier: wrong\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		res := doctorAs(t, newFake("linux"), swCmd, path)
		if fix := res["settings"].Fix; !strings.Contains(fix, "write it again with `"+swCmd+" install`") {
			t.Errorf("the fix is %q, want one that names `%s install`", fix, swCmd)
		}
	})
	t.Run("a Docker container whose restart policy is not always", func(t *testing.T) {
		f, s := colocated{engine: RuntimeDocker, edit: func(name string, obj map[string]any) {
			if name == ContainerDB {
				obj["HostConfig"].(map[string]any)["RestartPolicy"] = map[string]any{"Name": "no"}
			}
		}}.build(t)
		fix := doctorAs(t, f, swCmd, savedAt(t, s))["restart"].Fix
		if !strings.Contains(fix, "`"+swCmd+" uninstall`") || !strings.Contains(fix, "`"+swCmd+" install`") {
			t.Errorf("the fix is %q, want one that names `%s uninstall` and `%s install`", fix, swCmd, swCmd)
		}
	})
	t.Run("an image that drifted", func(t *testing.T) {
		other := "ghcr.io/ignisdevne/suiteward@sha256:" + strings.Repeat("c", 64)
		f, s := colocated{engine: RuntimeDocker, migrate: doctorStateOf("exited", 0, ""), edit: func(name string, obj map[string]any) {
			if name == ContainerMigrate {
				obj["Config"].(map[string]any)["Image"] = other
			}
		}}.build(t)
		if fix := doctorAs(t, f, swCmd, savedAt(t, s))["pins"].Fix; !strings.Contains(fix, "`"+swCmd+" install --upgrade`") {
			t.Errorf("the fix is %q, want one that names `%s install --upgrade`", fix, swCmd)
		}
	})
}

// ---- enroll and the instance ----

func TestEnrollNamesTheProgramAsTheRunReachesIt(t *testing.T) {
	enrollAs := func(t *testing.T, args ...string) string {
		t.Helper()
		useDefaultConfigDir(t)
		h := enrollHarness(t, statusEnrolled)
		term, _ := fakeTerminal(true, "")
		term.Out = &h.out
		env := h.newEnv()
		env.Deps.Command = swCmd
		env.Term = term
		var stderr bytes.Buffer
		if code := Main(context.Background(), append([]string{"enroll", "--owner", "alice"}, args...), env, &stderr); code != 1 {
			t.Fatalf("exit %d, stderr %q", code, stderr.String())
		}
		return stderr.String()
	}
	if got := enrollAs(t); !strings.Contains(got, "run `"+swCmd+" install --upgrade --tier co-located` first") {
		t.Errorf("a host with no settings file is told %q, want `%s install --upgrade --tier co-located`", got, swCmd)
	}
	if got := enrollAs(t, "--tier", "co-located"); !strings.Contains(got, "run `"+swCmd+" install --upgrade`, which saves the settings file") {
		t.Errorf("a co-located stack with no settings file is told %q, want `%s install --upgrade`", got, swCmd)
	}
	if err := func() error { _, err := enrollChecks(swCmd, &Resolver{}, GitHub{}); return err }(); err == nil || !strings.Contains(err.Error(), "run `"+swCmd+" install --github yes` first") {
		t.Errorf("enrollChecks = %v, want a refusal that names `%s install --github yes`", err, swCmd)
	}
}

func TestTheMessagesOfTheInstanceNameTheProgramAsTheRunReachesIt(t *testing.T) {
	if err := (instance{settingsPath: "/s.yaml"}).needSettings(swCmd); err == nil || !strings.Contains(err.Error(), "`"+swCmd+" install --upgrade`, which saves one") {
		t.Errorf("needSettings = %v, want a message that names `%s install --upgrade`", err, swCmd)
	}
	steps := colocatedSteps(swCmd, nil, colocatedSettings(), testImage, screenOn(&bytes.Buffer{}))
	if !strings.Contains(steps.recovery, "run `"+swCmd+" install` with the same options as this run") {
		t.Errorf("the way back to a running service is %q, want it to name `%s install`", steps.recovery, swCmd)
	}
}

func TestNothingInstalledNamesTheInstallAsTheRunReachesIt(t *testing.T) {
	err := nothingInstalled(swCmd)
	if !errors.Is(err, ErrNothingInstalled) || !strings.Contains(err.Error(), "install first with `"+swCmd+" install`") {
		t.Errorf("nothingInstalled = %v, want ErrNothingInstalled that says to install first with `%s install`", err, swCmd)
	}
	// The same refusal, from a command that finds no stack.
	useDefaultConfigDir(t)
	h := newHarness(t, "linux")
	env := h.newEnv()
	env.Deps.Command = swCmd
	args := []string{"--upgrade", "--tier", "co-located", "--runtime", "podman", "--connection", "pm", "--image", testImage, "--token-file", h.tokenPath()}
	if err := Install(context.Background(), args, env); !errors.Is(err, ErrNothingInstalled) || !strings.Contains(err.Error(), "install first with `"+swCmd+" install`") {
		t.Errorf("an upgrade of nothing = %v, want ErrNothingInstalled that says to install first with `%s install`", err, swCmd)
	}
}

// ---- the isolated upgrade ----

func TestAnIsolatedUpgradeNamesTheProgramAsTheRunReachesIt(t *testing.T) {
	u := newIsoUpgrade(t, func(p *UnitParams) { p.Digest = testDigest })
	env := u.env()
	env.Deps.Command = swCmd
	fastHealthWait(t)
	args := []string{"--upgrade", "--tier", "isolated", "--settings", settingsFile(u.home), "--backup-dir", u.backups, "--image", testDigest, "--yes"}
	if err := Install(context.Background(), args, env); err != nil {
		t.Fatal(err)
	}
	if want := "To check that everything works, run `" + swCmd + " doctor`."; !strings.Contains(u.out.String(), want) {
		t.Errorf("the output lacks %q:\n%s", want, u.out.String())
	}
	r := &Resolver{flags: map[string]string{"settings": "/s.yaml"}}
	if got, want := installCommand(swCmd, r, true), swCmd+" install --tier isolated --settings /s.yaml"; got != want {
		t.Errorf("installCommand = %q, want %q", got, want)
	}
}

// ---- the GitHub flow and setup ----

func TestTheRulesetStepNamesDoctorAsTheRunReachesIt(t *testing.T) {
	flags := func() map[string]string { return existingFlags(t, map[string]string{"_command": swCmd}) }
	t.Run("a skip", func(t *testing.T) {
		g := newFakeGitHub(t)
		run := startRulesetRunWith(t, g, flags())
		waitForText(t, run.out, "Skip, import later")
		run.answer(t, "2")
		run.result(t)
		_ = run.env.Out.(*ui.Screen).Close()
		tail := run.out.String()[strings.LastIndex(run.out.String(), leaveAlt):]
		if want := "then run `" + swCmd + " doctor` to check the protection"; !strings.Contains(tail, want) {
			t.Errorf("the warning that is kept lacks %q:\n%s", want, tail)
		}
	})
	t.Run("after a check that found nothing", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.changeRepos(func(repos []fakeRepo) { repos[0].noRule = true })
		run := startRulesetRunWith(t, g, flags())
		waitForText(t, run.out, "I imported it, check now")
		run.answer(t, "1")
		waitForText(t, run.out, "missing-contract-check")
		run.answer(t, "2")
		run.result(t)
		_ = run.env.Out.(*ui.Screen).Close()
		tail := run.out.String()[strings.LastIndex(run.out.String(), leaveAlt):]
		if want := "To check again at any time, run `" + swCmd + " doctor`."; !strings.Contains(tail, want) {
			t.Errorf("the scrollback lacks %q:\n%s", want, tail)
		}
	})
}

func TestTheEndOfSetupNamesEnrollAsTheRunReachesIt(t *testing.T) {
	run := newScreenRun(100, 30)
	scr, _, err := run.on(Env{}).openConsole(liveContext(t), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := afterSetup(scr, swCmd, errors.New("boom")); err == nil {
		t.Fatal("afterSetup hid the failure of setup")
	}
	mustSay(t, viewOf(t, run.output(), 100, 30), "run `"+swCmd+" enroll` to try again")
	_ = scr.Close()
}

func TestASetupThatDoesNotEndInTimeSaysToRunEnrollAgainAsTheRunReachesIt(t *testing.T) {
	withFastPolls(t, time.Millisecond, 20*time.Millisecond)
	err := waitForExitCode(context.Background(), filepath.Join(t.TempDir(), "exit"))
	if !errors.Is(err, errSetupTimeout) {
		t.Fatalf("waitForExitCode = %v, want errSetupTimeout", err)
	}
	run := newScreenRun(100, 30)
	scr, _, openErr := run.on(Env{}).openConsole(liveContext(t), true)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer func() { _ = scr.Close() }()
	if got := afterSetup(scr, swCmd, err); got == nil || !strings.Contains(got.Error(), "run "+swCmd+" enroll again") {
		t.Errorf("afterSetup = %v, want an error that says to run `%s enroll` again", got, swCmd)
	}
}

func TestTheIsolatedImageRefusalNamesTheUpgradeAsTheRunReachesIt(t *testing.T) {
	r := newTestResolver(t, map[string]string{"image": "sha256:" + strings.Repeat("b", 64)}, nil, Terminal{In: strings.NewReader(""), Out: &syncBuffer{}})
	_, err := chooseImage(swCmd, r, strings.Repeat("a", 64), func() (bool, error) { return false, nil }, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "run `"+swCmd+" install --upgrade`") {
		t.Errorf("chooseImage = %v, want a refusal that says to run `%s install --upgrade`", err, swCmd)
	}
}
