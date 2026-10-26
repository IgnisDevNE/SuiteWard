package installer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The live run of 2026-10-26 (WIN-10): `uninstall --settings <second instance> --purge`, with no --token-file, removed the DEFAULT token file,
// which belongs to the main instance of the user, and left the second instance's own. The default token file is this instance's only when the
// settings file in use is the default one, or when --token-file names it; with any other settings file a purge keeps it and says so. What a
// purge deletes is said before it acts, on the page, at the prompt and with --yes.

// shortDir is a directory of the test with a short path: a page wraps the paths that it names, and long ones trim it.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// pageHarness is purgeHarness with files at a short path.
func pageHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, "linux")
	h.dir = shortDir(t)
	podmanStackExists(h)
	h.writeFiles()
	return h
}

// defaultTokenPlace points the per-user directories at one of the test's own, and gives the default place of the token file in it: a purge
// that reached a real file would be the incident again.
func defaultTokenPlace(t *testing.T, h *harness) string {
	t.Helper()
	dir := shortDir(t)
	for _, key := range []string{"APPDATA", "XDG_CONFIG_HOME", "HOME"} {
		t.Setenv(key, dir)
	}
	h.f.env["LOCALAPPDATA"] = dir // where the Windows default is
	path, err := defaultTokenFile(h.f.deps())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, dir) {
		t.Fatalf("the default token file %s is outside the test's configuration directory %s: a purge would delete a real file", path, dir)
	}
	return path
}

