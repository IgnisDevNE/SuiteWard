package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const handMadeEnv = "# made by hand from the install guide\nSUITEWARD_GITHUB_APP_ID=1111\nSUITEWARD_GITHUB_INSTALLATION_ID=2222\nSUITEWARD_GITHUB_REPOSITORIES=octo/one,octo/two\n"

// handInstalled makes the run of a host that has its App key secret and its GitHub env file, and was asked nothing about GitHub.
func handInstalled(t *testing.T, g *isoGitHub, envText string) {
	t.Helper()
	g.f.secrets[SecretGitHubAppKey] = "existing"
	for _, key := range []string{"github-app-id", "github-installation-id", "github-repositories", "github-owner"} {
		g.answers[key] = ""
	}
	if err := os.MkdirAll(filepath.Dir(envFile(g.home)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile(g.home), []byte(envText), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInstallIsolatedKeepsTheAppOfTheEnvFileOfAHandInstalledHost(t *testing.T) {
	for _, tty := range []bool{true, false} {
		g := newIsoGitHub(t)
		g.tty = tty
		handInstalled(t, g, handMadeEnv)
		if err := g.run(); err != nil {
			t.Fatalf("tty=%v: %v", tty, err)
		}
		out := g.out.String()
		if len(g.seen) != 0 || strings.Contains(out, "Set up GitHub now") {
			t.Errorf("tty=%v: the install offered a second App (flow runs %d):\n%s", tty, len(g.seen), out)
		}
		if strings.Contains(out, "GitHub is not configured") {
			t.Errorf("tty=%v: the App of the env file is configured:\n%s", tty, out)
		}
		saved, err := LoadSettings(settingsFile(g.home))
		if err != nil || saved.GitHub.AppID != 1111 || saved.GitHub.InstallationID != 2222 || saved.GitHub.Owner != "octo" || len(saved.GitHub.Repositories) != 2 {
			t.Errorf("tty=%v: the settings keep %+v, %v", tty, saved.GitHub, err)
		}
		if got := string(readFileOrFail(t, envFile(g.home))); got != handMadeEnv {
			t.Errorf("tty=%v: the hand-made env file was rewritten:\n%s", tty, got)
		}
		if g.f.secrets[SecretGitHubAppKey] != "existing" {
			t.Errorf("tty=%v: the installed key was replaced", tty)
		}
	}
}

func TestInstallIsolatedRerunAfterAFailureTakesTheAppFromTheEnvFile(t *testing.T) {
	g := newIsoGitHub(t)
	g.f.failOn[generatorPath] = errors.New("the generator failed") // after the env file was written, before the settings were saved
	if err := g.run(); err == nil {
		t.Fatal("the first run must fail")
	}
	if _, err := os.Stat(settingsFile(g.home)); err == nil {
		t.Fatal("the settings must not be saved after the failure")
	}
	delete(g.f.failOn, generatorPath)
	for _, key := range []string{"github-app-id", "github-installation-id", "github-repositories", "github-owner"} {
		g.answers[key] = ""
	}
	g.out.Reset()
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	if len(g.seen) != 1 {
		t.Errorf("the flow ran %d times in all, want only the first run's", len(g.seen))
	}
	saved, err := LoadSettings(settingsFile(g.home))
	if err != nil || saved.GitHub.AppID != 123 || saved.GitHub.InstallationID != 456 || len(saved.GitHub.Repositories) != 2 {
		t.Errorf("the re-run must take the App from the env file: %+v, %v", saved.GitHub, err)
	}
	if strings.Contains(g.out.String(), "GitHub is not configured") {
		t.Errorf("the output says GitHub is not configured:\n%s", g.out.String())
	}
}

func TestInstallIsolatedStopsOnAnEnvFileItCannotReadStrictly(t *testing.T) {
	// The ids have more digits than the random name of a temporary directory ever does (at most ten), so a path in an error never holds one.
	const (
		appID, installationID, repeatedID = "424242424242", "434343434343", "444444444444"
		ids                               = "SUITEWARD_GITHUB_APP_ID=" + appID + "\nSUITEWARD_GITHUB_INSTALLATION_ID=" + installationID + "\n"
	)
	for name, c := range map[string]struct{ text, want string }{
		"partial":         {ids, "SUITEWARD_GITHUB_REPOSITORIES"},
		"one key":         {"SUITEWARD_GITHUB_APP_ID=" + appID + "\n", "SUITEWARD_GITHUB_INSTALLATION_ID"},
		"no equal sign":   {ids + "SUITEWARD_GITHUB_REPOSITORIES octo/one\n", "line 3"},
		"another key":     {ids + "SUITEWARD_GITHUB_REPOSITORIES=octo/one\nSUITEWARD_PASSWORD=hunter2\n", "line 4"},
		"a repeated key":  {ids + "SUITEWARD_GITHUB_APP_ID=" + repeatedID + "\nSUITEWARD_GITHUB_REPOSITORIES=octo/one\n", "line 3"},
		"not a number":    {"SUITEWARD_GITHUB_APP_ID=abc\nSUITEWARD_GITHUB_INSTALLATION_ID=" + installationID + "\nSUITEWARD_GITHUB_REPOSITORIES=octo/one\n", "SUITEWARD_GITHUB_APP_ID"},
		"not a repo list": {ids + "SUITEWARD_GITHUB_REPOSITORIES=octo/one,other/two\n", "SUITEWARD_GITHUB_REPOSITORIES"},
		"quoted":          {ids + "SUITEWARD_GITHUB_REPOSITORIES=\"octo/one\"\n", "SUITEWARD_GITHUB_REPOSITORIES"},
	} {
		g := newIsoGitHub(t)
		handInstalled(t, g, c.text)
		err := g.run()
		if err == nil || !strings.Contains(err.Error(), "github.env") || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: want a named error that mentions %q, got %v", name, c.want, err)
		}
		for _, value := range []string{appID, installationID, repeatedID, "octo", "hunter2", "abc", "other"} {
			if strings.Contains(err.Error(), value) {
				t.Errorf("%s: the error repeats a value (%q): %v", name, value, err)
			}
		}
		if g.f.has("secret create") || g.f.has("pull") || g.f.has("start") || g.f.has("daemon-reload") || exists(unitDir(g.home)) {
			t.Fatalf("%s: nothing may change: %v", name, g.f.lines())
		}
		if got := string(readFileOrFail(t, envFile(g.home))); got != c.text {
			t.Errorf("%s: the env file was changed", name)
		}
	}
}

func TestInstallIsolatedAnEmptyEnvFileMeansNoGitHubValues(t *testing.T) {
	g := newIsoGitHub(t)
	handInstalled(t, g, "")
	if err := g.run(); err != nil {
		t.Fatalf("the installer writes an empty env file when GitHub is off: %v", err)
	}
	if !strings.Contains(g.out.String(), "GitHub is not configured") {
		t.Errorf("an empty file configures nothing, and the output must say so:\n%s", g.out.String())
	}
}

func TestInstallIsolatedStoresTheAppKeyWithOneTrailingNewline(t *testing.T) {
	bare := strings.TrimSpace(appKeyPEM(t))
	for name, key := range map[string]string{"none": bare, "two": bare + "\n\n", "crlf": bare + "\r\n"} {
		g := newIsoGitHub(t)
		inner := g.flow
		g.flow = func(ctx context.Context, e githubEnv) (GitHubResult, error) {
			res, err := inner(ctx, e)
			res.PrivateKey = []byte(key)
			return res, err
		}
		if err := g.run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if g.f.secrets[SecretGitHubAppKey] != appKeyStored(t) {
			t.Errorf("%s: the stored key is %q, want the PEM with one trailing newline", name, g.f.secrets[SecretGitHubAppKey])
		}
	}
}

const placeholderCommand = "suiteward enroll --tier isolated"

func TestInstallIsolatedRerunWithoutTheFlowSaysWhetherAnOwnerIsEnrolled(t *testing.T) {
	enrolled := `{"version":"v0.2.0","owner":{"login":"bob","accountId":7,"enrolledAt":"2026-10-01T00:00:00Z"}}`
	for name, c := range map[string]struct {
		status func(context.Context, string) ([]byte, error)
		want   []string
	}{
		"enrolled": {
			func(context.Context, string) ([]byte, error) { return []byte(enrolled), nil },
			[]string{"an owner (bob) is already enrolled", "setup was skipped", "TOTP secret is unchanged"},
		},
		"no owner": {
			func(context.Context, string) ([]byte, error) { return []byte(`{"version":"v0.2.0"}`), nil },
			[]string{"no owner is enrolled", placeholderCommand},
		},
		"unreadable": {
			func(_ context.Context, address string) ([]byte, error) {
				if strings.HasSuffix(address, "/readyz") { // the service is up and stable; only /status cannot be read
					return []byte("ok"), nil
				}
				return nil, errors.New("dial tcp: connection refused")
			},
			[]string{"connection refused", placeholderCommand},
		},
	} {
		terminalForSetup(t)
		g := newIsoGitHub(t)
		handInstalled(t, g, handMadeEnv)
		g.status = c.status
		if err := g.run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(g.f.tty) != 0 || len(g.seen) != 0 {
			t.Errorf("%s: setup or the flow ran although there is no owner login: %v", name, g.f.tty)
		}
		for _, want := range c.want {
			if !strings.Contains(g.out.String(), want) {
				t.Errorf("%s: the output lacks %q:\n%s", name, want, g.out.String())
			}
		}
	}
}

func TestInstallIsolatedWritesTheEnvFileBeforeTheSecretsSoAFailureInBetweenKeepsTheApp(t *testing.T) {
	g := newIsoGitHub(t)
	inner := g.flow
	g.flow = func(ctx context.Context, e githubEnv) (GitHubResult, error) {
		if err := e.StoreKey(ctx, []byte(appKeyPEM(t))); err != nil { // the App was just created
			return GitHubResult{}, err
		}
		return inner(ctx, e)
	}
	g.f.secrets[SecretDatabaseOwnerURL] = DatabaseURL("suiteward", strings.Repeat("cd", 24)) // ensureSecrets refuses this
	if err := g.run(); err == nil {
		t.Fatal("ensureSecrets must refuse an owner URL without its password")
	}
	if got := string(readFileOrFail(t, envFile(g.home))); !strings.Contains(got, "SUITEWARD_GITHUB_APP_ID=123") {
		t.Fatalf("the App created by the flow must reach the env file before anything else can fail: %q", got)
	}
	delete(g.f.secrets, SecretDatabaseOwnerURL)
	for _, key := range []string{"github-app-id", "github-installation-id", "github-repositories", "github-owner"} {
		g.answers[key] = ""
	}
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	if len(g.seen) != 1 {
		t.Errorf("the re-run created another App (%d flow runs)", len(g.seen))
	}
	if saved, err := LoadSettings(settingsFile(g.home)); err != nil || saved.GitHub.AppID != 123 {
		t.Errorf("the re-run must reuse the App: %+v, %v", saved.GitHub, err)
	}
}

func TestReadGitHubEnvFileAcceptsMaxRepositoriesAndRefusesOneMore(t *testing.T) {
	file := func(n int) string {
		var repos []string
		for i := range n {
			repos = append(repos, "octo/r"+strconv.Itoa(i))
		}
		path := filepath.Join(t.TempDir(), "github.env")
		text := "SUITEWARD_GITHUB_APP_ID=1\nSUITEWARD_GITHUB_INSTALLATION_ID=2\nSUITEWARD_GITHUB_REPOSITORIES=" + strings.Join(repos, ",") + "\n"
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if gh, err := readGitHubEnvFile(file(MaxRepositories)); err != nil || len(gh.Repositories) != MaxRepositories {
		t.Fatalf("%d repositories must pass: %v", MaxRepositories, err)
	}
	if _, err := readGitHubEnvFile(file(MaxRepositories + 1)); err == nil {
		t.Fatalf("%d repositories must be refused", MaxRepositories+1)
	}
}

func TestInstallIsolatedRunsTheMigrationAgainWhenTheServiceIsNotRunning(t *testing.T) {
	// suiteward-migrate.service stays active after it ran (RemainAfterExit), so starting the service alone would not migrate to
	// a release that changed since.
	g := newIsoGitHub(t)
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	role := g.f.index("exec -i suiteward-db psql", 0)
	migrate := g.f.index("systemctl --user restart suiteward-migrate.service", max(role, 0))
	start := g.f.index("systemctl --user start suiteward.service", max(migrate, 0))
	if role < 0 || migrate < 0 || start < 0 {
		t.Fatalf("the migration must be restarted after the role and before the service starts:\n%s", strings.Join(g.f.lines(), "\n"))
	}

	g = newIsoGitHub(t)
	g.f.serviceRunning, g.f.everActive = true, true
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	if g.f.has("restart suiteward-migrate.service") {
		t.Errorf("a service that runs is not restarted by a re-run: %v", g.f.lines())
	}
}
