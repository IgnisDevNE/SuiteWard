package installer

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (g *fakeGitHub) answer(request string, status int, body string) {
	if g.override == nil {
		g.override = map[string]fakeAnswer{}
	}
	g.override[request] = fakeAnswer{status: status, body: body}
}

func TestResolveGitHubRefusesAnEnvironmentWithoutWhatItNeeds(t *testing.T) {
	if _, err := resolveGitHub(context.Background(), githubEnv{}); err == nil || !strings.Contains(err.Error(), "needs a resolver, an output and the settings directory") {
		t.Fatalf("resolveGitHub = %v", err)
	}
	r, err := NewResolver(nil, nil, Terminal{In: strings.NewReader(""), Out: &syncBuffer{}})
	if err != nil {
		t.Fatal(err)
	}
	env, err := githubEnv{R: r, Out: &syncBuffer{}, SettingsDir: t.TempDir(), WebURL: "https://github.example/"}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if env.HTTP == nil || env.HTTP.Timeout == 0 || env.APIURL != "https://api.github.com" || env.Now == nil || env.WebURL != "https://github.example" ||
		env.ManifestWait != defaultManifestWait || env.InstallWait != 10*time.Minute || env.PollEvery != 5*time.Second {
		t.Errorf("defaults = %+v", env)
	}
	env, err = githubEnv{R: r, Out: &syncBuffer{}, SettingsDir: t.TempDir()}.withDefaults()
	if err != nil || env.WebURL != "https://github.com" {
		t.Errorf("default web address = %q, %v", env.WebURL, err)
	}
}

func TestAppPageIsWhereTheAppIsDeleted(t *testing.T) {
	if got := appPage("https://github.com", "suiteward-abcd", "acme", "Organization"); got != "https://github.com/organizations/acme/settings/apps/suiteward-abcd" {
		t.Errorf("organization: %s", got)
	}
	if got := appPage("https://github.com", "suiteward-abcd", "alice", "User"); got != "https://github.com/settings/apps/suiteward-abcd" {
		t.Errorf("user: %s", got)
	}
}

// Every step of the flow that fails stops it with an error that names the step; nothing reaches the output that is a key.
func TestResolveGitHubStopsAtTheStepThatFails(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, g *fakeGitHub, flags map[string]string)
		want  string
	}{
		{"an App id that is not a number", func(_ *testing.T, _ *fakeGitHub, f map[string]string) { f["app-id"] = "abc" }, `app-id "abc" is not a positive number`},
		{"an App id below one", func(_ *testing.T, _ *fakeGitHub, f map[string]string) { f["app-id"] = "-3" }, "is not a positive number"},
		{"an installation id that is not a number", func(_ *testing.T, _ *fakeGitHub, f map[string]string) { f["installation-id"] = "x" }, `installation-id "x" is not a positive number`},
		{"a key file that is not there", func(t *testing.T, _ *fakeGitHub, f map[string]string) {
			f["app-key-file"] = filepath.Join(t.TempDir(), "missing.pem")
		}, "read the App key file"},
		{"an App that GitHub refuses", func(_ *testing.T, g *fakeGitHub, _ map[string]string) { g.appStatus = http.StatusUnauthorized }, "verify the App with GET /app"},
		{"installations that cannot be listed", func(_ *testing.T, g *fakeGitHub, _ map[string]string) {
			g.answer("GET /app/installations", http.StatusInternalServerError, "")
		}, "list the App's installations"},
		{"repositories that cannot be listed", func(_ *testing.T, g *fakeGitHub, _ map[string]string) {
			g.answer("GET /installation/repositories", http.StatusInternalServerError, "")
		}, "list the repositories of installation 77"},
		{"an installation without repositories", func(_ *testing.T, g *fakeGitHub, _ map[string]string) { g.repos = nil }, "covers no repositories"},
		{"a repository of another owner", func(_ *testing.T, g *fakeGitHub, _ map[string]string) {
			g.repos = []fakeRepo{{name: "other/app", branch: "main"}}
		}, "which is not owned by acme"},
		{"a repository that is not covered", func(_ *testing.T, _ *fakeGitHub, f map[string]string) { f["repositories"] = "acme/elsewhere" }, "is not one of the repositories the installation covers"},
		{"no repository chosen", func(_ *testing.T, _ *fakeGitHub, f map[string]string) { f["repositories"] = " , " }, "0 repositories chosen"},
		{"no owner for an organization", func(_ *testing.T, _ *fakeGitHub, f map[string]string) { delete(f, "owner") }, "instance owner"},
		{"an owner that is not a login", func(_ *testing.T, _ *fakeGitHub, f map[string]string) { f["owner"] = "not a login!" }, `owner "not a login!" is not a GitHub login`},
		{"a repository that cannot be read", func(_ *testing.T, g *fakeGitHub, _ map[string]string) {
			g.answer("GET /repos/acme/app", http.StatusInternalServerError, "")
		}, "read the repository acme/app"},
		{"a test check that is blank", func(_ *testing.T, _ *fakeGitHub, f map[string]string) { f["test-check"] = "  " }, "test check of acme/app is empty"},
		{"a test check that is not named after a repository", func(_ *testing.T, _ *fakeGitHub, f map[string]string) {
			f["test-check"] = "acme/other=CI / test"
		}, "not one of the chosen repositories"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGitHub(t)
			flags := existingFlags(t, nil)
			tc.setup(t, g, flags)
			env, out := flowEnv(t, g, flags, strings.NewReader(""), false)
			_, err := resolve(t, env)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("resolveGitHub = %v, want an error containing %q", err, tc.want)
			}
			assertNoKey(t, err.Error(), out.String())
			if entries, _ := os.ReadDir(env.SettingsDir); len(entries) != 0 {
				t.Errorf("a ruleset was written although the flow failed before it: %v", entries)
			}
		})
	}
}

