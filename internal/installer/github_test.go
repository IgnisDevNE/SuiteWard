package installer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// flowEnv is an environment for resolveGitHub against g. flags are the answers; in is the terminal input.
func flowEnv(t *testing.T, g *fakeGitHub, flags map[string]string, in io.Reader, terminal bool) (githubEnv, *syncBuffer) {
	t.Helper()
	out := &syncBuffer{}
	answers := map[string]string{}
	if code, ok := flags["manifest-code"]; ok { // a credential: the answers file carries it, never a flag
		answers["manifest-code"] = code
		delete(flags, "manifest-code")
	}
	r, err := NewResolver(flags, answers, Terminal{In: in, Out: out, IsTerminal: terminal, ReadPassword: func() ([]byte, error) { return nil, errors.New("no password here") }})
	if err != nil {
		t.Fatal(err)
	}
	return githubEnv{
		R: r, Out: out, HTTP: g.srv.Client(), APIURL: g.srv.URL, WebURL: "https://github.example", Now: time.Now, Tier: config.TierCoLocated,
		SettingsDir: t.TempDir(), SkipRulesetWait: true, ManifestWait: 5 * time.Second, InstallWait: 2 * time.Second, PollEvery: 2 * time.Millisecond,
	}, out
}

// existingFlags are the answers of an install that already has its App: nothing is created on GitHub.
func existingFlags(t *testing.T, extra map[string]string) map[string]string {
	t.Helper()
	keyFile := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(keyFile, testKeyPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	flags := map[string]string{
		"app-id": "4242", "installation-id": "77", "app-key-file": keyFile,
		"repositories": "acme/app", "test-check": "CI / test", "owner": "alice",
	}
	for k, v := range extra {
		flags[k] = v
	}
	return flags
}

func assertNoKey(t *testing.T, texts ...string) {
	t.Helper()
	for _, text := range texts {
		for _, secret := range []string{pemBody(), "client-secret-value", "webhook-secret-value", "BEGIN RSA PRIVATE KEY"} {
			if strings.Contains(text, secret) {
				t.Errorf("a secret reached the output or an error:\n%s", text)
			}
		}
	}
}

func resolve(t *testing.T, env githubEnv) (GitHubResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return resolveGitHub(ctx, env)
}

func TestResolveGitHubWithAnExistingApp(t *testing.T) {
	g := newFakeGitHub(t)
	env, out := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
	res, err := resolve(t, env)
	if err != nil {
		t.Fatal(err)
	}
	if res.AppID != fakeAppID || res.Slug != "suiteward-abcd" || res.InstallationID != fakeInstallationID || res.Account != "acme" || res.AccountType != "Organization" ||
		res.OwnerLogin != "alice" || !slices.Equal(res.Repositories, []string{"acme/app"}) || res.TestChecks["acme/app"] != "CI / test" || string(res.PrivateKey) != string(testKeyPEM()) {
		t.Fatalf("result = %v (%+v)", res, res.TestChecks)
	}
	if got := res.Protection["acme/app"].Kind; got != protection.KindEnforced {
		t.Errorf("protection = %q, want enforced", got)
	}
	if g.sawRequest("POST /app-manifests") || listenerURL.MatchString(out.String()) {
		t.Errorf("an App was created although one was given: %v\n%s", g.requests, out.String())
	}
	if !g.sawRequest("GET /app") {
		t.Error("the given App was not verified with GET /app")
	}
	assertNoKey(t, out.String(), fmt.Sprint(res), fmt.Sprintf("%+v", res), fmt.Sprintf("%#v", res))
	entries, err := os.ReadDir(env.SettingsDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "suiteward-ruleset-acme-app.json" {
		t.Fatalf("settings dir = %v, %v; want only the ruleset", entries, err)
	}
	raw, _ := os.ReadFile(filepath.Join(env.SettingsDir, entries[0].Name()))
	if string(raw) != goldenRuleset {
		t.Errorf("ruleset file:\n%s", raw)
	}
	for _, want := range []string{"https://github.example/acme/app/settings/rules", "suiteward-ruleset-acme-app.json", "enforced"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "GitHub Free") {
		t.Errorf("the Free-plan warning was printed for a public repository:\n%s", out.String())
	}
}

func TestResolveGitHubRefusesWhatItCannotVerify(t *testing.T) {
	t.Run("an installation that is not the App's", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, existingFlags(t, map[string]string{"installation-id": "99"}), strings.NewReader(""), false)
		g.installAfter = 0
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "99") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a key file that is not a key never shows its content", func(t *testing.T) {
		g := newFakeGitHub(t)
		bad := filepath.Join(t.TempDir(), "bad.pem")
		if err := os.WriteFile(bad, []byte("not a pem secret-xyz"), 0o600); err != nil {
			t.Fatal(err)
		}
		env, out := flowEnv(t, g, existingFlags(t, map[string]string{"app-key-file": bad}), strings.NewReader(""), false)
		_, err := resolve(t, env)
		if err == nil || strings.Contains(err.Error(), "secret-xyz") || strings.Contains(out.String(), "secret-xyz") {
			t.Fatalf("error = %v\n%s", err, out.String())
		}
	})
	t.Run("an App id without an installation id", func(t *testing.T) {
		g := newFakeGitHub(t)
		flags := existingFlags(t, nil)
		delete(flags, "installation-id")
		env, _ := flowEnv(t, g, flags, strings.NewReader(""), false)
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "installation-id") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestResolveGitHubManifestFlow(t *testing.T) {
	g := newFakeGitHub(t)
	g.installAfter = 2 // the installation appears at the third poll
	env, out := flowEnv(t, g, map[string]string{"github-account": "acme", "repositories": "acme/app", "test-check": "CI / test", "owner": "alice"}, strings.NewReader(""), false)
	type result struct {
		res GitHubResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := resolve(t, env)
		done <- result{res, err}
	}()
	base := waitForListener(t, out)
	action, _ := fetchForm(t, base)
	if status := callback(t, base, "thecode1", stateOf(t, action)); status != 200 {
		t.Fatalf("callback answered %d", status)
	}
	var got result
	select {
	case got = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("resolveGitHub did not return")
	}
	if got.err != nil {
		t.Fatalf("resolveGitHub: %v\n%s", got.err, out.String())
	}
	res := got.res
	if res.AppID != fakeAppID || res.Slug != "suiteward-abcd" || res.InstallationID != fakeInstallationID || res.Account != "acme" || string(res.PrivateKey) != string(testKeyPEM()) {
		t.Fatalf("result = %v", res)
	}
	if g.polls != 3 {
		t.Errorf("polled %d times, want 3", g.polls)
	}
	if !strings.Contains(out.String(), "https://github.example/apps/suiteward-abcd/installations/new") {
		t.Errorf("the installation link was not printed:\n%s", out.String())
	}
	assertNoKey(t, out.String(), fmt.Sprint(res))
	entries, _ := os.ReadDir(env.SettingsDir)
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(env.SettingsDir, e.Name()))
		assertNoKey(t, string(raw))
	}
}

