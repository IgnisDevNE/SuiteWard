package installer

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// The list of the installations (ADR 0029, section 7): installations.yaml beside the default settings file, written by install and install
// --upgrade where they save the settings, read by the commands that ask which installation, removed by a purge.

// tokenName is a token file of a test, as the list records it: absolute. No such file is made.
func tokenName(name string) string { return absolute(filepath.Join("/tokens", name)) }

// listFile is the path of the list in the configuration folder of the test.
func listFile(t *testing.T) string {
	t.Helper()
	path, err := installationsPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// listed reads the list of the test, which must be readable.
func listed(t *testing.T) []installation {
	t.Helper()
	list, err := loadInstallations(listFile(t))
	if err != nil {
		t.Fatal(err)
	}
	return list.Installations
}

// settingsOf saves the settings of an installation in a folder of its own and returns the file.
func settingsOf(t *testing.T, s Settings) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	return path
}

// defaultSettingsOf saves s as the default settings file of the test and returns it.
func defaultSettingsOf(t *testing.T, s Settings) string {
	t.Helper()
	path, err := DefaultSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTheListBesideTheDefaultSettingsFileTakesOneEntryAtATime(t *testing.T) {
	useDefaultConfigDir(t)
	first, second := settingsOf(t, colocatedSettings()), settingsOf(t, colocatedSettings())
	s := colocatedSettings()
	s.Connection = "pm"
	if err := recordInstallation(runtime.GOOS, first, tokenName("first"), s, testInstance); err != nil {
		t.Fatal(err)
	}
	if err := recordInstallation(runtime.GOOS, second, tokenName("second"), colocatedSettings(), ""); err != nil {
		t.Fatal(err)
	}
	want := installation{Settings: first, TokenFile: tokenName("first"), Tier: config.TierCoLocated, Runtime: RuntimePodman, Connection: "pm", Instance: testInstance, HTTPPort: 18081, MCPPort: 18091}
	if got := listed(t); len(got) != 2 || got[0] != want || got[1].Settings != second {
		t.Fatalf("the list is %+v, want the first installation as %+v, then the second", got, want)
	}
	// An entry is refreshed where it is: the same settings file is one entry, whatever else changed.
	moved := colocatedSettings()
	moved.HTTPPort, moved.MCPPort = 28081, 28091
	if err := recordInstallation(runtime.GOOS, first, tokenName("first"), moved, ""); err != nil {
		t.Fatal(err)
	}
	got := listed(t)
	if len(got) != 2 || got[0].Settings != first || got[0].HTTPPort != 28081 || got[0].MCPPort != 28091 {
		t.Fatalf("the list is %+v, want the first entry refreshed in place", got)
	}
	if got[0].Instance != testInstance {
		t.Errorf("a run that does not know the instance id erased %q, it must keep it", got[0].Instance)
	}
}

func TestTheListIsAPrivateYamlFileOfVersionOne(t *testing.T) {
	useDefaultConfigDir(t)
	if err := recordInstallation(runtime.GOOS, settingsOf(t, colocatedSettings()), tokenName("one"), colocatedSettings(), ""); err != nil {
		t.Fatal(err)
	}
	path := listFile(t)
	raw := readFile(t, path)
	if !strings.HasPrefix(raw, "version: 1\ninstallations:\n") {
		t.Errorf("the list is %q, want version 1 and a list installations", raw)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("the list has mode %v, %v: want 0600", info, err)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".tmp-*")); len(left) != 0 {
		t.Errorf("the atomic replace left %v", left)
	}
	for _, secret := range []string{"password", "secret", "BEGIN"} {
		if strings.Contains(strings.ToLower(raw), strings.ToLower(secret)) {
			t.Errorf("the list holds %q: it is paths and ports only", secret)
		}
	}
}

