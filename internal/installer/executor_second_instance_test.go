package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A second instance on one PC has a settings file of its own. Installed or upgraded with no --token-file it would fall back to the default token
// file, which is the main instance's, and so plan, stop, rewrite and restart the main instance's executor, pointed at the second instance's
// service (the review of phase M2.1, area C, S1). The rule of the uninstall (tokenFileOf) holds for the install and the upgrade: the default token
// file is this instance's only with the default settings file, and any other settings file asks for --token-file, before anything is changed.

const mainExecutorRunning = "LoadState=loaded\nActiveState=active\nSubState=running\n"

// mainInstanceExecutor puts down what the main instance of the user leaves beside its default token file: the executor's program and token copy,
// and the user unit that names that program. It returns the unit and what it holds.
func mainInstanceExecutor(t *testing.T, h *harness) (unit, content string) {
	t.Helper()
	mainToken := mainInstanceToken(t, h)
	program := filepath.Join(executorDirOf(mainToken), "suiteward-executor")
	unit = filepath.Join(h.dir, ".config", "systemd", "user", "suiteward-executor.service")
	content = "[Service]\nExecStart=\"" + unitValue(program) + "\" execute\nEnvironment=\"SUITEWARD_EXECUTOR_URL=http://127.0.0.1:27081\"\n"
	for path, text := range map[string]string{program: "the main instance's program", executorTokenFile(mainToken): strings.Repeat("a", 64), unit: content} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h.f.on("--user show -p LoadState -p ActiveState -p SubState suiteward-executor.service", mainExecutorRunning)
	return unit, content
}

// assertMainExecutorUntouched fails when anything reached the executor of the main instance: its unit, its program or the user manager that runs it.
func assertMainExecutorUntouched(t *testing.T, h *harness, unit, content string) {
	t.Helper()
	if raw, err := os.ReadFile(filepath.Clean(unit)); err != nil || string(raw) != content {
		t.Errorf("the unit of the main instance's executor was rewritten: %q (%v)", raw, err)
	}
	for _, marker := range []string{"--user stop", "enable --now", "daemon-reload"} {
		if got := h.f.find(marker); len(got) != 0 {
			t.Errorf("the main instance's executor was stopped, started or reloaded (%s): %v", marker, got)
		}
	}
}

// secondInstance is the stack of a second instance, which has its executor token already, beside the executor of the main instance that the default token file leads to.
func secondInstance(t *testing.T) (u *upgradeHarness, unit, content string) {
	t.Helper()
	u = newUpgradeHarness(t, nil)
	u.have(kindSecret, SecretExecutorAuth)
	unit, content = mainInstanceExecutor(t, u.harness)
	return u, unit, content
}

func installOfAnotherInstance(u *upgradeHarness, args ...string) error {
	all := append([]string{"--settings", u.settingsPath(), "--runtime", "podman", "--connection", "pm", "--yes"}, args...) // no --token-file
	return Install(context.Background(), all, u.env())
}

// assertAsksForATokenFile wants the refusal: it names --token-file, says that nothing was changed, and gives the way out of a sole instance with a settings
// file of its own: --token-file with the default token file, which is its own.
func assertAsksForATokenFile(t *testing.T, u *upgradeHarness, err error) {
	t.Helper()
	defaultFile, pathErr := defaultTokenFile(u.f.deps())
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if err == nil || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Errorf("got %v, want a refusal that says that nothing was changed", err)
		return
	}
	for _, want := range []string{"--token-file", "the only instance on this computer", "--token-file " + quoteArg(defaultFile)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
}

func TestAnInstallOfAnotherSettingsFileWithoutATokenFileLeavesTheMainInstancesExecutorAlone(t *testing.T) {
	u, unit, content := secondInstance(t)
	assertAsksForATokenFile(t, u, installOfAnotherInstance(u, "--image", oldImage))
	assertMainExecutorUntouched(t, u.harness, unit, content)
	u.mustNotMutate()
}

func TestAnUpgradeOfAnotherSettingsFileWithoutATokenFileLeavesTheMainInstancesExecutorAlone(t *testing.T) {
	for name, image := range map[string]string{"at the installed release": oldImage, "to another release": testImage} {
		t.Run(name, func(t *testing.T) {
			u, unit, content := secondInstance(t)
			assertAsksForATokenFile(t, u, installOfAnotherInstance(u, "--upgrade", "--backup-dir", u.backups, "--image", image))
			assertMainExecutorUntouched(t, u.harness, unit, content)
			u.mustNotMutate()
			if files, _ := filepath.Glob(filepath.Join(u.backups, "*")); len(files) != 0 {
				t.Errorf("a backup was made before the refusal: %v", files)
			}
		})
	}
}

// The default settings file, named by its own path, is the main instance's: its default token file is its own, and the install goes on as it did.
func TestAnInstallThatNamesTheDefaultSettingsFileUsesTheDefaultTokenFile(t *testing.T) {
	h := newHarness(t, "linux")
	defaultToken := defaultTokenPlace(t, h)
	settings, err := DefaultSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--settings", settings, "--runtime", "podman", "--connection", "pm", "--image", testImage}
	if err := Install(context.Background(), args, h.newEnv()); err != nil {
		t.Fatal(err)
	}
	if !exists(defaultToken) {
		t.Errorf("the token copy is not at the default place %s", defaultToken)
	}
}

// The help of --token-file says what the install and the upgrade do with another settings file: they ask for it.
func TestTheUsageOfTheTokenFileSaysThatAnotherSettingsFileNeedsIt(t *testing.T) {
	h := newHarness(t, "linux")
	if err := Install(context.Background(), []string{"--help"}, h.newEnv()); err == nil {
		t.Fatal("Install --help must end with the help")
	}
	help := strings.Join(strings.Fields(h.out.String()), " ")
	if !strings.Contains(help, "for the default settings file only") || !strings.Contains(help, "another --settings file needs --token-file") {
		t.Errorf("the usage of --token-file does not say that another settings file needs it:\n%s", h.out.String())
	}
}