// mainInstanceToken is the token copy of the main instance of the user, at its default place.
func mainInstanceToken(t *testing.T, h *harness) string {
	t.Helper()
	path := defaultTokenPlace(t, h)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// secondInstanceArgs purge the stack of the harness by its settings file alone: no --token-file.
func (h *harness) secondInstanceArgs(extra ...string) []string {
	return append([]string{"--settings", h.settingsPath(), "--runtime", "podman", "--connection", "pm", "--purge"}, extra...)
}

func TestThePurgeOfAnotherSettingsFileKeepsTheDefaultTokenFileAndSaysSo(t *testing.T) {
	h := purgeHarness(t)
	mainToken := mainInstanceToken(t, h)
	if err := Uninstall(context.Background(), h.secondInstanceArgs("--yes", "--instance", testInstance), h.newEnv()); err != nil {
		t.Fatal(err)
	}
	if exists(h.settingsPath()) {
		t.Error("the purge did not run: the settings file is still there")
	}
	if !exists(mainToken) {
		t.Error("the purge removed the default token file, which may belong to another instance")
	}
	out := h.out.String()
	for _, want := range []string{"Kept the MCP token file " + mainToken, "--token-file"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "the network, "+mainToken) || strings.Contains(out, "the network and "+mainToken) {
		t.Errorf("the closing line says that the kept file was removed:\n%s", out)
	}
}

// --token-file names the file of this instance: that file goes, and the default one of another instance stays.
func TestThePurgeWithATokenFileRemovesExactlyThatFile(t *testing.T) {
	h := purgeHarness(t)
	mainToken := mainInstanceToken(t, h)
	if err := h.uninstallOn(false, "", "--purge", "--yes", "--instance", testInstance); err != nil {
		t.Fatal(err)
	}
	if exists(h.tokenPath()) {
		t.Error("the token file that --token-file names is still there")
	}
	if !exists(mainToken) {
		t.Error("the purge removed the default token file as well")
	}
}

// With the default settings file, the default token file is the instance's own: the purge removes it, as it always did.
func TestThePurgeOfTheDefaultSettingsRemovesTheDefaultTokenFile(t *testing.T) {
	h := newHarness(t, "linux")
	podmanStackExists(h)
	mainToken := mainInstanceToken(t, h)
	settings, err := DefaultSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	s := colocatedSettings()
	s.Connection = "pm"
	if err := SaveSettings(settings, s); err != nil {
		t.Fatal(err)
	}
	args := []string{"--runtime", "podman", "--connection", "pm", "--purge", "--yes", "--instance", testInstance}
	if err := Uninstall(context.Background(), args, h.newEnv()); err != nil {
		t.Fatal(err)
	}
	if exists(settings) || exists(mainToken) {
		t.Errorf("the settings file (%v) or the token file (%v) of the default instance are still there", exists(settings), exists(mainToken))
	}
}

// ---- what a purge deletes is said before it acts ----

// purgeLists are the sentences of the list that a purge shows: what it deletes, whatever its token file.
var purgeLists = []string{"This purge deletes", VolumeData, VolumeDBData, "the secrets", "the network " + NetworkName, "the settings file"}

// with is the list and some more sentences.
func with(list []string, more ...string) []string { return append(slices.Clone(list), more...) }

func TestThePlainPurgeListsWhatItDeletesBeforeItAsks(t *testing.T) {
	h := purgeHarness(t)
	if err := h.uninstallOn(true, testInstance+"\n", "--purge"); err != nil {
		t.Fatal(err)
	}
	out := h.out.String()
	prompt := strings.Index(out, "Type the instance id")
	for _, want := range with(purgeLists, h.settingsPath(), "the MCP token file "+h.tokenPath()) {
		if at := strings.Index(out, want); at < 0 || at > prompt {
			t.Errorf("%q is not said before the prompt (at %d, the prompt at %d):\n%s", want, at, prompt, out)
		}
	}
	if strings.Contains(out, "is kept: it may belong") {
		t.Errorf("a token file that is removed is said to be kept:\n%s", out)
	}
}

func TestThePlainPurgeThatKeepsTheDefaultTokenFileSaysSoBeforeItAsks(t *testing.T) {
	h := purgeHarness(t)
	mainToken := mainInstanceToken(t, h)
	if err := Uninstall(context.Background(), h.secondInstanceArgs(), h.consoleOf(true, testInstance+"\n")); err != nil {
		t.Fatal(err)
	}
	out := h.out.String()
	prompt := strings.Index(out, "Type the instance id")
	for _, want := range with(purgeLists, h.settingsPath(), "The MCP token file "+mainToken+" is kept", "--token-file <path>") {
		if at := strings.Index(out, want); at < 0 || at > prompt {
			t.Errorf("%q is not said before the prompt (at %d, the prompt at %d):\n%s", want, at, prompt, out)
		}
	}
	if strings.Contains(out, "the MCP token file "+mainToken+"\n") {
		t.Errorf("the kept token file is listed among what is deleted:\n%s", out)
	}
}

// The words of the list that a person reads on the page, as the page wraps them.
func TestThePurgePageListsWhatItDeletesAndTheTokenFileItRemoves(t *testing.T) {
	h := pageHarness(t)
	run := newScreenRun(termWidth, termHeight, testInstance+"\n", "\n")
	args := h.screenArgs("--runtime", "podman", "--purge")
	if err := Uninstall(liveContext(t), args, run.on(h.newEnv())); err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, run.output())
	}
	v := run.question(t, 0)
	mustSay(t, v, with(purgeLists, "the MCP token file")...)
	if strings.Contains(v.text(), "is kept: it may belong") || strings.Contains(v.text(), trimmed) {
		t.Errorf("the page keeps a token file that it removes, or is trimmed:\n%s", v.dump())
	}
	if strings.Contains(v.text(), "?") {
		t.Errorf("the page asks a question besides the typed id:\n%s", v.dump())
	}
}

func TestThePurgePageSaysWhichTokenFileItKeeps(t *testing.T) {
	h := pageHarness(t)
	mainInstanceToken(t, h)
	run := newScreenRun(termWidth, termHeight, testInstance+"\n", "\n")
	if err := Uninstall(liveContext(t), h.secondInstanceArgs(), run.on(h.newEnv())); err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, run.output())
	}
	v := run.question(t, 0)
	mustSay(t, v, with(purgeLists, "is kept", "may belong to another instance", "--token-file <path>")...)
	if strings.Contains(v.text(), "the MCP token file") || strings.Contains(v.text(), trimmed) {
		t.Errorf("the page lists the kept token file among what is deleted, or is trimmed:\n%s", v.dump())
	}
}

