package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An App that the settings (or the isolated env file, or a record of an earlier run) name, with no key in the secret store, is
// never replaced silently: the installer stops and offers two routes. On a terminal it asks; otherwise the error names both:
// --github-app-key-file <file> (the key of that App, verified with GET /app) and --github new-app (a new App; the old one stays
// on GitHub, and the installer names the page that deletes it).

const (
	oldAppPage = "https://github.example/settings/apps" // where the App of the settings is deleted, whoever owns it
	oldAppOrg  = "https://github.example/organizations/acme/settings/apps"
)

func writeKeyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(path, testKeyPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// namedAppWithoutKey is a flow whose settings name the App of the fake GitHub and whose key secret is gone.
func namedAppWithoutKey(t *testing.T, g *fakeGitHub, flags map[string]string, stdin string, terminal bool) (githubEnv, *syncBuffer) {
	t.Helper()
	env, out := flowEnv(t, g, flags, strings.NewReader(stdin), terminal)
	env.Existing = &GitHub{AppID: fakeAppID, InstallationID: fakeInstallationID, Owner: "acme", Repositories: []string{"acme/app"}}
	env.KeyExists = func(context.Context) (bool, error) { return false, nil }
	env.ReadKey = func(context.Context) ([]byte, error) { t.Error("a key that does not exist was read"); return nil, nil }
	return env, out
}

func withoutApp(flags map[string]string) map[string]string {
	delete(flags, "app-id")
	delete(flags, "installation-id")
	return flags
}

func TestResolveGitHubNamedAppWithoutKeyWithoutATerminalNamesBothRoutes(t *testing.T) {
	g := newFakeGitHub(t)
	env, out := namedAppWithoutKey(t, g, manifestFlags(), "", false)
	_, err := resolve(t, env)
	if err == nil {
		t.Fatal("an App without its key must stop the flow")
	}
	for _, want := range []string{"--github-app-key-file", "--github new-app", "4242", oldAppPage, oldAppOrg} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q:\n%v", want, err)
		}
	}
	if len(g.requests) != 0 {
		t.Errorf("GitHub was called before the stop: %v", g.requests)
	}
	assertNoKey(t, err.Error(), out.String())
}

func TestResolveGitHubNamedAppWithoutKeyTakesTheKeyFile(t *testing.T) {
	g := newFakeGitHub(t)
	flags := withoutApp(existingFlags(t, nil))
	env, out := namedAppWithoutKey(t, g, flags, "", false)
	res, err := resolve(t, env)
	if err != nil {
		t.Fatal(err)
	}
	if res.AppID != fakeAppID || res.InstallationID != fakeInstallationID || string(res.PrivateKey) != string(testKeyPEM()) {
		t.Errorf("result = %v", res)
	}
	if !g.sawRequest("GET /app") || g.sawRequest("POST /app-manifests") {
		t.Errorf("the key was not verified, or an App was created: %v", g.requests)
	}
	assertNoKey(t, out.String())
}

func TestResolveGitHubNamedAppWithoutKeyRefusesTheKeyOfAnotherApp(t *testing.T) {
	g := newFakeGitHub(t)
	g.answersAppWith(401) // GET /app refuses a key that is not this App's
	env, out := namedAppWithoutKey(t, g, withoutApp(existingFlags(t, nil)), "", false)
	_, err := resolve(t, env)
	if err == nil || !strings.Contains(err.Error(), "GET /app") {
		t.Fatalf("got %v, want the refusal of GET /app", err)
	}
	if g.sawRequest("POST /app-manifests") {
		t.Error("a new App was created after the key was refused")
	}
	assertNoKey(t, err.Error(), out.String())
}

