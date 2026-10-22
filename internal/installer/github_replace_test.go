package installer

import (
	"context"
	"os"
	"strings"
	"testing"
)

// When the App of the settings has no key and a new App is made (--github new-app, or the choice 2 at the prompt), the key secret
// and the record are those of the NEW App from the moment it exists. A run that stops after that (the installation does not
// arrive, Ctrl-C, a later prompt) must be taken up by the next run: the settings still name the old App, and the old App must never
// run with the new key.

const oldAppID = 1111 // the App of the settings: the fake GitHub creates fakeAppID

func savedOldAppWithoutKey(t *testing.T) (*harness, *fakeGitHub) {
	t.Helper()
	h, g := savedAppWithoutKey(t)
	s := h.settings()
	s.GitHub.AppID = oldAppID
	if err := SaveSettings(h.settingsPath(), s); err != nil {
		t.Fatal(err)
	}
	return h, g
}

func TestInstallColocatedARunThatStopsAfterTheNewAppIsMadeIsTakenUpByTheNextRun(t *testing.T) {
	h, g := savedOldAppWithoutKey(t)
	g.neverInstalled(true)
	if err := h.installOn(false, "", append(newAppAnswers, "--github", "new-app")...); err == nil {
		t.Fatal("the first run must stop at the installation wait")
	}
	if g.count("POST /app-manifests") != 1 || !exists(pendingAppPath(h.settingsPath())) {
		t.Fatalf("the test needs the new App and its record: %v", g.requests)
	}
	h.theKeySecretIsThere() // the key of the new App, stored by the first run
	g.neverInstalled(false)
	before := len(h.secretWrites(SecretGitHubAppKey))

	if err := h.installOn(false, "", newAppAnswers...); err != nil { // no --github: a plain re-run
		t.Fatalf("the re-run must take up the new App: %v", err)
	}
	if n := g.count("POST /app-manifests"); n != 1 {
		t.Errorf("%d Apps were created, want one", n)
	}
	if got := h.settings().GitHub.AppID; got != fakeAppID {
		t.Errorf("the settings name the App %d, not the new App %d: the service would run the old App with the new key", got, fakeAppID)
	}
	if serve := h.f.find("--name suiteward --network")[0].line(); !strings.Contains(serve, "SUITEWARD_GITHUB_APP_ID=4242") {
		t.Errorf("the service did not start with the new App: %s", serve)
	}
	if after := len(h.secretWrites(SecretGitHubAppKey)); after != before {
		t.Errorf("the key was stored again")
	}
	if exists(pendingAppPath(h.settingsPath())) {
		t.Error("the record is still there after the settings were saved")
	}
}

func TestInstallIsolatedARunThatStopsAfterTheNewAppIsMadeIsTakenUpByTheNextRun(t *testing.T) {
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
	if fake.count("POST /app-manifests") != 1 || !exists(pendingAppPath(settingsFile(g.home))) || g.f.secrets[SecretGitHubAppKey] == "" {
		t.Fatalf("the test needs the new App, its record and its key: %v", fake.requests)
	}
	g.answers["github"] = ""
	fake.neverInstalled(false)

	if err := g.run(); err != nil {
		t.Fatalf("the re-run must take up the new App: %v", err)
	}
	if n := fake.count("POST /app-manifests"); n != 1 {
		t.Errorf("%d Apps were created, want one", n)
	}
	if saved, err := LoadSettings(settingsFile(g.home)); err != nil || saved.GitHub.AppID != fakeAppID {
		t.Errorf("the settings keep %+v, %v: the service would run the old App with the new key", saved.GitHub, err)
	}
	if got := string(readFileOrFail(t, envFile(g.home))); !strings.Contains(got, "SUITEWARD_GITHUB_APP_ID=4242") {
		t.Errorf("the env file still names the old App: %q", got)
	}
}

// A re-run with no GitHub answer must not skip the flow and start the old App with the key of the recorded one.
func TestWantsGitHubWhenTheRecordIsAnotherAppThanTheSettings(t *testing.T) {
	h := newHarness(t, "linux")
	h.theKeySecretIsThere()
	rt, err := NewPodman(context.Background(), h.f.deps(), "pm")
	if err != nil {
		t.Fatal(err)
	}
	r := newTestResolver(t, nil, nil, Terminal{In: strings.NewReader(""), Out: &strings.Builder{}})
	record := &PendingApp{AppID: fakeAppID, Slug: "suiteward-abcd", Account: "acme", AccountType: "Organization"}
	g := &githubInstall{rt: rt, s: Settings{GitHub: GitHub{AppID: oldAppID, InstallationID: 77}}, pending: record}
	if run, err := g.wantsGitHub(context.Background(), r); err != nil || !run {
		t.Fatalf("wantsGitHub = %v, %v; the key secret is the recorded App's, so the flow must run", run, err)
	}
	g.pending = &PendingApp{AppID: oldAppID, Slug: "suiteward-abcd", Account: "acme", AccountType: "Organization"}
	if run, err := g.wantsGitHub(context.Background(), r); err != nil || run {
		t.Fatalf("wantsGitHub = %v, %v; a record of the settings' own App changes nothing", run, err)
	}
}

