package installer

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// A choice of the installation never confirms a purge: the typed instance id (or --yes with --instance) is what says that the data of that
// installation may go, whatever was chosen.

// purgeOfAChosenInstallation is the co-located stack of a purge harness listed beside the default installation, so that the question is asked.
func purgeOfAChosenInstallation(t *testing.T) *harness {
	t.Helper()
	useDefaultConfigDir(t)
	defaultSettingsOf(t, colocatedSettings())
	h := purgeHarness(t)
	if err := recordInstallation(runtime.GOOS, h.settingsPath(), h.tokenPath(), colocatedSettings(), testInstance); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestAPurgeOfAChosenInstallationStillAsksForTheTypedInstanceId(t *testing.T) {
	t.Run("a wrong id removes nothing", func(t *testing.T) {
		h := purgeOfAChosenInstallation(t)
		env := h.consoleOf(true, h.settingsPath()+"\n"+"9f8e7d6c\n")
		err := Uninstall(context.Background(), []string{"--plain", "--purge"}, env)
		if err == nil || !strings.Contains(err.Error(), "was not confirmed") {
			t.Fatalf("Uninstall = %v, want the purge refused for a wrong id\n%s", err, h.out.String())
		}
		h.intact()
		if got := h.removed("volume rm"); len(got) != 0 {
			t.Errorf("volumes removed: %v", got)
		}
	})
	t.Run("choosing is not typing the id: an empty answer removes nothing", func(t *testing.T) {
		h := purgeOfAChosenInstallation(t)
		env := h.consoleOf(true, h.settingsPath()+"\n"+"\n")
		if err := Uninstall(context.Background(), []string{"--plain", "--purge"}, env); err == nil {
			t.Fatalf("a purge went through with no id typed\n%s", h.out.String())
		}
		h.intact()
	})
	t.Run("the right id purges the chosen installation", func(t *testing.T) {
		h := purgeOfAChosenInstallation(t)
		env := h.consoleOf(true, h.settingsPath()+"\n"+testInstance+"\n")
		if err := Uninstall(context.Background(), []string{"--plain", "--purge"}, env); err != nil {
			t.Fatalf("%v\n%s", err, h.out.String())
		}
		if got, want := h.removed("volume rm"), []string{VolumeData, VolumeDBData}; !slices.Equal(got, want) {
			t.Errorf("volumes removed: %v, want %v", got, want)
		}
	})
}

// With one installation, it is used and said, and --yes needs --instance all the same.
func TestAPurgeOfTheOnlyInstallationStillNeedsItsInstanceIdWithYes(t *testing.T) {
	useDefaultConfigDir(t) // no default settings file: the harness's installation is the one candidate
	h := purgeHarness(t)
	if err := recordInstallation(runtime.GOOS, h.settingsPath(), h.tokenPath(), colocatedSettings(), testInstance); err != nil {
		t.Fatal(err)
	}
	err := Uninstall(context.Background(), []string{"--plain", "--purge", "--yes"}, h.consoleOf(false, ""))
	if err == nil || !strings.Contains(err.Error(), "--instance "+testInstance) {
		t.Fatalf("Uninstall = %v, want the purge refused with the --instance to give\n%s", err, h.out.String())
	}
	h.intact()
	if !strings.Contains(h.out.String(), "one SuiteWard installation") {
		t.Errorf("the installation that was used is not said:\n%s", h.out.String())
	}
	if err := Uninstall(context.Background(), []string{"--plain", "--purge", "--yes", "--instance", testInstance}, h.consoleOf(false, "")); err != nil {
		t.Fatalf("%v\n%s", err, h.out.String())
	}
	if got, want := h.removed("volume rm"), []string{VolumeData, VolumeDBData}; !slices.Equal(got, want) {
		t.Errorf("volumes removed: %v, want %v", got, want)
	}
}

// A script with several installations and no flag that names one purges nothing, with its --instance or not.
func TestAScriptedPurgeAmongSeveralInstallationsRefusesBeforeAnyChange(t *testing.T) {
	h := purgeOfAChosenInstallation(t)
	for _, terminal := range []bool{false, true} {
		err := Uninstall(context.Background(), []string{"--plain", "--purge", "--yes", "--instance", testInstance}, h.consoleOf(terminal, h.settingsPath()+"\n"))
		if err == nil || !strings.Contains(err.Error(), "several SuiteWard installations") {
			t.Errorf("terminal %v: Uninstall = %v, want the refusal that names the flags", terminal, err)
		}
	}
	if len(h.f.calls) != 0 {
		t.Errorf("the engine was called before the refusal: %v", h.f.lines())
	}
	h.intact()
}