func TestResolveGitHubNamedAppWithoutKeyOnATerminal(t *testing.T) {
	t.Run("route 1 asks for the key file", func(t *testing.T) {
		g := newFakeGitHub(t)
		flags := withoutApp(existingFlags(t, nil))
		delete(flags, "app-key-file")
		env, out := namedAppWithoutKey(t, g, flags, "1\n"+writeKeyFile(t)+"\n", true)
		res, err := resolve(t, env)
		if err != nil {
			t.Fatal(err)
		}
		if res.AppID != fakeAppID || string(res.PrivateKey) != string(testKeyPEM()) || g.sawRequest("POST /app-manifests") {
			t.Errorf("result = %v, requests %v", res, g.requests)
		}
		for _, want := range []string{"4242", "no key in the secret store", "1)", "2)", oldAppPage} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("the prompt lacks %q:\n%s", want, out.String())
			}
		}
		assertNoKey(t, out.String())
	})
	t.Run("route 2 creates a new App and names the old one at the end", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, out := namedAppWithoutKey(t, g, manifestFlags(), "2\n", true)
		if _, err := resolve(t, env); err != nil {
			t.Fatal(err)
		}
		if !g.sawRequest("POST /app-manifests") {
			t.Fatalf("no App was created: %v", g.requests)
		}
		text := out.String()
		protected, note := strings.Index(text, "Protection as GitHub reports"), strings.LastIndex(text, oldAppPage)
		if protected < 0 || note < protected {
			t.Errorf("the page that deletes the old App is not printed at the end of the flow:\n%s", text)
		}
	})
	t.Run("another answer is an error and creates nothing", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := namedAppWithoutKey(t, g, manifestFlags(), "3\n", true)
		if _, err := resolve(t, env); err == nil || g.sawRequest("POST /app-manifests") {
			t.Fatalf("got %v, requests %v", err, g.requests)
		}
	})
}

func TestResolveGitHubNewAppReplacesTheAppOfTheSettings(t *testing.T) {
	newAppFlags := func() map[string]string { // flowEnv takes the manifest code out of the map it is given
		flags := manifestFlags()
		flags["github"] = "new-app"
		return flags
	}
	t.Run("without its key secret", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, out := namedAppWithoutKey(t, g, newAppFlags(), "", false)
		if _, err := resolve(t, env); err != nil || !g.sawRequest("POST /app-manifests") {
			t.Fatalf("got %v, requests %v", err, g.requests)
		}
		if !strings.Contains(out.String(), oldAppPage) {
			t.Errorf("the page that deletes the old App is not printed:\n%s", out.String())
		}
	})
	t.Run("with its key secret in the way, before an App is created", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := namedAppWithoutKey(t, g, newAppFlags(), "", false)
		env.KeyExists = func(context.Context) (bool, error) { return true, nil }
		_, err := resolve(t, env)
		if err == nil || !strings.Contains(err.Error(), SecretGitHubAppKey) || len(g.requests) != 0 {
			t.Fatalf("got %v, requests %v: a new App's key cannot be stored over the old one", err, g.requests)
		}
	})
	t.Run("ignores the App an earlier run recorded", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := namedAppWithoutKey(t, g, newAppFlags(), "", false)
		env.Existing = &GitHub{}
		env.Pending = &PendingApp{AppID: 9, Slug: "suiteward-old", Account: "acme", AccountType: "Organization"}
		if _, err := resolve(t, env); err != nil || !g.sawRequest("POST /app-manifests") {
			t.Fatalf("got %v, requests %v", err, g.requests)
		}
	})
}

