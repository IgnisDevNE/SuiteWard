package installer

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The incident of 2026-10-23: `uninstall --settings <a file that does not exist> --token-file <missing> --purge --yes` fell back to the
// defaults, found the default instance of the PC (Podman, the rootless machine) and purged it: its data, its TOTP enrollment and its App key
// were gone, and there was no backup. These tests pin what keeps that from happening again: a settings file that is named must exist, a
// command takes no destructive action on defaults that were not read from a settings file, and a purge needs the instance id, as a restore does.

// removals are the calls that delete something of the instance: the containers, the volumes, the network and the secrets.
func (h *harness) removals() []string {
	var out []string
	for _, c := range h.f.calls {
		line := normalized(c)
		for _, verb := range []string{"rm ", "volume rm", "network rm", "secret rm"} {
			if strings.HasPrefix(line, verb) {
				out = append(out, line)
			}
		}
	}
	return out
}

// intact fails when the run removed anything of the instance: its objects or the files that the harness wrote.
func (h *harness) intact() {
	h.t.Helper()
	if got := h.removals(); len(got) != 0 {
		h.t.Errorf("something was removed: %v", got)
	}
	if !exists(h.settingsPath()) || !exists(h.tokenPath()) {
		h.t.Error("the settings or the token copy were removed")
	}
}

// consoleOf is a terminal that types stdin, or a console that cannot ask when terminal is false.
func (h *harness) consoleOf(terminal bool, stdin string) Env {
	env := h.newEnv()
	if terminal {
		env.Term = Terminal{In: strings.NewReader(stdin), Out: &h.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, errors.New("no secret is asked for") }}
	}
	return env
}

// purgeHarness is the co-located stack with its files, whose service answers /status as the instance testInstance.
func purgeHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, "linux")
	podmanStackExists(h)
	h.writeFiles()
	h.status = statusIs(statusBody)
	return h
}

// uninstallOn runs the uninstall of the harness's stack on a terminal that types stdin (or none), with the engine named as the install named it.
func (h *harness) uninstallOn(terminal bool, stdin string, args ...string) error {
	h.t.Helper()
	base := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm"}
	return Uninstall(context.Background(), append(base, args...), h.consoleOf(terminal, stdin))
}

// ---- a settings file that is named must exist ----

func TestUninstallNamingASettingsFileThatDoesNotExistRemovesNothing(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		name := "without a terminal"
		if terminal {
			name = "on a terminal"
		}
		t.Run(name, func(t *testing.T) {
			h := purgeHarness(t) // the default instance of this PC is there, with its settings and its token copy
			scratch := filepath.Join(h.dir, "sw-scratch")
			missing, token := filepath.Join(scratch, "settings.yaml"), filepath.Join(scratch, "mcp-token")
			var stderr bytes.Buffer
			args := []string{"uninstall", "--settings", missing, "--token-file", token, "--purge", "--yes"}
			if code := Main(context.Background(), args, h.consoleOf(terminal, "yes\n"), &stderr); code != 1 || !strings.Contains(stderr.String(), missing) {
				t.Fatalf("exit %d, stderr %q: want 1 and the path that does not exist", code, stderr.String())
			}
			if len(h.f.calls) != 0 {
				t.Errorf("the runtime was called before the refusal: %v", h.f.lines())
			}
			if strings.Contains(h.out.String(), "Using Podman") {
				t.Errorf("the refusal came after the runtime was chosen:\n%s", h.out.String())
			}
			h.intact()
		})
	}
}

func TestCommandsThatReadSettingsRefuseANamedFileThatDoesNotExist(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"uninstall", []string{"uninstall", "--yes"}},
		{"uninstall --purge", []string{"uninstall", "--purge", "--yes"}},
		{"uninstall on the isolated tier", []string{"uninstall", "--tier", "isolated", "--yes"}},
		{"enroll", []string{"enroll", "--owner", "alice"}},
		{"backup init", []string{"backup", "init", "--yes"}},
		{"backup", []string{"backup"}},
		{"restore", []string{"restore", "backup.tar.age", "--yes", "--instance", testInstance}},
		{"install --upgrade", []string{"install", "--upgrade", "--yes", "--image", testImage}},
		{"install --upgrade on the isolated tier", []string{"install", "--upgrade", "--tier", "isolated", "--yes", "--image", testImage}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withTerminal(t, true) // enroll refuses a console that is not a terminal before it reads anything
			h := purgeHarness(t)
			missing := filepath.Join(h.dir, "nowhere", "settings.yaml")
			var stderr bytes.Buffer
			if code := Main(context.Background(), append(slices.Clone(tc.args), "--settings", missing), h.newEnv(), &stderr); code != 1 || !strings.Contains(stderr.String(), missing) {
				t.Fatalf("exit %d, stderr %q: want 1 and the path that does not exist", code, stderr.String())
			}
			if len(h.f.calls) != 0 {
				t.Errorf("the runtime was called before the refusal: %v", h.f.lines())
			}
			h.intact()
		})
	}
}

