package installer

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A recorded App that is not the settings' App holds the key secret. Two more paths must not bring the stack up with the settings'
// App and that key: --github no, and install --upgrade (which never ran the GitHub flow, so never read the record).

var unfinishedRecord = PendingApp{AppID: fakeAppID, Slug: "suiteward-abcd", Account: "acme", AccountType: "Organization"}

// mutationsAfter lists the changes the fake engine was asked for after its first n calls.
func (h *harness) mutationsAfter(n int) []string {
	var out []string
	for _, c := range h.f.calls[n:] {
		line := normalized(c)
		for _, verb := range mutatingVerbs {
			if strings.HasPrefix(line, verb) {
				out = append(out, line)
			}
		}
	}
	return out
}

func TestInstallColocatedGitHubNoRefusesWhileARecordedAppHoldsTheKey(t *testing.T) {
	h, g := savedOldAppWithoutKey(t)
	g.neverInstalled(true)
	if err := h.installOn(false, "", append(newAppAnswers, "--github", "new-app")...); err == nil {
		t.Fatal("the first run must stop at the installation wait")
	}
	h.theKeySecretIsThere() // the key of the new App
	before := len(h.f.calls)
	err := h.installOn(false, "", "--github", "no")
	if err == nil {
		t.Fatal("the stack must not come up with the App of the settings and the key of the recorded one")
	}
	for _, want := range []string{"--github no", "--github-app-id none", pendingAppPath(h.settingsPath()), SecretGitHubAppKey} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q:\n%v", want, err)
		}
	}
	if m := h.mutationsAfter(before); len(m) != 0 {
		t.Errorf("the refused run changed something: %v", m)
	}
	// --github-app-id none is the way out the error names: the settings stop naming the App.
	if err := h.installOn(false, "", "--github-app-id", "none"); err != nil {
		t.Fatalf("--github-app-id none: %v", err)
	}
}

func TestInstallIsolatedGitHubNoRefusesWhileARecordedAppHoldsTheKey(t *testing.T) {
	g, fake := envFileAppWithoutKey(t)
	env := "SUITEWARD_GITHUB_APP_ID=1111\nSUITEWARD_GITHUB_INSTALLATION_ID=77\nSUITEWARD_GITHUB_REPOSITORIES=acme/app\n"
	if err := os.WriteFile(envFile(g.home), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	g.answers["github"] = "new-app"
	fake.neverInstalled(true)
	if err := g.run(); err == nil {
		t.Fatal("the first run must stop at the installation wait")
	}
	g.answers["github"] = "no"
	before := len(g.f.calls)
	err := g.run()
	// --github-app-id none is no way out here: the units read the env file of the settings' App whatever the run says.
	if err == nil || strings.Contains(err.Error(), "--github-app-id none") || !strings.Contains(err.Error(), "drop --github no") || !strings.Contains(err.Error(), pendingAppPath(settingsFile(g.home))) {
		t.Fatalf("got %v, want the refusal that names the way out", err)
	}
	for _, c := range g.f.lines()[before:] {
		for _, change := range []string{"start", "daemon-reload", "secret create"} {
			if strings.Contains(c, change) {
				t.Errorf("the refused run changed something: %s", c)
			}
		}
	}
	if got := string(readFileOrFail(t, envFile(g.home))); got != env {
		t.Errorf("the env file was rewritten: %q", got)
	}
}

// The record of the settings' own App is not "unused": the note about a recorded App is for another App.
func TestInstallColocatedGitHubNoWithARecordOfTheSettingsOwnAppSaysNothing(t *testing.T) {
	h, _ := savedAppWithoutKey(t) // the App of the settings is fakeAppID
	h.theKeySecretIsThere()
	if err := savePendingApp(pendingAppPath(h.settingsPath()), unfinishedRecord); err != nil {
		t.Fatal(err)
	}
	if err := h.installOn(false, "", "--github", "no"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.out.String(), "is not used") {
		t.Errorf("a record of the App of the settings was reported as unused:\n%s", h.out.String())
	}
}

// ---- install --upgrade ----

func TestUpgradeColocatedRefusesWhileAnInstallIsUnfinished(t *testing.T) {
	u := newUpgradeHarness(t, nil) // the settings name no App
	if err := savePendingApp(pendingAppPath(u.settingsPath()), unfinishedRecord); err != nil {
		t.Fatal(err)
	}
	err := u.upgrade("--image", testImage, "--yes")
	if err == nil {
		t.Fatal("an upgrade must not restart the stack over an unfinished install")
	}
	for _, want := range []string{pendingAppPath(u.settingsPath()), "suiteward install", "suiteward-abcd"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q:\n%v", want, err)
		}
	}
	u.noChanges()
}

