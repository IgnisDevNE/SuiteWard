package installer

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

func manifestFlags() map[string]string {
	return map[string]string{"github-account": "acme", "manifest-code": "thecode1", "repositories": "acme/app", "test-check": "CI / test", "owner": "alice"}
}

func TestResolveGitHubStoresTheKeyBeforeWaitingForTheInstallation(t *testing.T) {
	t.Run("right after the conversion", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, manifestFlags(), strings.NewReader(""), false)
		var stored []byte
		calls, pollsAtStore := 0, -1
		env.StoreKey = func(_ context.Context, key []byte) error {
			calls++
			stored = slices.Clone(key)
			g.mu.Lock()
			pollsAtStore = g.polls
			g.mu.Unlock()
			return nil
		}
		res, err := resolve(t, env)
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 || string(stored) != string(testKeyPEM()) || pollsAtStore != 0 {
			t.Fatalf("StoreKey: %d calls, key stored %v, %d polls at the time (want 1, true, 0)", calls, string(stored) == string(testKeyPEM()), pollsAtStore)
		}
		if string(res.PrivateKey) != string(testKeyPEM()) {
			t.Error("the result no longer carries the key")
		}
	})
	t.Run("a failure stops the flow with the delete link", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, out := flowEnv(t, g, manifestFlags(), strings.NewReader(""), false)
		env.StoreKey = func(context.Context, []byte) error { return errors.New("the secret store is down") }
		_, err := resolve(t, env)
		if err == nil || !strings.Contains(err.Error(), "the secret store is down") || !strings.Contains(err.Error(), "settings/apps/suiteward-abcd") {
			t.Fatalf("error = %v", err)
		}
		if g.polls != 0 {
			t.Errorf("the flow went on to wait for the installation (%d polls)", g.polls)
		}
		assertNoKey(t, err.Error(), out.String())
	})
	t.Run("a given App is not stored again", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
		env.StoreKey = func(context.Context, []byte) error { t.Error("StoreKey was called for a given App"); return nil }
		if _, err := resolve(t, env); err != nil {
			t.Fatal(err)
		}
	})
}

func TestResolveGitHubReusesTheAppOfTheSettings(t *testing.T) {
	settings := &GitHub{AppID: fakeAppID, InstallationID: fakeInstallationID, Owner: "acme", Repositories: []string{"acme/app"}}
	withKey := func(env *githubEnv) {
		env.Existing = settings
		env.KeyExists = func(context.Context) (bool, error) { return true, nil }
		env.ReadKey = func(context.Context) ([]byte, error) { return testKeyPEM(), nil }
	}
	noCode := func() map[string]string {
		f := manifestFlags()
		delete(f, "manifest-code")
		return f
	}
	t.Run("verifies and reuses", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, out := flowEnv(t, g, noCode(), strings.NewReader(""), false)
		withKey(&env)
		res, err := resolve(t, env)
		if err != nil {
			t.Fatal(err)
		}
		if res.AppID != fakeAppID || res.InstallationID != fakeInstallationID || res.Slug != "suiteward-abcd" || string(res.PrivateKey) != string(testKeyPEM()) {
			t.Fatalf("result = %v", res)
		}
		if g.sawRequest("POST /app-manifests") || listenerURL.MatchString(out.String()) || !g.sawRequest("GET /app") {
			t.Errorf("the App was not reused: %v", g.requests)
		}
	})
	t.Run("a missing key secret stops the flow: no App is created behind the settings' back", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, manifestFlags(), strings.NewReader(""), false)
		withKey(&env)
		env.KeyExists = func(context.Context) (bool, error) { return false, nil }
		if _, err := resolve(t, env); err == nil || g.sawRequest("POST /app-manifests") {
			t.Fatalf("resolveGitHub = %v, requests %v", err, g.requests)
		}
	})
	t.Run("a key that exists but cannot be read is an error, not a new App", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, manifestFlags(), strings.NewReader(""), false)
		withKey(&env)
		env.ReadKey = func(context.Context) ([]byte, error) { return nil, errors.New("secret unreadable") }
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "secret unreadable") || g.sawRequest("POST /app-manifests") {
			t.Fatalf("error = %v, requests %v", err, g.requests)
		}
	})
	t.Run("an installation that is not the App's", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, noCode(), strings.NewReader(""), false)
		withKey(&env)
		env.Existing = &GitHub{AppID: fakeAppID, InstallationID: 99}
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "99") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("settings without the key hooks are refused", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, manifestFlags(), strings.NewReader(""), false)
		env.Existing = settings
		if _, err := resolve(t, env); err == nil || g.sawRequest("POST /app-manifests") {
			t.Fatalf("error = %v, requests %v", err, g.requests)
		}
	})
	t.Run("an app-id answer wins over the settings", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
		withKey(&env)
		env.ReadKey = func(context.Context) ([]byte, error) { t.Error("the settings key was read"); return nil, nil }
		if _, err := resolve(t, env); err != nil {
			t.Fatal(err)
		}
	})
}