func TestResolveGitHubInstallationWait(t *testing.T) {
	t.Run("times out and names the App to delete", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.installAfter = 1 << 30
		env, out := flowEnv(t, g, map[string]string{"github-account": "acme", "manifest-code": "thecode1"}, strings.NewReader(""), false)
		env.InstallWait = 80 * time.Millisecond
		_, err := resolve(t, env)
		if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "suiteward-abcd") || !strings.Contains(err.Error(), "settings/apps/suiteward-abcd") {
			t.Fatalf("error = %v", err)
		}
		assertNoKey(t, err.Error(), out.String())
	})
	t.Run("an installation on another account does not count", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.installAccount, g.installType = "other", "Organization"
		env, _ := flowEnv(t, g, map[string]string{"github-account": "acme", "manifest-code": "thecode1"}, strings.NewReader(""), false)
		env.InstallWait = 80 * time.Millisecond
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a refused conversion names the App too", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.convertPerms = map[string]string{"metadata": "read"}
		env, _ := flowEnv(t, g, map[string]string{"github-account": "acme", "manifest-code": "thecode1"}, strings.NewReader(""), false)
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "suiteward-abcd") {
			t.Fatalf("error = %v", err)
		}
	})
}

func manyRepos(n int) []fakeRepo {
	var repos []fakeRepo
	for i := 1; i <= n; i++ {
		repos = append(repos, fakeRepo{name: fmt.Sprintf("acme/r%d", i), branch: "main", checkRuns: []string{"CI / test"}})
	}
	return repos
}

