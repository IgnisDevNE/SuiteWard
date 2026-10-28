package installer

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// The co-located tier gives up some assurance against the isolated one, and the person must be told. It is a security disclosure, said once
// in plain words: on the page that introduces the tier (pinned with the other tier pages in explain_install_test.go), in the summary of an
// install and of an upgrade of the tier, and, where no summary is drawn (a plain run), as a line at the end of the install. The doctor rows
// of the test runner do not repeat it, so a rewording of reducedAssuranceNotice must fail here.

const reducedAssuranceKey = "catch mistakes, not a determined agent"

func TestTheSummaryOfAColocatedInstallSaysOnceThatTheTierHasReducedAssurance(t *testing.T) {
	withTerminal(t, true)
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	c := newTermConsole(&bytes.Buffer{})
	env := h.newEnv()
	env.OpenScreen = c.open
	if err := Install(liveContext(t), colocatedArgs(h), env); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(flat(c.tail()), reducedAssuranceKey); got != 1 {
		t.Errorf("the summary says it %d times, want once:\n%s", got, c.tail())
	}
}

func TestTheSummaryOfAColocatedUpgradeSaysOnceThatTheTierHasReducedAssurance(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.first("exec -i suiteward /suiteward check --json", instanceChecks)
	c := newTermConsole(&u.out)
	env := u.env()
	env.OpenScreen = c.open
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", testImage, "--yes"}
	if err := Install(liveContext(t), args, env); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(flat(c.tail()), reducedAssuranceKey); got != 1 {
		t.Errorf("the summary says it %d times, want once:\n%s", got, c.tail())
	}
}

// A plain run draws no summary: the install ends with the line, once.
func TestAPlainColocatedInstallEndsWithTheReducedAssuranceLineOnce(t *testing.T) {
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	if err := Install(context.Background(), colocatedArgs(h), h.newEnv()); err != nil {
		t.Fatal(err)
	}
	out := h.out.String()
	if got := strings.Count(out, reducedAssuranceNotice); got != 1 {
		t.Errorf("the plain install says it %d times, want once:\n%s", got, out)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), reducedAssuranceNotice) {
		t.Errorf("the line is not the last thing the install prints:\n%s", out)
	}
}

// The isolated tier has full assurance: it never says the line.
func TestTheIsolatedInstallDoesNotSayTheColocatedTierHasReducedAssurance(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.noImage = true
	var out bytes.Buffer
	env := Env{
		Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &out}, PortFree: func(int) error { return nil }, Sleep: func(d time.Duration) {},
		HTTPGet: func(context.Context, string) ([]byte, error) { return []byte(`{"version":"v0.2.0"}`), nil },
	}
	args := []string{"--tier", "isolated", "--settings", settingsFile(f.home), "--image", testDigest, "--github-app-id", "none"}
	if err := Install(context.Background(), args, env); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), reducedAssuranceKey) {
		t.Errorf("the isolated install speaks of the reduced assurance of the other tier:\n%s", out.String())
	}
}