// doctor reads the settings first and reports the missing file as a failed check; it never reaches a runtime.
func TestDoctorOfASettingsFileThatDoesNotExistCallsNoRuntime(t *testing.T) {
	h := purgeHarness(t)
	missing := filepath.Join(h.dir, "nowhere", "settings.yaml")
	if code, _ := h.main("doctor", "--settings", missing); code != 1 || !strings.Contains(h.out.String(), missing) {
		t.Fatalf("exit %d, output:\n%s", code, h.out.String())
	}
	if len(h.f.calls) != 0 {
		t.Errorf("the runtime was called: %v", h.f.lines())
	}
}

// ---- no destructive action on defaults that were not read from a settings file ----

func TestUninstallWithNoSettingsFileNeedsTheEngineNamed(t *testing.T) {
	useDefaultConfigDir(t)
	path, err := DefaultSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--yes"},
		{"--purge", "--yes", "--instance", testInstance},
		{"--runtime", "podman", "--yes"}, // the connection decides which Podman machine, so it is named too
		{"--connection", "pm", "--yes"},
	} {
		h := purgeHarness(t) // the default instance of this PC runs, and the harness's settings are not at the default path
		var stderr bytes.Buffer
		if code := Main(context.Background(), append([]string{"uninstall", "--token-file", h.tokenPath()}, args...), h.newEnv(), &stderr); code != 1 || !strings.Contains(stderr.String(), path) {
			t.Fatalf("%v: exit %d, stderr %q: want 1 and the default settings path", args, code, stderr.String())
		}
		if len(h.f.calls) != 0 {
			t.Errorf("%v: the runtime was called: %v", args, h.f.lines())
		}
		h.intact()
	}
}

// The explicit way: a stack that has no settings file (a first install that failed, one that local.ps1 made and no upgrade converted) is
// removed when the command names its engine.
func TestUninstallWithNoSettingsFileRunsWhenTheEngineIsNamed(t *testing.T) {
	useDefaultConfigDir(t)
	h := newHarness(t, "linux")
	podmanStackExists(h)
	args := []string{"--runtime", "podman", "--connection", "pm", "--yes", "--token-file", h.tokenPath()}
	if err := Uninstall(context.Background(), args, h.newEnv()); err != nil {
		t.Fatal(err)
	}
	if len(h.f.find("rm --force --ignore --time 45 "+ContainerServe)) == 0 {
		t.Errorf("the service was not removed: %v", h.f.lines())
	}
}

// ---- a purge needs the instance id ----

func TestUninstallPurgeNeedsTheInstanceId(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		stdin    string
		terminal bool
		want     string // in the error, which names what to pass or type
	}{
		{"--yes alone", []string{"--purge", "--yes"}, "", false, testInstance},
		{"--yes alone on a terminal", []string{"--purge", "--yes"}, "", true, testInstance},
		{"another instance id", []string{"--purge", "--yes", "--instance", "00000000"}, "", false, testInstance},
		{"nothing to confirm with", []string{"--purge"}, "", false, "--yes"},
		{"yes typed on a terminal", []string{"--purge"}, "yes\n", true, testInstance},
		{"another instance id typed", []string{"--purge"}, "00000000\n", true, testInstance},
		{"an empty answer", []string{"--purge"}, "\n", true, testInstance},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := purgeHarness(t)
			err := h.uninstallOn(tc.terminal, tc.stdin, tc.args...)
			if err == nil || errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want a refusal that names %q", err, tc.want)
			}
			h.intact()
		})
	}
}

func TestUninstallPurgeWithTheInstanceIdRemovesTheState(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		stdin    string
		terminal bool
	}{
		{"--yes with --instance", []string{"--purge", "--yes", "--instance", testInstance}, "", false},
		{"--yes with --instance on a terminal", []string{"--purge", "--yes", "--instance", testInstance}, "", true},
		{"the id typed", []string{"--purge"}, testInstance + "\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := purgeHarness(t)
			if err := h.uninstallOn(tc.terminal, tc.stdin, tc.args...); err != nil {
				t.Fatal(err)
			}
			if got, want := h.removed("volume rm"), []string{VolumeData, VolumeDBData}; !slices.Equal(got, want) {
				t.Errorf("volumes removed: %v, want %v", got, want)
			}
			if exists(h.settingsPath()) || exists(h.tokenPath()) {
				t.Error("the settings or the token copy are still there")
			}
		})
	}
}

