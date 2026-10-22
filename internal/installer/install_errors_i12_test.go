package installer

import (
	"context"
	"errors"
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
	if err == nil || !strings.Contains(err.Error(), "exited before it became healthy") || !strings.Contains(err.Error(), "docs/backup.md") {
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
