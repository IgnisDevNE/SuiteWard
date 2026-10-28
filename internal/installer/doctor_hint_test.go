package installer

import (
	"context"
	"strings"
	"testing"
)

// Doctor finds the files of the executor beside the token file, and takes the default token file for the default settings file only. The command that
// an install or an upgrade tells the person to run to check the instance names --token-file whenever the settings file is not the default one, so
// that the doctor it leads to does not fail executor.token; the default instance's command stays as short as it was.

func TestTheDoctorCommandOfAnInstallNamesTheTokenFileOfAnInstanceWithItsOwnSettings(t *testing.T) {
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	if err := Install(context.Background(), colocatedArgs(h), h.newEnv()); err != nil {
		t.Fatal(err)
	}
	want := "Then check that everything works with `suiteward doctor --settings " + quoteArg(h.settingsPath()) + " --token-file " + quoteArg(h.tokenPath()) + "`."
	if !strings.Contains(h.out.String(), want) {
		t.Errorf("the output lacks %q:\n%s", want, h.out.String())
	}
}

func TestTheDoctorCommandOfAnUpgradeNamesTheTokenFileOfAnInstanceWithItsOwnSettings(t *testing.T) {
	for name, image := range map[string]string{"with nothing to upgrade": oldImage, "after an upgrade": testImage} {
		t.Run(name, func(t *testing.T) {
			u := newUpgradeHarness(t, nil)
			if err := u.upgradeAt(false, "", "--yes", "--image", image); err != nil {
				t.Fatal(err)
			}
			want := "Check the instance with `suiteward doctor --settings " + quoteArg(u.settingsPath()) + " --token-file " + quoteArg(u.tokenPath()) + "`."
			if !strings.Contains(u.out.String(), want) {
				t.Errorf("the output lacks %q:\n%s", want, u.out.String())
			}
		})
	}
}

// A sole instance with a settings file of its own, given the default token file as --token-file (the way out that the refusal names), is told to
// check with that token file too.
func TestTheDoctorCommandOfASoleInstanceWithItsOwnSettingsNamesTheDefaultTokenFile(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	defaultFile := defaultTokenPlace(t, u.harness)
	args := []string{"--settings", u.settingsPath(), "--token-file", defaultFile, "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", oldImage, "--yes"}
	if err := Install(context.Background(), args, u.env()); err != nil {
		t.Fatal(err)
	}
	want := "Check the instance with `suiteward doctor --settings " + quoteArg(u.settingsPath()) + " --token-file " + quoteArg(defaultFile) + "`."
	if !strings.Contains(u.out.String(), want) {
		t.Errorf("the output lacks %q:\n%s", want, u.out.String())
	}
}

// The default instance, with the default settings file and its token file, is checked by a bare `doctor`.
func TestTheDoctorCommandOfTheDefaultInstanceNamesNeitherTheSettingsNorTheTokenFile(t *testing.T) {
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	defaultTokenPlace(t, h)
	args := append([]string{"--runtime", "podman", "--connection", "pm", "--image", testImage}, githubFlags...)
	if err := Install(context.Background(), args, h.newEnv()); err != nil {
		t.Fatal(err)
	}
	if want := "Then check that everything works with `suiteward doctor`."; !strings.Contains(h.out.String(), want) {
		t.Errorf("the output lacks %q:\n%s", want, h.out.String())
	}
}