func TestAnEntryIsKeyedByItsAbsoluteSettingsFile(t *testing.T) {
	useDefaultConfigDir(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join("tmp-installations-test", "settings.yaml")
	if err := recordInstallation(runtime.GOOS, relative, "", colocatedSettings(), ""); err != nil {
		t.Fatal(err)
	}
	if got := listed(t); len(got) != 1 || got[0].Settings != filepath.Join(cwd, relative) {
		t.Fatalf("the list is %+v, want the settings file as an absolute path", got)
	}
	for name, c := range map[string]struct {
		goos, a, b string
		want       bool
	}{
		"the same file":           {"linux", "/a/settings.yaml", "/a/./settings.yaml", true},
		"another file":            {"linux", "/a/settings.yaml", "/b/settings.yaml", false},
		"case differs on linux":   {"linux", "/a/Settings.yaml", "/a/settings.yaml", false},
		"case differs on windows": {"windows", "/a/Settings.yaml", "/a/settings.yaml", true},
	} {
		if got := samePath(c.goos, c.a, c.b); got != c.want {
			t.Errorf("%s: samePath = %v, want %v", name, got, c.want)
		}
	}
}

func TestAPurgeTakesItsEntryOutAndLeavesTheOthers(t *testing.T) {
	useDefaultConfigDir(t)
	first, second := settingsOf(t, colocatedSettings()), settingsOf(t, colocatedSettings())
	for _, path := range []string{first, second} {
		if err := recordInstallation(runtime.GOOS, path, tokenName("x"), colocatedSettings(), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := forgetInstallation(runtime.GOOS, first); err != nil {
		t.Fatal(err)
	}
	if got := listed(t); len(got) != 1 || got[0].Settings != second {
		t.Fatalf("the list is %+v, want only the second installation", got)
	}
	if err := forgetInstallation(runtime.GOOS, first); err != nil {
		t.Errorf("forgetting an installation that is not listed is not a failure: %v", err)
	}
	if got := listed(t); len(got) != 1 {
		t.Errorf("the list is %+v after forgetting twice", got)
	}
}

func TestAListThatCannotBeReadIsNeverReplaced(t *testing.T) {
	useDefaultConfigDir(t)
	path := listFile(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"not yaml":        "installations: [this is: not\n",
		"a newer version": "version: 2\ninstallations: []\n",
		"an unknown key":  "version: 1\ninstallations: []\nsecret: 1\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		err := recordInstallation(runtime.GOOS, settingsOf(t, colocatedSettings()), "", colocatedSettings(), "")
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: recordInstallation = %v, want a refusal that names %s", name, err, path)
		}
		if got := readFile(t, path); got != content {
			t.Errorf("%s: the list was replaced by %q", name, got)
		}
		if _, _, err := installationCandidates(runtime.GOOS); err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: installationCandidates = %v, want a refusal that names %s", name, err, path)
		}
	}
}

func TestTheCandidatesAreTheInstallationsWhoseSettingsFileExistsAndTheDefaultOneAlways(t *testing.T) {
	useDefaultConfigDir(t)
	live, gone := settingsOf(t, colocatedSettings()), settingsOf(t, colocatedSettings())
	for _, path := range []string{live, gone} {
		if err := recordInstallation(runtime.GOOS, path, tokenName("x"), colocatedSettings(), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	t.Run("no default settings file: the live entry, and the stale one counted", func(t *testing.T) {
		found, stale, err := installationCandidates(runtime.GOOS)
		if err != nil || stale != 1 || len(found) != 1 || found[0].Settings != live || found[0].isDefault {
			t.Fatalf("candidates = %+v, %d stale, %v: want the live entry alone and one stale", found, stale, err)
		}
	})
	t.Run("the default settings file is a candidate with no entry, and comes first", func(t *testing.T) {
		s := colocatedSettings()
		s.Connection, s.HTTPPort, s.MCPPort = "pm", 27081, 27091
		def := defaultSettingsOf(t, s)
		found, stale, err := installationCandidates(runtime.GOOS)
		if err != nil || stale != 1 || len(found) != 2 {
			t.Fatalf("candidates = %+v, %d stale, %v: want the default and the live entry", found, stale, err)
		}
		want := installation{Settings: def, Tier: config.TierCoLocated, Runtime: RuntimePodman, Connection: "pm", HTTPPort: 27081, MCPPort: 27091}
		if !found[0].isDefault || found[0].installation != want || found[1].Settings != live {
			t.Errorf("candidates = %+v, want the default as %+v first, then %s", found, want, live)
		}
	})
	t.Run("an entry of the default settings file is one candidate", func(t *testing.T) {
		def := defaultSettingsOf(t, colocatedSettings())
		if err := recordInstallation(runtime.GOOS, def, tokenName("default"), colocatedSettings(), testInstance); err != nil {
			t.Fatal(err)
		}
		found, _, err := installationCandidates(runtime.GOOS)
		if err != nil || len(found) != 2 || !found[0].isDefault || found[0].Instance != testInstance || found[0].TokenFile != tokenName("default") {
			t.Fatalf("candidates = %+v, %v: want the default once, with what its entry knows", found, err)
		}
	})
}