func TestResolveGitHubRepositories(t *testing.T) {
	t.Run("an installation that covers none", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.repos = nil
		env, _ := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "no repositor") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("more than 20 chosen", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.repos = manyRepos(21)
		all := make([]string, 21)
		for i := range all {
			all[i] = fmt.Sprintf("%d", i+1)
		}
		env, _ := flowEnv(t, g, existingFlags(t, map[string]string{"repositories": strings.Join(all, ",")}), strings.NewReader(""), false)
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "20") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("exactly 20 chosen", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.repos = manyRepos(21)
		var names []string
		flags := existingFlags(t, nil)
		for i := 1; i <= 20; i++ {
			names = append(names, fmt.Sprintf("acme/r%d", i))
			flags[fmt.Sprintf("test-check.acme/r%d", i)] = "CI / test"
		}
		flags["repositories"] = strings.Join(names, ",")
		delete(flags, "test-check")
		env, _ := flowEnv(t, g, flags, strings.NewReader(""), false)
		res, err := resolve(t, env)
		if err != nil || len(res.Repositories) != 20 {
			t.Fatalf("resolveGitHub = %d repositories, %v", len(res.Repositories), err)
		}
	})
	t.Run("more than 20 covered and no answer cannot be asked", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.repos = manyRepos(21)
		flags := existingFlags(t, nil)
		delete(flags, "repositories")
		env, _ := flowEnv(t, g, flags, strings.NewReader(""), false)
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "repositories") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("all of a short list is the default when asking is impossible", func(t *testing.T) {
		g := newFakeGitHub(t)
		flags := existingFlags(t, nil)
		delete(flags, "repositories")
		env, _ := flowEnv(t, g, flags, strings.NewReader(""), false)
		res, err := resolve(t, env)
		if err != nil || !slices.Equal(res.Repositories, []string{"acme/app"}) {
			t.Fatalf("resolveGitHub = %v, %v", res.Repositories, err)
		}
	})
	t.Run("a number picks from the printed list", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.repos = manyRepos(3)
		env, out := flowEnv(t, g, existingFlags(t, map[string]string{"repositories": "2"}), strings.NewReader(""), false)
		res, err := resolve(t, env)
		if err != nil || !slices.Equal(res.Repositories, []string{"acme/r2"}) {
			t.Fatalf("resolveGitHub = %v, %v", res.Repositories, err)
		}
		if !strings.Contains(out.String(), "2) acme/r2") {
			t.Errorf("the list was not printed:\n%s", out.String())
		}
	})
	t.Run("a repository outside the installation", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, existingFlags(t, map[string]string{"repositories": "acme/elsewhere"}), strings.NewReader(""), false)
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "acme/elsewhere") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestResolveGitHubOwnerLogin(t *testing.T) {
	t.Run("a personal account suggests itself", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.ownerLogin, g.ownerType = "alice", "User"
		g.repos[0].name = "alice/app"
		flags := existingFlags(t, map[string]string{"repositories": "alice/app"})
		delete(flags, "owner")
		env, _ := flowEnv(t, g, flags, strings.NewReader(""), false)
		res, err := resolve(t, env)
		if err != nil || res.OwnerLogin != "alice" {
			t.Fatalf("resolveGitHub = %q, %v", res.OwnerLogin, err)
		}
	})
	t.Run("an organization has no suggestion", func(t *testing.T) {
		g := newFakeGitHub(t)
		flags := existingFlags(t, nil)
		delete(flags, "owner")
		env, _ := flowEnv(t, g, flags, strings.NewReader(""), false)
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "owner") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a login that could be read as a flag", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, existingFlags(t, map[string]string{"owner": "-x"}), strings.NewReader(""), false)
		if _, err := resolve(t, env); err == nil {
			t.Fatal("owner -x was accepted")
		}
	})
}