// --yes asks nothing, so the list is shown before the first removal: as lines in plain mode, and as the page on a full screen, which is on
// the screen while the removal runs.
func TestThePurgeByYesListsWhatItDeletesBeforeItActs(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		h := purgeHarness(t)
		if err := h.uninstallOn(false, "", "--purge", "--yes", "--instance", testInstance); err != nil {
			t.Fatal(err)
		}
		out := h.out.String()
		acted := strings.Index(out, "Removed the containers")
		for _, want := range with(purgeLists, h.settingsPath(), "the MCP token file "+h.tokenPath()) {
			if at := strings.Index(out, want); at < 0 || at > acted {
				t.Errorf("%q is not said before the removal (at %d, the removal at %d):\n%s", want, at, acted, out)
			}
		}
	})
	t.Run("full screen", func(t *testing.T) {
		h := pageHarness(t)
		run := newScreenRun(termWidth, termHeight, "\n")
		args := h.screenArgs("--runtime", "podman", "--purge", "--yes", "--instance", testInstance)
		if err := Uninstall(liveContext(t), args, run.on(h.newEnv())); err != nil {
			t.Fatalf("Uninstall: %v\n%s", err, run.output())
		}
		out := run.output()
		drawn := out[:strings.LastIndex(out, leaveAlt)] // the frames, before the screen was left
		scrollback := strings.Join(strings.Fields(run.console.tail()), " ")
		for _, want := range with(purgeLists, "the MCP token file "+h.tokenPath()) {
			if !strings.Contains(drawn, want) {
				t.Errorf("no frame drew %q:\n%s", want, drawn)
			}
			if !strings.Contains(scrollback, want) { // the page is gone when the command ends: what stays on the screen is the scrollback
				t.Errorf("the scrollback lacks %q:\n%s", want, run.console.tail())
			}
		}
	})
	t.Run("full screen, a token file that is kept", func(t *testing.T) {
		h := pageHarness(t)
		mainToken := mainInstanceToken(t, h)
		run := newScreenRun(termWidth, termHeight, "\n")
		if err := Uninstall(liveContext(t), h.secondInstanceArgs("--yes", "--instance", testInstance), run.on(h.newEnv())); err != nil {
			t.Fatalf("Uninstall: %v\n%s", err, run.output())
		}
		scrollback := strings.Join(strings.Fields(run.console.tail()), " ")
		for _, want := range with(purgeLists, "The MCP token file "+mainToken+" is kept", "--token-file <path>") {
			if !strings.Contains(scrollback, want) {
				t.Errorf("the scrollback lacks %q:\n%s", want, run.console.tail())
			}
		}
	})
}

