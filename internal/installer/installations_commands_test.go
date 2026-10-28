package installer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// Who writes the list (ADR 0029, section 7): install and install --upgrade, where they save the settings file; uninstall --purge removes the entry
// with the settings file; nothing else writes it.

// entryOf is the entry of a settings file in the list of the test, which must be there.
func entryOf(t *testing.T, settingsPath string) installation {
	t.Helper()
	for _, e := range listed(t) {
		if samePath(runtime.GOOS, e.Settings, settingsPath) {
			return e
		}
	}
	t.Fatalf("the list holds no entry of %s: %+v", settingsPath, listed(t))
	return installation{}
}

func TestAnInstallListsTheInstallation(t *testing.T) {
	useDefaultConfigDir(t)
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	saved, err := LoadSettings(h.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	want := installation{Settings: absolute(h.settingsPath()), TokenFile: absolute(h.tokenPath()), Tier: config.TierCoLocated, Runtime: RuntimePodman, Connection: "pm", HTTPPort: saved.HTTPPort, MCPPort: saved.MCPPort}
	if got := entryOf(t, h.settingsPath()); got != want {
		t.Errorf("the entry is %+v, want %+v (the instance id is not known while the service is not up)", got, want)
	}
}

func TestAnUpgradeListsTheInstallationWithItsInstanceId(t *testing.T) {
	t.Run("an upgrade that replaces the service", func(t *testing.T) {
		useDefaultConfigDir(t)
		u := newUpgradeHarness(t, nil)
		if err := u.upgrade("--image", testImage, "--yes"); err != nil {
			t.Fatal(err)
		}
		got := entryOf(t, u.settingsPath())
		if got.Instance != testInstance || got.TokenFile != absolute(u.tokenPath()) || got.HTTPPort != u.port || got.Tier != config.TierCoLocated {
			t.Errorf("the entry is %+v, want the instance %s, the token file, the port %d and the co-located tier", got, testInstance, u.port)
		}
	})
	t.Run("an upgrade that finds nothing to do still lists an installation that was made before", func(t *testing.T) {
		useDefaultConfigDir(t)
		u := newUpgradeHarness(t, noRecipient)
		if err := u.upgradeAt(false, "", "--yes", "--image", oldImage); err != nil {
			t.Fatal(err)
		}
		if got := entryOf(t, u.settingsPath()); got.TokenFile != absolute(u.tokenPath()) {
			t.Errorf("the entry is %+v, want it to hold the token file", got)
		}
	})
}

func TestTheIsolatedInstallAndUpgradeListTheInstallation(t *testing.T) {
	t.Run("install", func(t *testing.T) {
		useDefaultConfigDir(t)
		home := t.TempDir()
		f := newIsoFake(home)
		f.noImage = true
		if _, err := runIsolated(t, f, nil, ""); err != nil {
			t.Fatal(err)
		}
		got := entryOf(t, settingsFile(home))
		if got.Tier != config.TierIsolated || got.Runtime != RuntimePodman || got.TokenFile != "" {
			t.Errorf("the entry is %+v, want the isolated tier on Podman and no token file", got)
		}
	})
	for name, image := range map[string]string{"an upgrade": testImage, "an upgrade at the installed release": oldImage} {
		t.Run(name, func(t *testing.T) {
			useDefaultConfigDir(t)
			u := newIsoUpgrade(t, nil)
			if err := u.upgrade("--image", image, "--yes"); err != nil {
				t.Fatalf("%v\n%s", err, u.out.String())
			}
			got := entryOf(t, settingsFile(u.home))
			if got.Tier != config.TierIsolated || got.HTTPPort != u.port {
				t.Errorf("the entry is %+v, want the isolated tier and the port %d", got, u.port)
			}
			if image == testImage && got.Instance != testInstance {
				t.Errorf("the entry is %+v, want the instance %s that /status showed before the service was replaced", got, testInstance)
			}
		})
	}
}

// A list that cannot be written is a warning: the installation works, and its entry returns at the next upgrade.
func TestAListThatCannotBeWrittenDoesNotFailAnInstall(t *testing.T) {
	useDefaultConfigDir(t)
	path := listFile(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const broken = "installations: [this is: not\n"
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatalf("an install that worked failed for its list: %v", err)
	}
	if !strings.Contains(h.out.String(), path) || !strings.Contains(h.out.String(), "The installation itself works") {
		t.Errorf("the output does not warn about the list %s:\n%s", path, h.out.String())
	}
	if got := readFile(t, path); got != broken {
		t.Errorf("the list was replaced by %q", got)
	}
	if !exists(h.settingsPath()) {
		t.Error("the settings were not saved")
	}
}

func TestOnlyAPurgeTakesTheEntryOut(t *testing.T) {
	useDefaultConfigDir(t)
	h := purgeHarness(t)
	other := settingsOf(t, colocatedSettings())
	for _, path := range []string{h.settingsPath(), other} {
		if err := recordInstallation(runtime.GOOS, path, h.tokenPath(), colocatedSettings(), testInstance); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.uninstall("--runtime", "podman", "--connection", "pm", "--yes"); err != nil {
		t.Fatal(err)
	}
	if got := listed(t); len(got) != 2 {
		t.Fatalf("a plain uninstall changed the list: %+v", got)
	}
	if err := h.uninstall("--runtime", "podman", "--connection", "pm", "--purge", "--yes", "--instance", testInstance); err != nil {
		t.Fatal(err)
	}
	if got := listed(t); len(got) != 1 || got[0].Settings != absolute(other) {
		t.Errorf("the list is %+v after the purge, want only the other installation", got)
	}
}

func TestAnIsolatedPurgeTakesTheEntryOut(t *testing.T) {
	useDefaultConfigDir(t)
	u := newIsoUpgrade(t, nil)
	if err := recordInstallation(runtime.GOOS, settingsFile(u.home), "", isolatedSettings(), testInstance); err != nil {
		t.Fatal(err)
	}
	if err := u.uninstall("--purge", "--yes", "--instance", testInstance); err != nil {
		t.Fatal(err)
	}
	if got := listed(t); len(got) != 0 {
		t.Errorf("the list is %+v after the purge, want none", got)
	}
}

// Nothing else writes it: a recovery key changes the settings file and not the entry, and an installation that was never listed joins at its upgrade.
func TestBackupInitDoesNotWriteTheList(t *testing.T) {
	useDefaultConfigDir(t)
	u := bareInstance(t)
	kit := filepath.Join(t.TempDir(), "kit.txt")
	if err := u.backupInit("--kit", kit, "--yes"); err != nil {
		t.Fatal(err)
	}
	if exists(listFile(t)) {
		t.Errorf("backup init wrote the list:\n%s", readFile(t, listFile(t)))
	}
}

// A chosen installation is upgraded as a typed one is: the notice of the co-located tier ends a plain upgrade, and the list learns the instance id.
func TestAnUpgradeOfTheChosenInstallationStillSaysTheTierNotice(t *testing.T) {
	useDefaultConfigDir(t)
	defaultSettingsOf(t, colocatedSettings())
	u := newUpgradeHarness(t, nil)
	if err := recordInstallation(runtime.GOOS, u.settingsPath(), u.tokenPath(), u.stack, ""); err != nil {
		t.Fatal(err)
	}
	env := u.env()
	env.Term = Terminal{In: strings.NewReader(u.settingsPath() + "\n"), Out: &u.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}
	if err := Install(context.Background(), []string{"--upgrade", "--image", testImage, "--yes", "--plain", "--backup-dir", u.backups}, env); err != nil {
		t.Fatalf("%v\n%s", err, u.out.String())
	}
	if !strings.Contains(u.out.String(), reducedAssuranceNotice) {
		t.Errorf("the notice of the co-located tier did not end the upgrade:\n%s", u.out.String())
	}
	if want := "suiteward doctor --settings " + quoteArg(u.settingsPath()) + " --token-file " + quoteArg(u.tokenPath()); !strings.Contains(u.out.String(), want) {
		t.Errorf("the hints do not name the chosen installation (%s):\n%s", want, u.out.String())
	}
	if got := entryOf(t, u.settingsPath()); got.Instance != testInstance {
		t.Errorf("the entry is %+v, want the instance %s", got, testInstance)
	}
}
