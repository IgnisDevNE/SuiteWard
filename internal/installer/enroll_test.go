package installer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

const (
	statusNoOwner  = `{"version":"v0.2.0"}`
	statusEnrolled = `{"version":"v0.2.0","owner":{"login":"bob","accountId":7,"enrolledAt":"2026-10-01T00:00:00Z"}}`
)

func statusIs(body string) func(context.Context, string) ([]byte, error) {
	return func(context.Context, string) ([]byte, error) { return []byte(body), nil }
}

// enrollHarness is a co-located stack that runs, with GitHub settings and a test check per repository, on a real terminal.
func enrollHarness(t *testing.T, status string) *harness {
	t.Helper()
	return enrollHarnessWith(t, status, func(*Settings) {})
}

func enrollHarnessWith(t *testing.T, status string, edit func(*Settings)) *harness {
	t.Helper()
	withTerminal(t, true)
	h := newHarness(t, "linux")
	s := localPS1Settings()
	s.Connection = "pm"
	s.GitHub.TestChecks = map[string]string{"Acme/api": "CI / test", "Acme/web": "CI / web"}
	edit(&s)
	if err := SaveSettings(h.settingsPath(), s); err != nil {
		t.Fatal(err)
	}
	h.existingStack(s, testImage, nil)
	h.status = statusIs(status)
	return h
}

// enroll runs `suiteward enroll` on a terminal that types stdin.
func (h *harness) enroll(stdin string, args ...string) (int, string) {
	h.t.Helper()
	term, _ := fakeTerminal(true, stdin)
	return h.enrollOn(term, args...)
}

func (h *harness) enrollOn(term Terminal, args ...string) (int, string) {
	h.t.Helper()
	return h.enrollWith(term, append([]string{"--settings", h.settingsPath()}, args...)...)
}

// enrollWith runs `suiteward enroll` with the arguments as they are: a test of what a run with no settings file does gives no --settings.
func (h *harness) enrollWith(term Terminal, args ...string) (int, string) {
	h.t.Helper()
	env := h.newEnv()
	term.Out = &h.out
	env.Term = term
	var stderr bytes.Buffer
	code := Main(context.Background(), append([]string{"enroll"}, args...), env, &stderr)
	return code, stderr.String()
}

func (h *harness) setupArgs() string {
	h.t.Helper()
	if len(h.f.tty) != 1 {
		h.t.Fatalf("setup ran %d times, want once; stdout:\n%s", len(h.f.tty), h.out.String())
	}
	return h.f.tty[0].line()
}

func TestEnrollNeedsATerminal(t *testing.T) {
	h := enrollHarness(t, statusNoOwner)
	withTerminal(t, false)
	code, stderr := h.enroll("", "--owner", "alice")
	if code != 1 || !strings.Contains(stderr, "enroll needs a terminal: setup prints the TOTP secret") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if len(h.f.tty) != 0 || len(h.mutations()) != 0 {
		t.Errorf("something ran without a terminal: %v %v", h.f.tty, h.mutations())
	}
}

func TestEnrollRefusesWhenTheServiceDoesNotAnswer(t *testing.T) {
	h := enrollHarness(t, statusNoOwner)
	h.status = func(context.Context, string) ([]byte, error) { return nil, errors.New("connection refused") }
	code, stderr := h.enroll("", "--owner", "alice")
	if code != 1 || !strings.Contains(stderr, "the service must be running: start it, then run enroll again") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if len(h.f.tty) != 0 {
		t.Error("setup ran although /status cannot be read")
	}
}