// Reading the check runs is a help, not a requirement: when it fails the user types the name.
func TestResolveGitHubTypesTheCheckWhenTheRunsCannotBeRead(t *testing.T) {
	for name, setup := range map[string]func(*fakeGitHub){
		"the list fails": func(g *fakeGitHub) {
			g.answer("GET /repos/acme/app/commits/main/check-runs", http.StatusInternalServerError, "")
		},
		"the list is not JSON": func(g *fakeGitHub) {
			g.answer("GET /repos/acme/app/commits/main/check-runs", http.StatusOK, "not json")
		},
		"the branch is a . path": func(g *fakeGitHub) { g.repos[0].branch = ".." },
	} {
		t.Run(name, func(t *testing.T) {
			g := newFakeGitHub(t)
			setup(g)
			env, out := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
			res, err := resolve(t, env)
			if err != nil {
				t.Fatal(err)
			}
			if res.TestChecks["acme/app"] != "CI / test" {
				t.Errorf("the typed check was not used: %v", res.TestChecks)
			}
			if !strings.Contains(out.String(), "Could not read the check runs of acme/app") {
				t.Errorf("the failure was not said:\n%s", out.String())
			}
		})
	}
}

func TestResolveGitHubStopsWhenTheRulesetCannotBeWrittenOrAskedFor(t *testing.T) {
	t.Run("the settings directory is a file", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		env.SettingsDir = filepath.Join(file, "sub")
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "write the ruleset of acme/app") {
			t.Fatalf("resolveGitHub = %v", err)
		}
	})
	t.Run("the import cannot be waited for", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), true) // a terminal that has no input left
		env.SkipRulesetWait = false
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "read the answer for") {
			t.Fatalf("resolveGitHub = %v", err)
		}
	})
	t.Run("a test check that cannot make a ruleset", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
		a := ghAccess{}
		_, err := importRulesets(context.Background(), env, a, GitHubResult{AppID: fakeAppID, TestChecks: map[string]string{"acme/app": ""}}, []repoInfo{{name: "acme/app", branch: "main"}})
		if err == nil || !strings.Contains(err.Error(), "ruleset of acme/app") {
			t.Fatalf("importRulesets = %v", err)
		}
		if entries, _ := os.ReadDir(env.SettingsDir); len(entries) != 0 {
			t.Errorf("a ruleset was written for a test check that is empty: %v", entries)
		}
	})
}

// The GitHub clients are composed from an installation that has an id and repositories; one that does not is refused, by every
// step that builds a client.
func TestGitHubStepsRefuseAnInstallationThatCannotBeComposed(t *testing.T) {
	g := newFakeGitHub(t)
	env, _ := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
	ctx := context.Background()
	for name, a := range map[string]ghAccess{
		"no id":           {id: 0, names: []string{"app"}, http: g.srv.Client(), api: g.srv.URL},
		"no repositories": {id: 77, http: g.srv.Client(), api: g.srv.URL},
		"no http client":  {id: 77, names: []string{"app"}, api: g.srv.URL},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := a.client(checksReadPermissions); err == nil || !strings.Contains(err.Error(), "compose the GitHub installation") {
				t.Errorf("client = %v", err)
			}
			if _, err := readRepositories(ctx, a, []string{"acme/app"}); err == nil || !strings.Contains(err.Error(), "compose the GitHub installation") {
				t.Errorf("readRepositories = %v", err)
			}
			if _, err := chooseTestChecks(ctx, env, a, []repoInfo{{name: "acme/app", branch: "main"}}); err == nil || !strings.Contains(err.Error(), "compose the GitHub installation") {
				t.Errorf("chooseTestChecks = %v", err)
			}
			if _, err := importRulesets(ctx, withoutWait(env), a, GitHubResult{AppID: fakeAppID, TestChecks: map[string]string{"acme/app": "CI / test"}}, []repoInfo{{name: "acme/app", branch: "main"}}); err == nil || !strings.Contains(err.Error(), "compose the GitHub installation") {
				t.Errorf("importRulesets = %v", err)
			}
		})
	}
}