func TestResolveGitHubTheRecordedAppWinsOverTheAppOfTheSettings(t *testing.T) {
	g := newFakeGitHub(t)
	env, _ := flowEnv(t, g, withoutApp(existingFlags(t, nil)), strings.NewReader(""), false)
	env.Existing = &GitHub{AppID: oldAppID, InstallationID: fakeInstallationID, Owner: "acme", Repositories: []string{"acme/app"}}
	env.Pending = &PendingApp{AppID: fakeAppID, Slug: "suiteward-abcd", Account: "acme", AccountType: "Organization"}
	env.KeyExists = func(context.Context) (bool, error) { return true, nil }
	env.ReadKey = func(context.Context) ([]byte, error) { return testKeyPEM(), nil }
	res, err := resolve(t, env)
	if err != nil {
		t.Fatal(err)
	}
	if res.AppID != fakeAppID || g.sawRequest("POST /app-manifests") {
		t.Errorf("the flow took up the App %d (requests %v), not the recorded App %d", res.AppID, g.requests, fakeAppID)
	}
}

func TestResolveGitHubNewAppRefusesAccuratelyWhileTheKeySecretExists(t *testing.T) {
	flags := func() map[string]string {
		f := manifestFlags()
		f["github"] = "new-app"
		return f
	}
	t.Run("the key of the App of the settings", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := namedAppWithoutKey(t, g, flags(), "", false)
		env.KeyExists = func(context.Context) (bool, error) { return true, nil }
		_, err := resolve(t, env)
		if err == nil {
			t.Fatal("the key secret is in the way")
		}
		for _, want := range []string{SecretGitHubAppKey, "4242", "stops working on this instance", oldAppPage} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error does not name %q:\n%v", want, err)
			}
		}
	})
	t.Run("the key of a recorded App is not the replaced App's key", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := namedAppWithoutKey(t, g, flags(), "", false)
		env.KeyExists = func(context.Context) (bool, error) { return true, nil }
		env.Pending = &PendingApp{AppID: 9, Slug: "suiteward-old", Account: "acme", AccountType: "Organization"}
		_, err := resolve(t, env)
		if err == nil {
			t.Fatal("the key secret is in the way")
		}
		for _, want := range []string{"suiteward-old", "https://github.example/organizations/acme/settings/apps/suiteward-old", "without --github new-app"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error does not name %q:\n%v", want, err)
			}
		}
		if strings.Contains(err.Error(), "being replaced") {
			t.Errorf("the error says the secret is the key of the replaced App, which the record contradicts:\n%v", err)
		}
	})
}

func TestResolveGitHubTheOldAppsAreNamedBeforeTheNewOneIsMade(t *testing.T) {
	g := newFakeGitHub(t)
	flags := manifestFlags()
	flags["github"] = "new-app"
	env, out := namedAppWithoutKey(t, g, flags, "", false)
	env.Pending = &PendingApp{AppID: 9, Slug: "suiteward-old", Account: "acme", AccountType: "Organization"}
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	created := strings.Index(text, "Created the GitHub App")
	for _, want := range []string{oldAppPage, "suiteward-old"} {
		first := strings.Index(text, want)
		if first < 0 || created < 0 || first > created {
			t.Errorf("%q is not named before the new App is made, so a run that stops after it leaves the user without the page:\n%s", want, text)
		}
	}
}

// The choice at the prompt is the terminal's: an answers-file key of that name (undocumented, and not a flag) never answers it.
func TestResolveGitHubTheMenuIsNeverAnsweredByTheAnswersFile(t *testing.T) {
	g := newFakeGitHub(t)
	flags := withoutApp(existingFlags(t, nil))
	delete(flags, "app-key-file")
	env, _ := namedAppWithoutKey(t, g, flags, "1\n"+writeKeyFile(t)+"\n", true)
	env.R.answers["key-route"] = "2"
	res, err := resolve(t, env)
	if err != nil {
		t.Fatal(err)
	}
	if g.sawRequest("POST /app-manifests") || string(res.PrivateKey) != string(testKeyPEM()) {
		t.Errorf("the typed 1 was overridden by the answers file: %v", g.requests)
	}
}

// A key file that GitHub accepts for a recorded App, and an installation that then does not arrive: the key was never stored, so
// the advice for a stored key (retry, remove the secret) does not fit.
func TestInstallColocatedAGivenKeyThatFailsAtTheInstallationWaitIsNotStored(t *testing.T) {
	h, g, args := colocatedFlow(t)
	g.neverInstalled(true)
	if err := h.podman(args...); err == nil {
		t.Fatal("the first run must fail")
	}
	err := h.podman(append(args, "--github-app-key-file", writeKeyFile(t))...) // still not installed
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v, want the installation wait to time out", err)
	}
	for _, wrong := range []string{"its key is the secret", "to retry"} {
		if strings.Contains(err.Error(), wrong) {
			t.Errorf("the error says %q, which is false: the key was never stored:\n%v", wrong, err)
		}
	}
	for _, want := range []string{"--github-app-key-file", "--github new-app"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q:\n%v", want, err)
		}
	}
}