func TestUpgradeColocatedGoesOnWhenTheRecordIsTheSettingsOwnApp(t *testing.T) {
	u := newUpgradeHarness(t, func(s *Settings) {
		s.GitHub = GitHub{AppID: fakeAppID, InstallationID: 77, Owner: "acme", Repositories: []string{"acme/app"}}
	})
	if err := savePendingApp(pendingAppPath(u.settingsPath()), unfinishedRecord); err != nil {
		t.Fatal(err)
	}
	if err := u.upgrade("--image", testImage, "--yes"); err != nil {
		t.Fatalf("a record of the App of the settings is no obstacle: %v", err)
	}
}

func TestUpgradeIsolatedRefusesWhileAnInstallIsUnfinished(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	env := "SUITEWARD_GITHUB_APP_ID=1111\nSUITEWARD_GITHUB_INSTALLATION_ID=77\nSUITEWARD_GITHUB_REPOSITORIES=acme/app\n"
	if err := os.WriteFile(envFile(u.home), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := savePendingApp(pendingAppPath(settingsFile(u.home)), unfinishedRecord); err != nil {
		t.Fatal(err)
	}
	before := u.units()
	err := u.upgrade("--image", testImage, "--yes")
	if err == nil || !strings.Contains(err.Error(), pendingAppPath(settingsFile(u.home))) || !strings.Contains(err.Error(), "suiteward install") {
		t.Fatalf("got %v, want the refusal that names the record and the plain install", err)
	}
	u.untouched(before)

	// The record of the App the env file names is no obstacle.
	u = newIsoUpgrade(t, nil)
	same := "SUITEWARD_GITHUB_APP_ID=4242\nSUITEWARD_GITHUB_INSTALLATION_ID=77\nSUITEWARD_GITHUB_REPOSITORIES=acme/app\n"
	if err := os.WriteFile(envFile(u.home), []byte(same), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := savePendingApp(pendingAppPath(settingsFile(u.home)), unfinishedRecord); err != nil {
		t.Fatal(err)
	}
	if err := u.upgrade("--image", testImage, "--yes"); err != nil {
		t.Fatalf("a record of the App of the env file is no obstacle: %v", err)
	}
}

// ---- the settings path in the commands the installer prints ----

func TestTheCommandsTheInstallerPrintsQuoteTheSettingsPath(t *testing.T) {
	if got := doctorHint("/home/me/My Settings/s.yaml", false); got != "suiteward doctor --settings '/home/me/My Settings/s.yaml'" {
		t.Errorf("doctorHint = %q", got)
	}
	if got := doctorHint("/home/me/s.yaml", false); got != "suiteward doctor --settings /home/me/s.yaml" {
		t.Errorf("doctorHint = %q", got)
	}
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	spaced := h.dir + "/my settings/settings.yaml"
	if err := h.installAt("--settings", spaced, "--runtime", "podman", "--connection", "pm", "--image", testImage, "--github-app-id", "1234", "--github-installation-id", "5678", "--github-owner", "Acme", "--github-repositories", "Acme/api,Acme/web"); err != nil {
		t.Fatal(err)
	}
	if want := "suiteward doctor --settings '" + spaced + "'"; !strings.Contains(h.out.String(), want) {
		t.Errorf("the install does not print %q:\n%s", want, h.out.String())
	}
}