func withoutWait(env githubEnv) githubEnv {
	env.SkipRulesetWait = true
	env.SettingsDir = os.TempDir()
	return env
}

// A protection that GitHub does not answer is reported as unknown, not as a failure of the install.
func TestResolveGitHubReportsAProtectionItCannotRead(t *testing.T) {
	g := newFakeGitHub(t)
	g.repos[0].rulesStatus = http.StatusInternalServerError
	env, out := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
	res, err := resolve(t, env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "could not read the protection") {
		t.Errorf("the output does not say it:\n%s", out.String())
	}
	if res.Protection["acme/app"].Kind == "enforced" {
		t.Errorf("an unread protection was reported as enforced: %v", res.Protection["acme/app"])
	}
}

// ---- creating an App ----

func TestResolveGitHubCreateAppStopsAndNamesTheAppItLeft(t *testing.T) {
	t.Run("a github-account that is not a login", func(t *testing.T) {
		g := newFakeGitHub(t)
		flags := manifestFlags()
		flags["github-account"] = "not a login!"
		env, _ := flowEnv(t, g, flags, strings.NewReader(""), false)
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "is not \"personal\" or a GitHub login") {
			t.Fatalf("resolveGitHub = %v", err)
		}
		if g.sawRequest("POST /app-manifests") {
			t.Error("an App was created for an account that is not a login")
		}
	})
	t.Run("a key GitHub returned that is not a key", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.convertRaw = `{"id":4242,"slug":"suiteward-abcd","owner":{"login":"acme","type":"Organization"},"permissions":{"metadata":"read","pull_requests":"write","contents":"read","administration":"read","checks":"write"},"pem":"not a pem secret-xyz","client_secret":"c","webhook_secret":"w"}`
		env, out := flowEnv(t, g, manifestFlags(), strings.NewReader(""), false)
		_, err := resolve(t, env)
		if err == nil || !strings.Contains(err.Error(), "is not usable") || !strings.Contains(err.Error(), "settings/apps/suiteward-abcd") {
			t.Fatalf("resolveGitHub = %v", err)
		}
		if strings.Contains(err.Error(), "secret-xyz") || strings.Contains(out.String(), "secret-xyz") {
			t.Error("the key text reached the output")
		}
	})
	t.Run("the App cannot be recorded", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, out := flowEnv(t, g, manifestFlags(), strings.NewReader(""), false)
		env.StoreKey = func(context.Context, []byte) error { return nil }
		env.RecordApp = func(context.Context, PendingApp) error { return errors.New("the disk is full") }
		_, err := resolve(t, env)
		if err == nil || !strings.Contains(err.Error(), "record the App") || !strings.Contains(err.Error(), "the disk is full") || !strings.Contains(err.Error(), "settings/apps/suiteward-abcd") {
			t.Fatalf("resolveGitHub = %v", err)
		}
		if g.polls != 0 {
			t.Errorf("the flow waited for an installation of an App it could not record (%d polls)", g.polls)
		}
		assertNoKey(t, err.Error(), out.String())
	})
}

// ---- taking up a recorded App ----