func TestResolveGitHubTestCheckList(t *testing.T) {
	two := func(t *testing.T, flags map[string]string, noRuns ...bool) (GitHubResult, error) {
		g := newFakeGitHub(t)
		g.repos = manyRepos(2)
		if len(noRuns) > 0 {
			g.repos[0].checkRuns, g.repos[1].checkRuns = nil, nil // nothing to offer as a default
		}
		flags = existingFlags(t, flags)
		flags["repositories"] = "acme/r1,acme/r2"
		env, _ := flowEnv(t, g, flags, strings.NewReader(""), false)
		return resolve(t, env)
	}
	t.Run("one entry per repository, as setup takes them", func(t *testing.T) {
		res, err := two(t, map[string]string{"test-check": "acme/r1=one\nacme/r2=CI / two"})
		if err != nil || res.TestChecks["acme/r1"] != "one" || res.TestChecks["acme/r2"] != "CI / two" {
			t.Fatalf("resolveGitHub = %v, %v", res.TestChecks, err)
		}
	})
	t.Run("a repository the list lacks cannot be asked", func(t *testing.T) {
		if _, err := two(t, map[string]string{"test-check": "acme/r1=one"}, true); err == nil || !strings.Contains(err.Error(), "acme/r2") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a repository that is not chosen", func(t *testing.T) {
		if _, err := two(t, map[string]string{"test-check": "acme/r1=one\nacme/r2=two\nacme/r3=three"}); err == nil || !strings.Contains(err.Error(), "acme/r3") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("the key of one repository wins over the list", func(t *testing.T) {
		res, err := two(t, map[string]string{"test-check": "acme/r1=one\nacme/r2=two", "test-check.acme/r2": "mine"})
		if err != nil || res.TestChecks["acme/r1"] != "one" || res.TestChecks["acme/r2"] != "mine" {
			t.Fatalf("resolveGitHub = %v, %v", res.TestChecks, err)
		}
	})
	t.Run("a single check name is not a list", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, existingFlags(t, map[string]string{"test-check": "build=release"}), strings.NewReader(""), false)
		res, err := resolve(t, env)
		if err != nil || res.TestChecks["acme/app"] != "build=release" {
			t.Fatalf("resolveGitHub = %v, %v", res.TestChecks, err)
		}
	})
}

func TestResolveGitHubReadsCheckRunsWithExactlyTheCheckReadToken(t *testing.T) {
	g := newFakeGitHub(t)
	env, _ := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	checksRead := map[string]string{"metadata": "read", "checks": "read"}
	g.mu.Lock()
	defer g.mu.Unlock()
	if want := "Bearer " + fakeTokenFor(checksRead); g.checkRunAuth != want {
		t.Errorf("the check runs were read with %q, want the token minted for exactly %v", g.checkRunAuth, checksRead)
	}
	for _, perms := range g.tokenRequests {
		for name, level := range perms {
			if level != "read" {
				t.Errorf("a token asked for %s: %s; the install flow only reads", name, level)
			}
		}
		if _, ok := perms["contents"]; ok {
			t.Errorf("a token asked for contents: %v", perms)
		}
	}
}

func TestResolveGitHubRefusesATestCheckListThatNamesARepositoryTwice(t *testing.T) {
	for name, list := range map[string]string{
		"the same case": "acme/r1=one\nacme/r1=two\nacme/r2=three",
		"another case":  "acme/r1=one\nACME/R1=two\nacme/r2=three",
	} {
		t.Run(name, func(t *testing.T) {
			g := newFakeGitHub(t)
			g.repos = manyRepos(2)
			flags := existingFlags(t, map[string]string{"repositories": "acme/r1,acme/r2", "test-check": list})
			env, _ := flowEnv(t, g, flags, strings.NewReader(""), false)
			if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "twice") {
				t.Fatalf("error = %v, want a refusal that says the repository is listed twice", err)
			}
		})
	}
}

// withTerminal makes runSetup see a terminal, or not, for the test.
func withTerminal(t *testing.T, terminal bool) {
	t.Helper()
	old := setupTerminal
	setupTerminal = func() bool { return terminal }
	t.Cleanup(func() { setupTerminal = old })
}

func TestRunSetupNeedsARealTerminal(t *testing.T) {
	withTerminal(t, false)
	rt := &ttyRuntime{}
	var out strings.Builder
	err := runSetup(context.Background(), Deps{}, rt, "alice", map[string]string{"acme/app": "CI"}, &out)
	if err == nil || rt.calls != 0 || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("runSetup = %v after %d calls; want a refusal that names the terminal", err, rt.calls)
	}
	if strings.Contains(out.String(), "recording") {
		t.Errorf("the warning was printed although setup does not run:\n%s", out.String())
	}
}

func TestRunSetupBoundsEachCheck(t *testing.T) {
	withTerminal(t, true)
	rt := &ttyRuntime{}
	long := strings.Repeat("c", owner.MaxTestCheckBytes+1)
	if err := runSetup(context.Background(), Deps{}, rt, "alice", map[string]string{"acme/app": long}, io.Discard); err == nil || rt.calls != 0 || !strings.Contains(err.Error(), "acme/app") {
		t.Fatalf("runSetup = %v after %d calls; want a refusal naming the repository", err, rt.calls)
	}
	exact := strings.Repeat("c", owner.MaxTestCheckBytes)
	if err := runSetup(context.Background(), Deps{}, rt, "alice", map[string]string{"acme/app": exact}, io.Discard); err != nil || rt.calls != 1 {
		t.Fatalf("runSetup at the limit = %v after %d calls", err, rt.calls)
	}
}