func TestEnrollOfAnEnrolledOwnerWarnsAndNeedsTheTypedWord(t *testing.T) {
	for name, typed := range map[string]string{"nothing": "\n", "yes": "yes\n", "the login": "bob\n", "a near miss": "enrol\n"} {
		h := enrollHarness(t, statusEnrolled)
		code, stderr := h.enroll(typed, "--owner", "alice")
		if code != 1 || !strings.Contains(stderr, "not confirmed") {
			t.Errorf("%s: exit %d, stderr %q", name, code, stderr)
		}
		if len(h.f.tty) != 0 {
			t.Errorf("%s: setup ran without the confirmation", name)
		}
		for _, want := range []string{"bob", "the TOTP secret", "old authenticator entry stops working", `Type "enroll"`} {
			if !strings.Contains(h.out.String(), want) {
				t.Errorf("%s: the warning lacks %q:\n%s", name, want, h.out.String())
			}
		}
	}
	h := enrollHarness(t, statusEnrolled)
	if code, stderr := h.enroll("enroll\n", "--owner", "alice"); code != 0 {
		t.Fatalf("exit %d, stderr %q\n%s", code, stderr, h.out.String())
	}
	want := "--connection pm exec -it suiteward /suiteward setup --owner alice --test-check Acme/api=CI / test --test-check Acme/web=CI / web"
	if got := h.setupArgs(); got != want {
		t.Errorf("setup ran as %q, want %q", got, want)
	}
}

func TestEnrollWithNoOwnerEnrolledNeedsNoConfirmation(t *testing.T) {
	h := enrollHarness(t, statusNoOwner)
	if code, stderr := h.enroll("", "--owner", "alice"); code != 0 {
		t.Fatalf("exit %d, stderr %q\n%s", code, stderr, h.out.String())
	}
	h.setupArgs()
	if strings.Contains(h.out.String(), "Type \"enroll\"") || strings.Contains(h.out.String(), "replaces the TOTP secret") {
		t.Errorf("a first enrollment was warned about a replacement:\n%s", h.out.String())
	}
	if !strings.Contains(h.out.String(), "Stop any session recording") {
		t.Errorf("the recording warning of setup is missing:\n%s", h.out.String())
	}
	if !strings.Contains(h.out.String(), "alice is enrolled as the owner.\n") {
		t.Errorf("a finished enrollment does not say so:\n%s", h.out.String())
	}
}