func TestResolveGitHubTestChecks(t *testing.T) {
	t.Run("offers the check runs, once each and never the contract check", func(t *testing.T) {
		g := newFakeGitHub(t)
		flags := existingFlags(t, nil)
		delete(flags, "test-check")
		env, out := flowEnv(t, g, flags, strings.NewReader("2\n"), true)
		res, err := resolve(t, env)
		if err != nil || res.TestChecks["acme/app"] != "lint" {
			t.Fatalf("resolveGitHub = %v, %v\n%s", res.TestChecks, err, out.String())
		}
		text := out.String()
		if !strings.Contains(text, "1) CI / test") || !strings.Contains(text, "2) lint") || strings.Contains(text, "3)") {
			t.Errorf("the offered checks are wrong:\n%s", text)
		}
		if strings.Contains(text, ") SuiteWard / Contract") {
			t.Errorf("the contract check was offered:\n%s", text)
		}
	})
	t.Run("never offers the receipt check, which is not a test", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.repos[0].checkRuns = []string{"CI / test", "SuiteWard / Contract", "SuiteWard / Receipt", "lint"}
		flags := existingFlags(t, nil)
		delete(flags, "test-check")
		env, out := flowEnv(t, g, flags, strings.NewReader("2\n"), true)
		res, err := resolve(t, env)
		if err != nil || res.TestChecks["acme/app"] != "lint" {
			t.Fatalf("resolveGitHub = %v, %v\n%s", res.TestChecks, err, out.String())
		}
		if text := out.String(); strings.Contains(text, "SuiteWard / Receipt") || strings.Contains(text, "3)") {
			t.Errorf("the receipt check was offered:\n%s", text)
		}
	})
	t.Run("no check runs: type the name, with a warning", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.repos[0].checkRuns = nil
		env, out := flowEnv(t, g, existingFlags(t, map[string]string{"test-check": "build"}), strings.NewReader(""), false)
		res, err := resolve(t, env)
		if err != nil || res.TestChecks["acme/app"] != "build" {
			t.Fatalf("resolveGitHub = %v, %v", res.TestChecks, err)
		}
		if !strings.Contains(out.String(), "missing-test-check") {
			t.Errorf("the warning is missing:\n%s", out.String())
		}
	})
	t.Run("no check runs and no answer cannot be asked", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.repos[0].checkRuns = nil
		flags := existingFlags(t, nil)
		delete(flags, "test-check")
		env, _ := flowEnv(t, g, flags, strings.NewReader(""), false)
		if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "test-check") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("the contract check is not a test check", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, existingFlags(t, map[string]string{"test-check": protection.ContractCheck}), strings.NewReader(""), false)
		if _, err := resolve(t, env); err == nil {
			t.Fatal("the contract check was accepted as the test check")
		}
	})
	t.Run("a repository chooses its own with the repository key", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.repos = manyRepos(2)
		flags := existingFlags(t, map[string]string{"repositories": "acme/r1,acme/r2", "test-check.acme/r1": "one", "test-check.acme/r2": "two"})
		delete(flags, "test-check")
		env, _ := flowEnv(t, g, flags, strings.NewReader(""), false)
		res, err := resolve(t, env)
		if err != nil || res.TestChecks["acme/r1"] != "one" || res.TestChecks["acme/r2"] != "two" {
			t.Fatalf("resolveGitHub = %v, %v", res.TestChecks, err)
		}
	})
	t.Run("the check runs are read with a token of the App's own permissions", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
		if _, err := resolve(t, env); err != nil {
			t.Fatal(err)
		}
		if !g.sawRequest("GET /repos/acme/app/commits/main/check-runs") {
			t.Fatalf("the check runs of the default branch were not read: %v", g.requests)
		}
	})
}