func TestResolveGitHubTakingUpARecordedAppStopsAtTheStepThatFails(t *testing.T) {
	pending := &PendingApp{AppID: fakeAppID, Slug: "suiteward-abcd", Account: "acme", AccountType: "Organization"}
	key := func(context.Context) ([]byte, error) { return testKeyPEM(), nil }
	has := func(context.Context) (bool, error) { return true, nil }
	cases := []struct {
		name  string
		setup func(env *githubEnv, g *fakeGitHub)
		want  string
	}{
		{"no way to look for the key", func(env *githubEnv, _ *fakeGitHub) {}, "has no way to look for its key secret"},
		{"the key cannot be looked for", func(env *githubEnv, _ *fakeGitHub) {
			env.KeyExists = func(context.Context) (bool, error) { return false, errors.New("store down") }
			env.ReadKey = key
		}, "look for the App key secret"},
		{"the key cannot be read", func(env *githubEnv, _ *fakeGitHub) {
			env.KeyExists = has
			env.ReadKey = func(context.Context) ([]byte, error) { return nil, errors.New("store down") }
		}, "read the App key secret"},
		{"the key is not a key", func(env *githubEnv, _ *fakeGitHub) {
			env.KeyExists = has
			env.ReadKey = func(context.Context) ([]byte, error) { return []byte("not a pem secret-xyz"), nil }
		}, errResuming.Error()},
		{"the App answers 401", func(env *githubEnv, g *fakeGitHub) {
			env.KeyExists, env.ReadKey = has, key
			g.appStatus = http.StatusUnauthorized
		}, "verify the App with GET /app"},
		{"the installation never comes", func(env *githubEnv, g *fakeGitHub) {
			env.KeyExists, env.ReadKey = has, key
			g.installAfter = 1 << 30
			env.InstallWait = 30 * time.Millisecond
		}, "timed out"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGitHub(t)
			flags := manifestFlags()
			delete(flags, "manifest-code")
			env, out := flowEnv(t, g, flags, strings.NewReader(""), false)
			env.Pending = pending
			tc.setup(&env, g)
			_, err := resolve(t, env)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("resolveGitHub = %v, want an error containing %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "settings/apps/suiteward-abcd") {
				t.Errorf("the error does not say where the App is deleted: %v", err)
			}
			if g.sawRequest("POST /app-manifests") {
				t.Error("a second App was created")
			}
			if strings.Contains(err.Error(), "secret-xyz") || strings.Contains(out.String(), "secret-xyz") {
				t.Error("the key text reached the output")
			}
		})
	}
}

func TestSettingsAppRefusesWhatItCannotLookUp(t *testing.T) {
	g := newFakeGitHub(t)
	env, _ := flowEnv(t, g, manifestFlags(), strings.NewReader(""), false)
	ctx := context.Background()
	env.Existing = &GitHub{AppID: fakeAppID}
	if _, _, found, err := settingsApp(ctx, env); err == nil || found || !strings.Contains(err.Error(), "without an installation id") {
		t.Errorf("no installation: %v %v", found, err)
	}
	env.Existing = &GitHub{AppID: fakeAppID, InstallationID: fakeInstallationID}
	env.KeyExists = func(context.Context) (bool, error) { return false, errors.New("store down") }
	env.ReadKey = func(context.Context) ([]byte, error) { return nil, nil }
	if _, _, found, err := settingsApp(ctx, env); err == nil || found || !strings.Contains(err.Error(), "look for the App key secret") {
		t.Errorf("key lookup: %v %v", found, err)
	}
	env.Existing = nil
	if _, _, found, err := settingsApp(ctx, env); err != nil || found {
		t.Errorf("no settings App is not an error: %v %v", found, err)
	}
}

// ---- the key of an App that exists ----

func TestReadAppKeyAsksOnlyWhereAskingCanWork(t *testing.T) {
	resolver := func(flags map[string]string, terminal Terminal) *Resolver {
		terminal.Out = &syncBuffer{}
		r, err := NewResolver(flags, nil, terminal)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	t.Run("a terminal asks for a file path and a closed input is an error", func(t *testing.T) {
		r := resolver(nil, Terminal{In: strings.NewReader(""), IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, errors.New("no") }})
		if _, err := readAppKey(r); err == nil || !strings.Contains(err.Error(), "read the answer for") {
			t.Errorf("readAppKey = %v", err)
		}
	})
	t.Run("a terminal takes the path it is given", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "k.pem")
		if err := os.WriteFile(path, testKeyPEM(), 0o600); err != nil {
			t.Fatal(err)
		}
		r := resolver(nil, Terminal{In: strings.NewReader(path + "\n"), IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, errors.New("no") }})
		got, err := readAppKey(r)
		if err != nil || string(got) != string(testKeyPEM()) {
			t.Errorf("readAppKey = %q, %v", got, err)
		}
	})
	t.Run("no file and no terminal reads the key from standard input", func(t *testing.T) {
		r := resolver(nil, Terminal{In: strings.NewReader(string(testKeyPEM()))})
		got, err := readAppKey(r)
		if err != nil || string(got) != strings.TrimSuffix(string(testKeyPEM()), "\n") {
			t.Errorf("readAppKey = %q, %v", got, err)
		}
	})
	t.Run("a key on the command line is refused", func(t *testing.T) {
		r := resolver(map[string]string{"app-key": "-----BEGIN"}, Terminal{In: strings.NewReader("")})
		if _, err := readAppKey(r); err == nil || !strings.Contains(err.Error(), "must not be given as an argument") {
			t.Errorf("readAppKey = %v", err)
		}
	})
	t.Run("a file that is not there", func(t *testing.T) {
		r := resolver(map[string]string{"app-key-file": filepath.Join(t.TempDir(), "gone.pem")}, Terminal{In: strings.NewReader("")})
		if _, err := readAppKey(r); err == nil || !strings.Contains(err.Error(), "read the App key file") {
			t.Errorf("readAppKey = %v", err)
		}
	})
}