func TestResolveGitHubRecordedAppWithoutKey(t *testing.T) {
	pending := &PendingApp{AppID: fakeAppID, Slug: "suiteward-abcd", Account: "acme", AccountType: "Organization"}
	record := func(env *githubEnv) {
		env.Existing = &GitHub{}
		env.Pending = pending
		env.KeyExists = func(context.Context) (bool, error) { return false, nil }
		env.ReadKey = func(context.Context) ([]byte, error) { t.Error("a key that does not exist was read"); return nil, nil }
	}
	t.Run("without a terminal it names both routes", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, manifestFlags(), strings.NewReader(""), false)
		record(&env)
		_, err := resolve(t, env)
		if err == nil {
			t.Fatal("a recorded App without its key must stop the flow")
		}
		for _, want := range []string{"--github-app-key-file", "--github new-app", "https://github.example/organizations/acme/settings/apps/suiteward-abcd"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error does not name %q:\n%v", want, err)
			}
		}
	})
	t.Run("the key file takes the App up", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, withoutApp(existingFlags(t, nil)), strings.NewReader(""), false)
		record(&env)
		res, err := resolve(t, env)
		if err != nil {
			t.Fatal(err)
		}
		if res.AppID != fakeAppID || string(res.PrivateKey) != string(testKeyPEM()) || g.sawRequest("POST /app-manifests") {
			t.Errorf("result = %v, requests %v", res, g.requests)
		}
	})
	t.Run("the key of another App is refused", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.answersAppWith(401)
		env, out := flowEnv(t, g, withoutApp(existingFlags(t, nil)), strings.NewReader(""), false)
		record(&env)
		_, err := resolve(t, env)
		if err == nil || !strings.Contains(err.Error(), "GET /app") || g.sawRequest("POST /app-manifests") {
			t.Fatalf("got %v, requests %v", err, g.requests)
		}
		assertNoKey(t, err.Error(), out.String())
	})
	t.Run("on a terminal, route 2 creates a new App", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, out := flowEnv(t, g, manifestFlags(), strings.NewReader("2\n"), true)
		record(&env)
		if _, err := resolve(t, env); err != nil || !g.sawRequest("POST /app-manifests") {
			t.Fatalf("got %v, requests %v", err, g.requests)
		}
		if !strings.Contains(out.String(), "https://github.example/organizations/acme/settings/apps/suiteward-abcd") {
			t.Errorf("the page of the old App is not printed:\n%s", out.String())
		}
	})
}

// ---- the co-located tier ----

// savedAppWithoutKey is a co-located install whose saved settings name the App of the fake GitHub and whose key secret is gone.
func savedAppWithoutKey(t *testing.T) (*harness, *fakeGitHub) {
	t.Helper()
	h := newHarness(t, "linux")
	g := newFakeGitHub(t)
	h.github = realFlow(g, 100*time.Millisecond)
	s := colocatedSettings()
	s.Connection = "pm"
	s.GitHub = GitHub{AppID: fakeAppID, InstallationID: fakeInstallationID, Owner: "acme", Repositories: []string{"acme/app"}}
	if err := SaveSettings(h.settingsPath(), s); err != nil {
		t.Fatal(err)
	}
	return h, g
}

var newAppAnswers = []string{"--github-account", "acme", "--github-repositories", "acme/app", "--github-owner", "alice", "--test-check", "CI / test"}

// install runs the install on a terminal that types stdin (or none), with the answers that keep it from asking anything else.
func (h *harness) installOn(terminal bool, stdin string, args ...string) error {
	h.t.Helper()
	env := h.newEnv()
	term, _ := fakeTerminal(terminal, stdin)
	term.Out = &h.out
	env.Term = term
	manifest := filepath.Join(h.t.TempDir(), "answers.yaml")
	if err := os.WriteFile(manifest, []byte("manifest-code: thecode1\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	all := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--answers", manifest, "--runtime", "podman", "--connection", "pm", "--image", testImage, "--tier", "co-located", "--http-port", "18081", "--mcp-port", "18091"}
	return Install(context.Background(), append(all, args...), env)
}

func TestInstallColocatedNamedAppWithoutKeyStopsBeforeAnyChange(t *testing.T) {
	h, g := savedAppWithoutKey(t)
	err := h.installOn(false, "", newAppAnswers...)
	if err == nil {
		t.Fatal("the install must stop")
	}
	for _, want := range []string{"--github-app-key-file", "--github new-app", "4242"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q:\n%v", want, err)
		}
	}
	h.mustNotMutate()
	if g.sawRequest("POST /app-manifests") {
		t.Error("a new App was created behind the settings' back")
	}
}