func TestResolveGitHubWarnsAboutPrivateRepositoriesOnFree(t *testing.T) {
	g := newFakeGitHub(t)
	g.repos[0].private = true
	env, out := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
	res, err := resolve(t, env)
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"GitHub Free", "acme/app", "public repository"} {
		if !strings.Contains(text, want) {
			t.Errorf("the warning lacks %q:\n%s", want, text)
		}
	}
	// The warning comes before the import steps, so the user reads it before importing.
	if strings.Index(text, "GitHub Free") > strings.Index(text, "settings/rules") {
		t.Errorf("the warning comes after the import link:\n%s", text)
	}
	if res.Protection["acme/app"].Kind != protection.KindEnforced {
		t.Errorf("a continuing install must still report: %+v", res.Protection)
	}
}

func TestResolveGitHubReportsTheProtectionItReads(t *testing.T) {
	t.Run("enforcement unavailable", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.repos[0].private = true
		g.repos[0].rulesStatus = 403
		env, out := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
		res, err := resolve(t, env)
		if err != nil || res.Protection["acme/app"].Kind != protection.KindEnforcementUnavailable {
			t.Fatalf("resolveGitHub = %+v, %v", res.Protection, err)
		}
		if !strings.Contains(out.String(), "enforcement-unavailable") {
			t.Errorf("the result was not reported:\n%s", out.String())
		}
	})
	t.Run("findings", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.repos[0].noRule = true
		env, out := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
		res, err := resolve(t, env)
		if err != nil || res.Protection["acme/app"].Kind != protection.KindFindings || !slices.Contains(res.Protection["acme/app"].Findings, protection.MissingContractCheck) {
			t.Fatalf("resolveGitHub = %+v, %v", res.Protection, err)
		}
		if !strings.Contains(out.String(), "missing-contract-check") {
			t.Errorf("the findings were not reported:\n%s", out.String())
		}
	})
}

func TestResolveGitHubWaitsForTheImport(t *testing.T) {
	g := newFakeGitHub(t)
	env, out := flowEnv(t, g, existingFlags(t, nil), strings.NewReader("\n"), true)
	env.SkipRulesetWait = false
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Press Enter when the rulesets are imported") {
		t.Errorf("the user was not asked to confirm the import:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(env.SettingsDir, "suiteward-ruleset-acme-app.json")); err != nil {
		t.Error(err)
	}
	// With the wait skipped nothing is asked at all.
	env, out = flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), true)
	if _, err := resolve(t, env); err != nil || strings.Contains(out.String(), "Press Enter when") {
		t.Fatalf("resolveGitHub with the wait skipped: %v\n%s", err, out.String())
	}
}

func TestResolveGitHubRulesetFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	g := newFakeGitHub(t)
	env, _ := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(env.SettingsDir, "suiteward-ruleset-acme-app.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v", info, err)
	}
}

// ttyRuntime is a Runtime where only ExecTTY and its command line exist: any other call panics on the nil embedded interface.
type ttyRuntime struct {
	Runtime
	container string
	argv      []string
	calls     int
	err       error
}

func (r *ttyRuntime) ExecTTY(_ context.Context, container string, argv []string) error {
	r.calls++
	r.container, r.argv = container, slices.Clone(argv)
	return r.err
}

