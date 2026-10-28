package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeColocatedAStartThatCannotBeAnsweredOrCannotBeHealthyRefuses(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.downUntilStarted()
	if err := u.upgradeAt(true, ""); err == nil || !strings.Contains(err.Error(), "podman --connection pm start suiteward-db") { // the input ended: no answer is not a yes
		t.Fatalf("got %v", err)
	}
	if u.startCalls(ContainerDB) != 0 {
		t.Error("the service was started without an answer")
	}

	u = newUpgradeHarness(t, nil)
	u.downUntilStarted()
	u.first("--format {{json .State}} "+ContainerDB, `{"Status":"exited"}`)
	err := u.upgradeAt(true, "\n", "--yes")
	if err == nil || !strings.Contains(err.Error(), "stopped before it was ready") || !strings.Contains(err.Error(), "docs/backup.md") {
		t.Fatalf("got %v, want the failed health of the database and the manual backup", err)
	}
	if u.startCalls(ContainerServe) != 0 {
		t.Error("the service was started on a database that is not healthy")
	}
	u.noChanges()
}

func TestResolveGitHubNewAppOnItsOwnAndWhenTheKeySecretCannotBeLookedFor(t *testing.T) {
	flags := func() map[string]string {
		f := manifestFlags()
		f["github"] = "new-app"
		return f
	}
	g := newFakeGitHub(t)
	env, out := flowEnv(t, g, flags(), strings.NewReader(""), false)
	if _, err := resolve(t, env); err != nil || !g.sawRequest("POST /app-manifests") { // nothing to replace: no note
		t.Fatalf("got %v, requests %v", err, g.requests)
	}
	if strings.Contains(out.String(), "stays on GitHub") {
		t.Errorf("a note about an App that was never there:\n%s", out.String())
	}

	g = newFakeGitHub(t)
	env, _ = flowEnv(t, g, flags(), strings.NewReader(""), false)
	env.KeyExists = func(context.Context) (bool, error) { return false, errors.New("the secret store is down") }
	if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "the secret store is down") || len(g.requests) != 0 {
		t.Fatalf("got %v, requests %v", err, g.requests)
	}
}

func TestResolveGitHubNamedAppWithoutKeyOnATerminalThatCannotAnswer(t *testing.T) {
	g := newFakeGitHub(t)
	env, _ := namedAppWithoutKey(t, g, manifestFlags(), "", true) // the input ends at the question
	if _, err := resolve(t, env); err == nil || len(g.requests) != 0 {
		t.Fatalf("got %v, requests %v", err, g.requests)
	}
}

func TestResolveGitHubRecordedAppWhoseKeySecretCannotBeRead(t *testing.T) {
	g := newFakeGitHub(t)
	env, _ := flowEnv(t, g, manifestFlags(), strings.NewReader(""), false)
	env.Existing = &GitHub{}
	env.Pending = &PendingApp{AppID: fakeAppID, Slug: "suiteward-abcd", Account: "acme", AccountType: "Organization"}
	env.KeyExists = func(context.Context) (bool, error) { return true, nil }
	env.ReadKey = func(context.Context) ([]byte, error) { return nil, errors.New("secret unreadable") }
	if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "secret unreadable") || g.sawRequest("POST /app-manifests") {
		t.Fatalf("got %v, requests %v", err, g.requests)
	}
}

func TestInstallColocatedGitHubNewAppNamesHowToFreeTheKeySecret(t *testing.T) {
	h, g := savedAppWithoutKey(t)
	h.theKeySecretIsThere()
	err := h.installOn(false, "", append(newAppAnswers, "--github", "new-app")...)
	if err == nil || !strings.Contains(err.Error(), "podman --connection pm secret rm "+SecretGitHubAppKey) {
		t.Fatalf("got %v, want the command that removes the old key secret", err)
	}
	if g.sawRequest("POST /app-manifests") {
		t.Error("an App was created although its key could not be stored")
	}
	h.mustNotMutate()
}

func TestQuoteArg(t *testing.T) {
	for arg, want := range map[string]string{
		"/home/me/s.yaml":    "/home/me/s.yaml",
		`C:\Users\me\s.yaml`: `C:\Users\me\s.yaml`,
		"my dir/s.yaml":      "'my dir/s.yaml'",
		"it's/s.yaml":        `"it's/s.yaml"`,
		"":                   "''",
	} {
		if got := quoteArg(arg); got != want {
			t.Errorf("quoteArg(%q) = %q, want %q", arg, got, want)
		}
	}
}

func TestResolveGitHubNewAppWithAKeySecretAndNoAppNamed(t *testing.T) {
	g := newFakeGitHub(t)
	flags := manifestFlags()
	flags["github"] = "new-app"
	env, _ := flowEnv(t, g, flags, strings.NewReader(""), false)
	env.KeyExists = func(context.Context) (bool, error) { return true, nil }
	env.ReadKey = func(context.Context) ([]byte, error) { return nil, nil }
	if _, err := resolve(t, env); err == nil || !strings.Contains(err.Error(), "remove the secret, then run again") || len(g.requests) != 0 {
		t.Fatalf("got %v, requests %v", err, g.requests)
	}
}

func TestUpgradeColocatedAStartQuestionThatCannotBeReadIsAnError(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.downUntilStarted()
	env := u.env()
	term, _ := fakeTerminal(true, "")
	term.In = failingReader{}
	term.Out = &u.out
	env.Term = term
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", testImage}
	if err := Install(context.Background(), args, env); err == nil || !strings.Contains(err.Error(), "read your answer") || u.startCalls(ContainerDB) != 0 {
		t.Fatalf("got %v", err)
	}
}

func TestInstallColocatedGitHubNewAppWhileARecordedAppHoldsTheKeyNamesTheRecord(t *testing.T) {
	h, g, args := colocatedFlow(t)
	g.neverInstalled(true)
	if err := h.podman(args...); err == nil {
		t.Fatal("the first run must fail")
	}
	h.theKeySecretIsThere() // the key of the recorded App
	err := h.podman(append(args, "--github", "new-app")...)
	if err == nil || !strings.Contains(err.Error(), pendingAppPath(h.settingsPath())) || !strings.Contains(err.Error(), "without --github new-app") {
		t.Fatalf("got %v, want the record and the way to take that App up", err)
	}
	if g.count("POST /app-manifests") != 1 {
		t.Errorf("%d Apps exist, want the one of the first run", g.count("POST /app-manifests"))
	}
}

func TestResolveGitHubAKeyFileThatIsNotAKeyIsNotThatAppsKey(t *testing.T) {
	g := newFakeGitHub(t)
	flags := withoutApp(existingFlags(t, nil))
	flags["app-key-file"] = writeTextFile(t, "this is not a PEM")
	env, _ := flowEnv(t, g, flags, strings.NewReader(""), false)
	env.Existing = &GitHub{}
	env.Pending = &PendingApp{AppID: fakeAppID, Slug: "suiteward-abcd", Account: "acme", AccountType: "Organization"}
	env.KeyExists = func(context.Context) (bool, error) { return false, nil }
	env.ReadKey = func(context.Context) ([]byte, error) { return nil, nil }
	_, err := resolve(t, env)
	if err == nil || !strings.Contains(err.Error(), "not the key of the App suiteward-abcd") || g.sawRequest("POST /app-manifests") {
		t.Fatalf("got %v, requests %v", err, g.requests)
	}
}

func writeTextFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