func TestEnrollAsksForTheOwnerAndOffersTheEnrolledOne(t *testing.T) {
	h := enrollHarness(t, statusNoOwner)
	if code, stderr := h.enroll("carol\n"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if got := h.setupArgs(); !strings.Contains(got, "setup --owner carol ") {
		t.Errorf("setup ran as %q", got)
	}
	h = enrollHarness(t, statusEnrolled)
	if code, stderr := h.enroll("\nenroll\n"); code != 0 { // Enter for the offered login, then the confirmation
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if got := h.setupArgs(); !strings.Contains(got, "setup --owner bob ") || !strings.Contains(h.out.String(), "[bob]") {
		t.Errorf("setup ran as %q; the prompt:\n%s", got, h.out.String())
	}
}

func TestEnrollTestChecksComeFromTheFlagsThenTheSettingsThenThePrompt(t *testing.T) {
	h := enrollHarness(t, statusNoOwner)
	if code, stderr := h.enroll("", "--owner", "alice", "--test-check", "Acme/web=Other web"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if got := h.setupArgs(); !strings.HasSuffix(got, "--test-check Acme/api=CI / test --test-check Acme/web=Other web") {
		t.Errorf("flag over settings: %q", got)
	}

	// The settings have no check for the second repository: it is asked for, in the order of the repositories.
	h = enrollHarness(t, statusNoOwner)
	s := h.settings()
	s.GitHub.TestChecks = map[string]string{"Acme/api": "CI / test"}
	if err := SaveSettings(h.settingsPath(), s); err != nil {
		t.Fatal(err)
	}
	if code, stderr := h.enroll("Typed web check\n", "--owner", "alice"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if got := h.setupArgs(); !strings.HasSuffix(got, "--test-check Acme/api=CI / test --test-check Acme/web=Typed web check") {
		t.Errorf("prompted check: %q", got)
	}
	if !strings.Contains(h.out.String(), "Acme/web") {
		t.Errorf("the prompt does not name the repository:\n%s", h.out.String())
	}
}

func TestEnrollRefusesATestCheckOfAnotherRepository(t *testing.T) {
	h := enrollHarness(t, statusNoOwner)
	code, stderr := h.enroll("", "--owner", "alice", "--test-check", "Other/repo=CI")
	if code != 1 || !strings.Contains(stderr, "Other/repo") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if len(h.f.tty) != 0 {
		t.Error("setup ran with a check for a repository the instance does not serve")
	}
}

func TestEnrollNeedsAGitHubApp(t *testing.T) {
	h := enrollHarnessWith(t, statusNoOwner, func(s *Settings) { s.GitHub = GitHub{} })
	code, stderr := h.enroll("", "--owner", "alice")
	if code != 1 || !strings.Contains(stderr, "GitHub") || len(h.f.tty) != 0 {
		t.Fatalf("exit %d, stderr %q, setup %v", code, stderr, h.f.tty)
	}
}

func TestEnrollNeedsAnInstalledStack(t *testing.T) {
	withTerminal(t, true)
	useDefaultConfigDir(t)
	h := newHarness(t, "linux") // no containers, no settings
	h.status = statusIs(statusNoOwner)
	term, _ := fakeTerminal(true, "")
	code, stderr := h.enrollWith(term, "--owner", "alice", "--tier", "co-located")
	if code != 1 || !strings.Contains(stderr, "nothing is installed") || len(h.f.tty) != 0 {
		t.Fatalf("exit %d, stderr %q, setup %v", code, stderr, h.f.tty)
	}
}

func TestEnrollRunsSetupWithoutPuttingASecretOnTheCommandLineOrInTheOutput(t *testing.T) {
	h := enrollHarness(t, statusNoOwner)
	if code, stderr := h.enroll("", "--owner", "alice"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, c := range h.f.calls { // setup goes through the terminal call, never through a captured one
		if strings.Contains(c.line(), "setup") {
			t.Errorf("setup ran through a captured call: %s", c.line())
		}
	}
	h.f.assertNoSecret(t, "totp", "TOTP-SECRET")
	if strings.Contains(h.out.String(), "otpauth://") {
		t.Error("the installer printed an enrollment URI")
	}
}

func TestEnrollIsInTheUsage(t *testing.T) {
	if !strings.Contains(Usage, "suiteward enroll") {
		t.Errorf("usage lacks the enroll verb:\n%s", Usage)
	}
	h := enrollHarness(t, statusNoOwner)
	if code, stderr := h.enroll("", "--bogus"); code != 2 || !strings.Contains(stderr, "suiteward enroll") {
		t.Errorf("a bad flag: exit %d, stderr %q", code, stderr)
	}
}

// ---- the isolated tier ----

// isoEnroll is an isolated host that runs, with GitHub settings and the checks of two repositories.
type isoEnroll struct {
	t   *testing.T
	f   *isoFake
	out bytes.Buffer
}

func newIsoEnroll(t *testing.T) *isoEnroll {
	t.Helper()
	withTerminal(t, true)
	home := t.TempDir()
	f := newIsoFake(home)
	p := UnitParams{Digest: oldDigest, Tier: config.TierIsolated, HTTPPort: 18081, MCPPort: 18091, JobTimeout: "3m", EnvFile: DefaultEnvFile}
	installedUnits(t, home, p)
	s := isolatedSettings()
	s.HTTPPort, s.MCPPort, s.JobTimeout, s.Image = 18081, 18091, "3m", oldImage
	s.GitHub = GitHub{AppID: 1234, InstallationID: 5678, Owner: "Acme", Repositories: []string{"Acme/api", "Acme/web"}, TestChecks: map[string]string{"Acme/api": "CI / test", "Acme/web": "CI / web"}}
	if err := SaveSettings(settingsFile(home), s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile(home), []byte("SUITEWARD_GITHUB_APP_ID=1234\nSUITEWARD_GITHUB_INSTALLATION_ID=5678\nSUITEWARD_GITHUB_REPOSITORIES=Acme/api,Acme/web\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &isoEnroll{t: t, f: f}
}

func (e *isoEnroll) enroll(status func(context.Context, string) ([]byte, error), stdin string, args ...string) (int, string) {
	e.t.Helper()
	term, _ := fakeTerminal(true, stdin)
	term.Out = &e.out
	env := Env{Deps: e.f.deps(), Term: term, PortFree: func(int) error { return nil }, Sleep: func(time.Duration) {}, HTTPGet: status}
	var stderr bytes.Buffer
	args = append([]string{"enroll", "--tier", "isolated", "--settings", settingsFile(e.f.home)}, args...)
	return Main(context.Background(), args, env, &stderr), stderr.String()
}

func TestEnrollIsolatedRunsSetupAsTheServiceAccount(t *testing.T) {
	e := newIsoEnroll(t)
	var asked []string
	status := func(_ context.Context, address string) ([]byte, error) {
		asked = append(asked, address)
		return []byte(statusEnrolled), nil
	}
	if code, stderr := e.enroll(status, "enroll\n", "--owner", "alice"); code != 0 {
		t.Fatalf("exit %d, stderr %q\n%s", code, stderr, e.out.String())
	}
	if len(asked) != 1 || asked[0] != "http://127.0.0.1:18081/status" {
		t.Errorf("/status was read from %v", asked)
	}
	want := "exec -it suiteward /suiteward setup --owner alice --test-check Acme/api=CI / test --test-check Acme/web=CI / web"
	if len(e.f.tty) != 1 || e.f.tty[0].line() != want {
		t.Fatalf("setup ran as %v, want %q", e.f.tty, want)
	}
}

func TestEnrollIsolatedAsksBeforeReplacingAnEnrolledOwner(t *testing.T) {
	e := newIsoEnroll(t)
	if code, stderr := e.enroll(statusIs(statusEnrolled), "no\n", "--owner", "alice"); code != 1 || !strings.Contains(stderr, "not confirmed") || len(e.f.tty) != 0 {
		t.Fatalf("exit %d, stderr %q, setup %v", code, stderr, e.f.tty)
	}
}

func TestEnrollIsolatedRefusesAServiceThatDoesNotAnswer(t *testing.T) {
	e := newIsoEnroll(t)
	down := func(context.Context, string) ([]byte, error) { return nil, errors.New("connection refused") }
	if code, stderr := e.enroll(down, "", "--owner", "alice"); code != 1 || !strings.Contains(stderr, "the service must be running") || len(e.f.tty) != 0 {
		t.Fatalf("exit %d, stderr %q, setup %v", code, stderr, e.f.tty)
	}
}

func TestEnrollIsolatedNeedsTheServiceAccountAndInstalledUnits(t *testing.T) {
	e := newIsoEnroll(t)
	e.f.euid = "0"
	if code, stderr := e.enroll(statusIs(statusNoOwner), "", "--owner", "alice"); code != 1 || !strings.Contains(stderr, "root") || len(e.f.tty) != 0 {
		t.Fatalf("as root: exit %d, stderr %q", code, stderr)
	}
	e = newIsoEnroll(t)
	if err := os.RemoveAll(unitDir(e.f.home)); err != nil {
		t.Fatal(err)
	}
	if code, stderr := e.enroll(statusIs(statusNoOwner), "", "--owner", "alice"); code != 1 || !strings.Contains(stderr, "nothing is installed") || len(e.f.tty) != 0 {
		t.Fatalf("no units: exit %d, stderr %q", code, stderr)
	}
}

func TestEnrollIsolatedTakesTheRepositoriesFromTheEnvFileWhenTheSettingsHaveNoApp(t *testing.T) {
	e := newIsoEnroll(t)
	s := isolatedSettings()
	s.HTTPPort, s.MCPPort, s.JobTimeout, s.Image = 18081, 18091, "3m", oldImage
	if err := SaveSettings(settingsFile(e.f.home), s); err != nil {
		t.Fatal(err)
	}
	if code, stderr := e.enroll(statusIs(statusNoOwner), "", "--owner", "alice", "--test-check", "Acme/api=CI / test", "--test-check", "Acme/web=CI / web"); code != 0 {
		t.Fatalf("exit %d, stderr %q\n%s", code, stderr, e.out.String())
	}
	if len(e.f.tty) != 1 {
		t.Fatalf("setup ran %d times", len(e.f.tty))
	}
}