// ExecTTYCommand is the command line of an engine with a connection, as the real runtimes make it.
func (r *ttyRuntime) ExecTTYCommand(container string, argv []string) (string, []string) {
	return "podman", append([]string{"--connection", "pm", "exec", "-it", container}, argv...)
}

func TestRunSetupPassesTheTerminalThroughAndCapturesNothing(t *testing.T) {
	withTerminal(t, true)
	rt := &ttyRuntime{}
	var out strings.Builder
	checks := map[string]string{"acme/lib": "build", "acme/app": "CI / test"}
	if err := runSetup(context.Background(), "suiteward", rt, "alice", checks, &out); err != nil {
		t.Fatal(err)
	}
	want := []string{"/suiteward", "setup", "--owner", "alice", "--test-check", "acme/app=CI / test", "--test-check", "acme/lib=build"}
	if rt.calls != 1 || rt.container != ContainerServe || !slices.Equal(rt.argv, want) {
		t.Fatalf("ExecTTY(%q, %q) after %d calls, want %q %q", rt.container, rt.argv, rt.calls, ContainerServe, want)
	}
	for _, want := range []string{"recording"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the warning lacks %q:\n%s", want, out.String())
		}
	}
	// The installer prints the warning and nothing from setup: the output holds neither the arguments nor any answer.
	for _, leaked := range []string{"acme/app=CI", "otpauth", "--test-check"} {
		if strings.Contains(out.String(), leaked) {
			t.Errorf("the output holds %q:\n%s", leaked, out.String())
		}
	}
}

func TestRunSetupRefusesWhatSetupWouldRefuse(t *testing.T) {
	withTerminal(t, true)
	for name, tc := range map[string]struct {
		owner  string
		checks map[string]string
	}{
		"an owner that reads as a flag": {"-x", map[string]string{"acme/app": "CI"}},
		"no owner":                      {"", map[string]string{"acme/app": "CI"}},
		"no checks":                     {"alice", nil},
		"an empty check":                {"alice", map[string]string{"acme/app": " "}},
		"a bad repository":              {"alice", map[string]string{"acme": "CI"}},
	} {
		t.Run(name, func(t *testing.T) {
			rt := &ttyRuntime{}
			if err := runSetup(context.Background(), "suiteward", rt, tc.owner, tc.checks, io.Discard); err == nil || rt.calls != 0 {
				t.Fatalf("runSetup = %v after %d calls", err, rt.calls)
			}
		})
	}
	t.Run("a failure of setup is returned", func(t *testing.T) {
		rt := &ttyRuntime{err: errors.New("exit status 1")}
		if err := runSetup(context.Background(), "suiteward", rt, "alice", map[string]string{"acme/app": "CI"}, io.Discard); !errors.Is(err, rt.err) {
			t.Fatalf("runSetup = %v", err)
		}
	})
}

func TestGitHubResultNeverRendersTheKey(t *testing.T) {
	res := GitHubResult{AppID: 1, PrivateKey: testKeyPEM()}
	for _, text := range []string{fmt.Sprintf("%v", res), fmt.Sprintf("%+v", res), fmt.Sprintf("%#v", res), fmt.Sprint(&res)} {
		assertNoKey(t, text)
	}
}

func TestGitHubResultKeyDoesNotSurviveAnEncoder(t *testing.T) {
	res := GitHubResult{AppID: 1, PrivateKey: testKeyPEM()}
	// An encoder writes a []byte as base64, so that is what to look for.
	leak := base64.StdEncoding.EncodeToString(testKeyPEM())[:40]
	asJSON, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	asYAML, err := yaml.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	for name, encoded := range map[string]string{"json": string(asJSON), "yaml": string(asYAML)} {
		if strings.Contains(encoded, leak) {
			t.Errorf("%s of a GitHubResult carries the private key:\n%s", name, encoded)
		}
	}
}
