package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	const ids = "SUITEWARD_GITHUB_APP_ID=1111\nSUITEWARD_GITHUB_INSTALLATION_ID=2222\n"
	for name, c := range map[string]struct{ text, want string }{
		"partial":         {ids, "SUITEWARD_GITHUB_REPOSITORIES"},
		"one key":         {"SUITEWARD_GITHUB_APP_ID=1111\n", "SUITEWARD_GITHUB_INSTALLATION_ID"},
		"no equal sign":   {ids + "SUITEWARD_GITHUB_REPOSITORIES octo/one\n", "line 3"},
		"another key":     {ids + "SUITEWARD_GITHUB_REPOSITORIES=octo/one\nSUITEWARD_PASSWORD=hunter2\n", "line 4"},
		"a repeated key":  {ids + "SUITEWARD_GITHUB_APP_ID=3333\nSUITEWARD_GITHUB_REPOSITORIES=octo/one\n", "line 3"},
		"not a number":    {"SUITEWARD_GITHUB_APP_ID=abc\nSUITEWARD_GITHUB_INSTALLATION_ID=2222\nSUITEWARD_GITHUB_REPOSITORIES=octo/one\n", "SUITEWARD_GITHUB_APP_ID"},
		"not a repo list": {ids + "SUITEWARD_GITHUB_REPOSITORIES=octo/one,other/two\n", "SUITEWARD_GITHUB_REPOSITORIES"},
		"quoted":          {ids + "SUITEWARD_GITHUB_REPOSITORIES=\"octo/one\"\n", "SUITEWARD_GITHUB_REPOSITORIES"},
	} {
		g := newIsoGitHub(t)
		handInstalled(t, g, c.text)
		err := g.run()
		if err == nil || !strings.Contains(err.Error(), "github.env") || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: want a named error that mentions %q, got %v", name, c.want, err)
		}
		for _, value := range []string{"1111", "2222", "3333", "octo", "hunter2", "abc", "other"} {
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
