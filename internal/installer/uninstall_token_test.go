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

// mainInstanceToken is the token copy of the main instance of the user, at the default place of a configuration directory that is the test's
// own: a purge that reached a real file would be the incident again.
func mainInstanceToken(t *testing.T, h *harness) string {
	t.Helper()
	dir := shortDir(t)
	for _, key := range []string{"APPDATA", "XDG_CONFIG_HOME", "HOME"} {
		t.Setenv(key, dir)
	}
	path, err := defaultTokenFile(h.f.deps())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, dir) {
		t.Fatalf("the default token file %s is outside the test's configuration directory %s: a purge would delete a real file", path, dir)
	}
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
	if strings.Contains(v.text(), "deletes the MCP token file") || strings.Contains(v.text(), trimmed) {
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
		for _, want := range with(purgeLists, "the MCP token file "+h.tokenPath()) {
			if !strings.Contains(drawn, want) {
				t.Errorf("no frame drew %q:\n%s", want, drawn)
			}
		}
	})
}