// The install that finds the test runner of another instance says which instance it is, from the list of installations, as doctor does.

// refusal is the install's words about the test runner of another instance, with who for the part after "another instance" and remove for the
// command that removes it; again is the install of this instance.
func refusal(who, remove, again string) string {
	return "a computer has one test runner, and yours belongs to another instance" + who + ". This instance left it as it is. To use this instance's own, remove the other instance with " +
		remove + ", then run `" + again + "` again"
}

func TestAnInstallThatFindsTheExecutorOfAnotherInstanceNamesTheInstanceThatHoldsIt(t *testing.T) {
	for name, c := range map[string]struct{ runtime, instance, who string }{
		"one other installation is known":  {RuntimeDocker, holderID, " (the instance `" + holderID + "`, on Docker)"},
		"the other installation has no id": {RuntimePodman, "", " (the instance with the settings file `%s`, on Podman)"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, "linux")
			mainInstanceExecutor(t, h) // the user unit names the main instance's program; the configuration folder is the test's own
			mainToken, err := defaultTokenFile(h.f.deps())
			if err != nil {
				t.Fatal(err)
			}
			holder := listedElsewhere(t, "linux", mainToken, c.runtime, c.instance)
			listedElsewhere(t, "linux", otherToken(t), RuntimePodman, bystanderID) // its program is not the one the unit names
			if err := h.podman(); err != nil {
				t.Fatalf("the stack is up and the install does not fail for the executor: %v", err)
			}
			who := c.who
			if c.instance == "" {
				who = strings.ReplaceAll(who, "%s", holder)
			}
			want := refusal(who, removing(holder, mainToken), (Deps{instance: h.named()}).suggest("install"))
			if out := h.out.String(); !strings.Contains(out, want) || strings.Contains(out, bystanderID) {
				t.Errorf("the output lacks %q, or names an instance that does not hold the test runner:\n%s", want, out)
			}
		})
	}
}

// On Windows the task is not read: several other installations that hold a test runner are listed, with the removal that leaves the flags to the person. This instance's
// own entry, which the install has just written, is not one of them.
func TestAnInstallOnWindowsListsTheInstancesThatMayHoldTheTestRunner(t *testing.T) {
	h := machineHarnessOn(t, "windows")
	defaultTokenPlace(t, h) // the configuration folder is the test's own
	h.first("/Query /TN SuiteWard Executor", "")
	first := listedElsewhere(t, "windows", owning(t, otherToken(t)), RuntimeDocker, holderID)
	second := listedElsewhere(t, "windows", owning(t, otherToken(t)), RuntimePodman, "")
	if err := h.podman(); err != nil {
		t.Fatalf("the stack is up and the install does not fail for the executor: %v", err)
	}
	who := " (one of the other instances on this computer: the instance `" + holderID + "`, on Docker; the instance with the settings file `" + second + "`, on Podman)"
	want := refusal(who, generalHint, (Deps{instance: h.named()}).suggest("install"))
	if out := h.out.String(); !strings.Contains(out, want) || strings.Contains(out, first) {
		t.Errorf("the output lacks %q, or names the settings file of an instance that has an id:\n%s", want, out)
	}
}

// On Windows a listed installation counts only when its own executor folder holds the wrapper and the task definition: the list keeps the last
// write, and an instance of an older release may have no entry, so the only other entry is not named by elimination.
func TestAnInstallOnWindowsNamesOnlyAnInstanceWhoseExecutorFolderHoldsATestRunner(t *testing.T) {
	for name, holderListed := range map[string]bool{"the other installation holds none": false, "one of two holds one": true} {
		t.Run(name, func(t *testing.T) {
			h := machineHarnessOn(t, "windows")
			defaultTokenPlace(t, h) // the configuration folder is the test's own
			h.first("/Query /TN SuiteWard Executor", "")
			listedElsewhere(t, "windows", otherToken(t), RuntimePodman, bystanderID)
			who, remove := "", generalHint
			if holderListed {
				token := owning(t, otherToken(t))
				holder := listedElsewhere(t, "windows", token, RuntimeDocker, holderID)
				who, remove = " (the instance `"+holderID+"`, on Docker)", removing(holder, token)
			}
			if err := h.podman(); err != nil {
				t.Fatalf("the stack is up and the install does not fail for the executor: %v", err)
			}
			want := refusal(who, remove, (Deps{instance: h.named()}).suggest("install"))
			if out := h.out.String(); !strings.Contains(out, want) || strings.Contains(out, bystanderID) {
				t.Errorf("the output lacks %q, or names an installation whose executor folder holds no test runner:\n%s", want, out)
			}
		})
	}
}

// Nothing in the list holds the test runner: the words are the ones the install always had.
func TestAnInstallThatFindsTheExecutorOfAnInstanceTheListDoesNotNameKeepsItsWords(t *testing.T) {
	h := newHarness(t, "linux")
	mainInstanceExecutor(t, h) // the unit names the main instance's program, which no entry of the list has
	listedElsewhere(t, "linux", otherToken(t), RuntimePodman, bystanderID)
	if err := h.podman(); err != nil {
		t.Fatalf("the stack is up and the install does not fail for the executor: %v", err)
	}
	want := refusal("", generalHint, (Deps{instance: h.named()}).suggest("install"))
	if out := h.out.String(); !strings.Contains(out, want) {
		t.Errorf("the output lacks %q:\n%s", want, out)
	}
}
