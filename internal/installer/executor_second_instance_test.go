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

func assertAsksForATokenFile(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "--token-file") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("got %v, want a refusal that names --token-file and says that nothing was changed", err)
	}
}

func TestAnInstallOfAnotherSettingsFileWithoutATokenFileLeavesTheMainInstancesExecutorAlone(t *testing.T) {
	u, unit, content := secondInstance(t)
	assertAsksForATokenFile(t, installOfAnotherInstance(u, "--image", oldImage))
	assertMainExecutorUntouched(t, u.harness, unit, content)
	u.mustNotMutate()
}

func TestAnUpgradeOfAnotherSettingsFileWithoutATokenFileLeavesTheMainInstancesExecutorAlone(t *testing.T) {
	for name, image := range map[string]string{"at the installed release": oldImage, "to another release": testImage} {
		t.Run(name, func(t *testing.T) {
			u, unit, content := secondInstance(t)
			assertAsksForATokenFile(t, installOfAnotherInstance(u, "--upgrade", "--backup-dir", u.backups, "--image", image))
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
	if !strings.Contains(help, "with the default settings file only") || !strings.Contains(help, "another --settings file needs --token-file") {
		t.Errorf("the usage of --token-file does not say that another settings file needs it:\n%s", h.out.String())
	}
}