// A GitHub App that an install was setting up has a record beside the settings file, which a purge deletes too: the list names it when it is
// there, and only then.
func TestThePurgeListsTheRecordOfAGitHubAppOnlyWhenThereIsOne(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record bool
	}{{"with a record", true}, {"without one", false}} {
		t.Run(tc.name, func(t *testing.T) {
			h := purgeHarness(t)
			pending := pendingAppPath(h.settingsPath())
			if tc.record {
				if err := os.WriteFile(pending, []byte("app_id: 1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.uninstallOn(false, "", "--purge", "--yes", "--instance", testInstance); err != nil {
				t.Fatal(err)
			}
			line := "the record of the GitHub App that is being set up, " + pending
			if said := strings.Contains(h.out.String(), line); said != tc.record {
				t.Errorf("the record is there: %v, the list names it: %v:\n%s", tc.record, said, h.out.String())
			}
			if exists(pending) {
				t.Error("the record is still there after the purge")
			}
		})
	}
}

// ---- a token file that is kept but not there is not mentioned ----

// With another settings file and no --token-file, the default token file is kept: when there is no such file, there is nothing to say about it.
func TestThePurgeSaysNothingOfAKeptTokenFileThatIsNotThere(t *testing.T) {
	const aboutIt = "MCP token file"
	t.Run("plain", func(t *testing.T) {
		h := purgeHarness(t)
		defaultTokenPlace(t, h) // the default place is in the test's directory, and nothing is there
		if err := Uninstall(context.Background(), h.secondInstanceArgs("--yes", "--instance", testInstance), h.newEnv()); err != nil {
			t.Fatal(err)
		}
		if out := h.out.String(); strings.Contains(out, aboutIt) || strings.Contains(out, "--token-file") {
			t.Errorf("the output speaks of a token file that is not there:\n%s", out)
		}
	})
	t.Run("full screen", func(t *testing.T) {
		h := pageHarness(t)
		defaultTokenPlace(t, h)
		run := newScreenRun(termWidth, termHeight, testInstance+"\n", "\n")
		if err := Uninstall(liveContext(t), h.secondInstanceArgs(), run.on(h.newEnv())); err != nil {
			t.Fatalf("Uninstall: %v\n%s", err, run.output())
		}
		if text := run.question(t, 0).text(); strings.Contains(text, aboutIt) || strings.Contains(text, "--token-file") {
			t.Errorf("the page speaks of a token file that is not there:\n%s", text)
		}
		if tail := run.console.tail(); strings.Contains(tail, aboutIt) || strings.Contains(tail, "--token-file") {
			t.Errorf("the scrollback speaks of a token file that is not there:\n%s", tail)
		}
	})
}

// ---- --settings that names the default settings file ----

// The main instance's own purge may name its default settings file: that is still the default settings file, so its default token file is its own.
func TestThePurgeThatNamesTheDefaultSettingsFileRemovesTheDefaultTokenFile(t *testing.T) {
	for _, tc := range []struct {
		name   string
		goos   string
		id     string              // the instance id the purge is confirmed with: a Windows host reads /status inside the Podman machine, which the fake does not answer
		spell  func(string) string // how --settings spells the default path
		caseOK bool                // the spelling needs a file system that ignores case
	}{
		{"as it is, unix", "linux", testInstance, func(p string) string { return p }, false},
		{"as it is, windows", "windows", unknownValue, func(p string) string { return p }, false},
		{"not cleaned, unix", "linux", testInstance, func(p string) string {
			return filepath.Dir(p) + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(p)
		}, false},
		{"in another case, windows", "windows", unknownValue, strings.ToUpper, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.goos)
			podmanStackExists(h)
			mainToken := mainInstanceToken(t, h)
			settings, err := DefaultSettingsPath()
			if err != nil {
				t.Fatal(err)
			}
			s := colocatedSettings()
			s.Connection = "pm"
			if err := SaveSettings(settings, s); err != nil {
				t.Fatal(err)
			}
			spelled := tc.spell(settings)
			if tc.caseOK && !settingsExist(spelled) {
				t.Skip("this file system tells the file apart by case: the spelling does not name it")
			}
			args := []string{"--settings", spelled, "--runtime", "podman", "--connection", "pm", "--purge", "--yes", "--instance", tc.id}
			if err := Uninstall(context.Background(), args, h.newEnv()); err != nil {
				t.Fatal(err)
			}
			if exists(settings) || exists(mainToken) {
				t.Errorf("the settings file (%v) or the default token file (%v) of the main instance are still there", exists(settings), exists(mainToken))
			}
		})
	}
}