func TestInstallColocatedNamedAppWithoutKeyTakesTheKeyFile(t *testing.T) {
	h, g := savedAppWithoutKey(t)
	if err := h.installOn(false, "", append(newAppAnswers, "--github-app-key-file", writeKeyFile(t))...); err != nil {
		t.Fatal(err)
	}
	if writes := h.secretWrites(SecretGitHubAppKey); len(writes) != 1 || !strings.HasPrefix(writes[0], "-----BEGIN") {
		t.Errorf("the key was written %d times", len(writes))
	}
	if g.sawRequest("POST /app-manifests") {
		t.Error("a new App was created")
	}
	h.f.assertNoSecret(t, pemBody())
	assertNoKey(t, h.out.String())
}

func TestInstallColocatedNamedAppWithKeyOfAnotherAppChangesNothing(t *testing.T) {
	h, g := savedAppWithoutKey(t)
	g.answersAppWith(401)
	err := h.installOn(false, "", append(newAppAnswers, "--github-app-key-file", writeKeyFile(t))...)
	if err == nil || !strings.Contains(err.Error(), "GET /app") {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
	assertNoKey(t, err.Error(), h.out.String())
}

func TestInstallColocatedNamedAppWithoutKeyOnATerminal(t *testing.T) {
	h, g := savedAppWithoutKey(t)
	if err := h.installOn(true, "1\n"+writeKeyFile(t)+"\n", newAppAnswers...); err != nil {
		t.Fatalf("route 1: %v", err)
	}
	if len(h.secretWrites(SecretGitHubAppKey)) != 1 || g.sawRequest("POST /app-manifests") {
		t.Errorf("route 1 did not store the key of the App without creating another: %v", g.requests)
	}

	h, g = savedAppWithoutKey(t)
	if err := h.installOn(true, "2\n", newAppAnswers...); err != nil {
		t.Fatalf("route 2: %v", err)
	}
	if g.count("POST /app-manifests") != 1 || len(h.secretWrites(SecretGitHubAppKey)) != 1 {
		t.Errorf("route 2 did not create one App and store its key: %v", g.requests)
	}
	if !strings.Contains(h.out.String(), oldAppPage) {
		t.Errorf("the page that deletes the old App is not printed:\n%s", h.out.String())
	}
}

func TestInstallColocatedGitHubNewAppCreatesAnotherAppAndNamesTheOldOne(t *testing.T) {
	h, g := savedAppWithoutKey(t)
	if err := h.installOn(false, "", append(newAppAnswers, "--github", "new-app")...); err != nil {
		t.Fatal(err)
	}
	if g.count("POST /app-manifests") != 1 || !strings.Contains(h.out.String(), oldAppPage) {
		t.Errorf("--github new-app: %d Apps created\n%s", g.count("POST /app-manifests"), h.out.String())
	}
	h.assertNoLeak()
}

func TestInstallGitHubNewAppContradictsAnAppId(t *testing.T) {
	h, _ := savedAppWithoutKey(t)
	err := h.installOn(false, "", "--github", "new-app", "--github-app-id", "4242")
	if err == nil || !strings.Contains(err.Error(), "new-app") || !strings.Contains(err.Error(), "--github-app-id") {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestInstallColocatedARecordedAppWithoutItsKeyOffersTheRoutes(t *testing.T) {
	h, g, args := colocatedFlow(t)
	g.neverInstalled(true)
	if err := h.podman(args...); err == nil {
		t.Fatal("the first run must fail")
	}
	g.neverInstalled(false)
	err := h.podman(args...)
	if err == nil || !strings.Contains(err.Error(), "--github new-app") || !strings.Contains(err.Error(), "--github-app-key-file") {
		t.Fatalf("got %v, want both routes", err)
	}
	// The key file alone takes the recorded App up: no App id, no installation id.
	if err := h.podman(append(args, "--github-app-key-file", writeKeyFile(t))...); err != nil {
		t.Fatalf("the key file of the recorded App: %v", err)
	}
	if g.count("POST /app-manifests") != 1 {
		t.Errorf("%d Apps were created, want one", g.count("POST /app-manifests"))
	}
}

// ---- the isolated tier ----

// envFileAppWithoutKey is an isolated install whose env file names the App of the fake GitHub, with no key secret.
func envFileAppWithoutKey(t *testing.T) (*isoGitHub, *fakeGitHub) {
	t.Helper()
	g, fake := isolatedFlow(t)
	g.answers["github"] = ""
	if err := os.MkdirAll(filepath.Dir(envFile(g.home)), 0o700); err != nil {
		t.Fatal(err)
	}
	env := "SUITEWARD_GITHUB_APP_ID=4242\nSUITEWARD_GITHUB_INSTALLATION_ID=77\nSUITEWARD_GITHUB_REPOSITORIES=acme/app\n"
	if err := os.WriteFile(envFile(g.home), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	return g, fake
}

func TestInstallIsolatedNamedAppWithoutKeyStopsBeforeAnyChange(t *testing.T) {
	g, fake := envFileAppWithoutKey(t)
	err := g.run()
	if err == nil {
		t.Fatal("the install must stop")
	}
	for _, want := range []string{"--github-app-key-file", "--github new-app", "4242"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q:\n%v", want, err)
		}
	}
	if g.f.has("secret create") || g.f.has("start") || fake.sawRequest("POST /app-manifests") {
		t.Errorf("something was changed: %v", g.f.lines())
	}
}

func TestInstallIsolatedNamedAppWithoutKeyTakesTheKeyFile(t *testing.T) {
	g, fake := envFileAppWithoutKey(t)
	g.answers["github-app-key-file"] = writeKeyFile(t)
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	if g.f.secrets[SecretGitHubAppKey] == "" || fake.sawRequest("POST /app-manifests") {
		t.Errorf("the key was not stored, or an App was created: %v", fake.requests)
	}

	g, fake = envFileAppWithoutKey(t)
	fake.answersAppWith(401)
	g.answers["github-app-key-file"] = writeKeyFile(t)
	err := g.run()
	if err == nil || !strings.Contains(err.Error(), "GET /app") || g.f.has("secret create") {
		t.Fatalf("a key of another App: got %v", err)
	}
	assertNoKey(t, err.Error(), g.out.String())
}

func TestInstallIsolatedNamedAppWithoutKeyOnATerminal(t *testing.T) {
	g, fake := envFileAppWithoutKey(t)
	g.tty, g.stdin = true, "1\n"+writeKeyFile(t)+"\n"
	if err := g.run(); err != nil {
		t.Fatalf("route 1: %v", err)
	}
	if g.f.secrets[SecretGitHubAppKey] == "" || fake.sawRequest("POST /app-manifests") {
		t.Errorf("route 1: %v", fake.requests)
	}

	g, fake = envFileAppWithoutKey(t)
	g.tty, g.stdin = true, "2\n"
	if err := g.run(); err != nil {
		t.Fatalf("route 2: %v", err)
	}
	if fake.count("POST /app-manifests") != 1 || !strings.Contains(g.out.String(), oldAppPage) {
		t.Errorf("route 2: %d Apps created\n%s", fake.count("POST /app-manifests"), g.out.String())
	}
}

func TestInstallIsolatedGitHubNewAppCreatesAnotherApp(t *testing.T) {
	g, fake := envFileAppWithoutKey(t)
	g.answers["github"] = "new-app"
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	if fake.count("POST /app-manifests") != 1 || !strings.Contains(g.out.String(), oldAppPage) {
		t.Errorf("--github new-app: %d Apps created\n%s", fake.count("POST /app-manifests"), g.out.String())
	}
}