// Without --purge the question stays what it was: the data stays, and a yes is enough.
func TestUninstallWithoutPurgeKeepsItsConfirmation(t *testing.T) {
	h := purgeHarness(t)
	if err := h.uninstallOn(true, "yes\n"); err != nil {
		t.Fatalf("a yes on a terminal: %v", err)
	}
	h = purgeHarness(t)
	if err := h.uninstallOn(false, "", "--yes"); err != nil {
		t.Fatalf("--yes alone: %v", err)
	}
	if got := h.removed("volume rm"); len(got) != 0 {
		t.Errorf("volumes removed without --purge: %v", got)
	}
}

// The service that does not answer has its id in the database; one that cannot be read at all is named "unknown", as a backup of it is.
func TestUninstallPurgeReadsTheInstanceIdOfAServiceThatDoesNotAnswer(t *testing.T) {
	down := func(context.Context, string) ([]byte, error) { return nil, errors.New("connection refused") }
	t.Run("from the database", func(t *testing.T) {
		h := purgeHarness(t)
		h.status = down
		h.f.on("psql", "9f8e7d6c\n")
		if err := h.uninstallOn(false, "", "--purge", "--yes", "--instance", testInstance); err == nil || !strings.Contains(err.Error(), "9f8e7d6c") {
			t.Fatalf("got %v, want a refusal that names the id of the database", err)
		}
		h.intact()
		if err := h.uninstallOn(false, "", "--purge", "--yes", "--instance", "9f8e7d6c"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("nowhere", func(t *testing.T) {
		h := purgeHarness(t)
		h.status = down
		if err := h.uninstallOn(false, "", "--purge", "--yes"); err == nil || !strings.Contains(err.Error(), unknownValue) {
			t.Fatalf("got %v, want a refusal that names %q", err, unknownValue)
		}
		h.intact()
		if err := h.uninstallOn(false, "", "--purge", "--yes", "--instance", unknownValue); err != nil {
			t.Fatal(err)
		}
	})
}

// ---- what is removed is said before anything is ----

func TestUninstallSaysWhatItWillRemoveBeforeItAsks(t *testing.T) {
	h := purgeHarness(t)
	if err := h.uninstallOn(true, "n\n", "--purge"); err == nil { // an answer of no at the prompt: nothing is removed
		t.Fatal("an answer of no was not refused")
	}
	h.intact()
	said := slices.ContainsFunc(strings.Split(h.out.String(), "\n"), func(line string) bool {
		return strings.Contains(line, "Podman") && strings.Contains(line, testInstance) && strings.Contains(line, h.settingsPath())
	})
	if !said {
		t.Errorf("no line names the runtime, the instance id and the settings file:\n%s", h.out.String())
	}
}

func TestUninstallIsolatedPurgeNeedsTheInstanceIdAndSaysWhatItRemoves(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	err := u.uninstall("--purge", "--yes")
	if err == nil || errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), testInstance) {
		t.Fatalf("got %v, want a refusal that names the instance id", err)
	}
	for _, line := range u.f.lines() {
		if strings.HasPrefix(line, "stop ") || strings.Contains(line, "secret rm") || strings.Contains(line, "volume rm") {
			t.Errorf("something was removed before the refusal: %s", line)
		}
	}
	if !exists(settingsFile(u.home)) {
		t.Error("the settings file was removed")
	}
	said := slices.ContainsFunc(strings.Split(u.out.String(), "\n"), func(line string) bool {
		return strings.Contains(line, testInstance) && strings.Contains(line, settingsFile(u.home))
	})
	if !said {
		t.Errorf("no line names the instance id and the settings file:\n%s", u.out.String())
	}
	if err := u.uninstall("--purge", "--yes", "--instance", testInstance); err != nil {
		t.Fatal(err)
	}
	if exists(settingsFile(u.home)) {
		t.Error("the settings file is still there")
	}
}

// A restore replaces the data of the instance it finds: with no settings file at all it needs the engine named, as an uninstall does.
func TestRestoreWithNoSettingsFileNeedsTheEngineNamed(t *testing.T) {
	useDefaultConfigDir(t)
	path, err := DefaultSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	h := purgeHarness(t) // the default instance runs, and its settings are not at the default path
	code, stderr := h.main("restore", "backup.tar.age", "--tier", "co-located", "--yes", "--instance", testInstance)
	if code != 1 || !strings.Contains(stderr, path) {
		t.Fatalf("exit %d, stderr %q: want 1 and the default settings path", code, stderr)
	}
	if got := destructive(h.f, "\x00"); len(got) != 0 {
		t.Errorf("the engine was asked to change something: %v", got)
	}
}