// The case of the path tells settings files apart on a unix-style system and not on Windows: what the harness cannot show on a file system of
// the other kind.
func TestSettingsNamedByPathAreTheDefaultFileByTheRulesOfTheSystem(t *testing.T) {
	useDefaultConfigDir(t)
	def, err := DefaultSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	sep := string(filepath.Separator)
	for _, tc := range []struct {
		name, goos, path string
		want             bool
	}{
		{"as it is", "linux", def, true},
		{"not cleaned", "linux", filepath.Dir(def) + sep + "." + sep + filepath.Base(def), true},
		{"in another case, unix", "linux", strings.ToUpper(def), false},
		{"in another case, windows", "windows", strings.ToUpper(def), true},
		{"another file of the same directory", "windows", filepath.Join(filepath.Dir(def), "other.yaml"), false},
		{"a relative path", "linux", filepath.Base(def), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := isDefaultSettings(tc.goos, tc.path, false); err != nil || got != tc.want {
				t.Errorf("isDefaultSettings(%q, %q) = %v, %v, want %v", tc.goos, tc.path, got, err, tc.want)
			}
		})
	}
}

// ---- --settings that names the default settings file is guarded as the default one ----

// The default settings file belongs to the main instance of the user. A purge that names it with --settings, in whatever spelling, takes the
// same guard as one that leaves --settings out: another runtime or another connection than the one the file records is another instance's
// command, and it refuses before anything is removed.
func TestThePurgeThatNamesTheDefaultSettingsFileOfAnotherInstanceRefusesAndRemovesNothing(t *testing.T) {
	sep := string(filepath.Separator)
	exact := func(p string) string { return p }
	podmanMain := func(s *Settings) { s.Connection = "pm" } // the main instance is Podman, on the connection pm
	dockerMain := func(s *Settings) { s.Runtime, s.Connection = RuntimeDocker, "" }
	for _, tc := range []struct {
		name   string
		goos   string
		docker bool // the command runs against Docker: the harness is Docker's, with the stack in it
		main   func(*Settings)
		flags  []string
		id     string              // the instance id that confirms the purge: a Windows host reads /status inside the Podman machine, which the fake does not answer
		spell  func(string) string // how --settings spells the default path
	}{
		{"the main instance is Docker, the command Podman", "linux", false, dockerMain, []string{"--runtime", "podman", "--connection", "pm"}, testInstance, exact},
		{"the main instance is Podman, the command Docker", "linux", true, podmanMain, []string{"--runtime", "docker"}, testInstance, exact},
		{"another connection", "linux", false, podmanMain, []string{"--runtime", "podman", "--connection", "elsewhere"}, testInstance, exact},
		{"not cleaned", "linux", false, dockerMain, []string{"--runtime", "podman", "--connection", "pm"}, testInstance,
			func(p string) string { return filepath.Dir(p) + sep + "." + sep + filepath.Base(p) }},
		{"on Windows", "windows", false, dockerMain, []string{"--runtime", "podman", "--connection", "pm"}, unknownValue, exact},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.goos)
			if tc.docker {
				h = dockerHarness(t, tc.goos)
				h.have(KindContainer, ContainerDB, ContainerServe)
				h.have(KindNetwork, NetworkName)
				h.have(KindVolume, VolumeData, VolumeDBData)
				h.status = statusIs(statusBody)
			} else {
				podmanStackExists(h)
			}
			mainToken := mainInstanceToken(t, h)
			settings, err := DefaultSettingsPath()
			if err != nil {
				t.Fatal(err)
			}
			s := colocatedSettings()
			tc.main(&s)
			if err := SaveSettings(settings, s); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"--settings", tc.spell(settings), "--purge", "--yes", "--instance", tc.id}, tc.flags...)
			err = Uninstall(context.Background(), args, h.newEnv())
			if err == nil || !strings.Contains(err.Error(), "the default settings file") {
				t.Fatalf("got %v, want the refusal of a command that would take over the default settings file", err)
			}
			if got := h.removals(); len(got) != 0 {
				t.Errorf("something was removed: %v", got)
			}
			if !exists(settings) || !exists(mainToken) {
				t.Errorf("the settings file (%v) or the default token file (%v) of the main instance were removed", exists(settings), exists(mainToken))
			}
		})
	}
}
