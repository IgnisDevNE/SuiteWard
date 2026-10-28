package installer

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// isoGitHub is one run of the isolated install with the GitHub flow and the /status of the service faked: the flow is
// the one the co-located tier runs, so these tests check how the isolated tier calls it and what it does with the result.
type isoGitHub struct {
	t       *testing.T
	f       *isoFake
	home    string
	answers map[string]string // over the defaults; an empty value removes the default
	tty     bool              // stdin is a terminal
	stdin   string
	flow    func(context.Context, githubEnv) (GitHubResult, error)
	ctx     context.Context // nil: context.Background()
	status  func(context.Context, string) ([]byte, error)

	seen   []githubEnv
	atFlow []string // the changes that were made when the flow was called
	envAt  bool     // the GitHub env file existed when the flow was called
	// pullsAtFlow is how many images had been pulled when the flow was called
	pullsAtFlow int
	asked       []string // the addresses read for /status
	slept       int
	out         bytes.Buffer
}

func newIsoGitHub(t *testing.T) *isoGitHub {
	t.Helper()
	return isoGitHubOn(t, newIsoFake(t.TempDir()))
}

// isoGitHubOn is a run on the machine f, whose home is the account home.
func isoGitHubOn(t *testing.T, f *isoFake) *isoGitHub {
	t.Helper()
	g := &isoGitHub{t: t, f: f, home: f.home, answers: map[string]string{}}
	g.flow = func(context.Context, githubEnv) (GitHubResult, error) {
		return GitHubResult{
			AppID: 123, InstallationID: 456, Account: "octo", AccountType: "User", OwnerLogin: "alice",
			Repositories: []string{"octo/one", "octo/two"}, TestChecks: map[string]string{"octo/one": "CI / test", "octo/two": "CI / test"},
			PrivateKey: []byte(appKeyPEM(t)),
		}, nil
	}
	g.status = func(context.Context, string) ([]byte, error) { return []byte(`{"version":"v0.2.0"}`), nil }
	return g
}

func (g *isoGitHub) run() error {
	g.t.Helper()
	answers := map[string]string{"image": testDigest, "github-app-id": "123", "github-installation-id": "456", "github-repositories": "octo/one,octo/two", "github-owner": "alice"}
	maps.Copy(answers, g.answers)
	for k, v := range answers {
		if v == "" {
			delete(answers, k)
		}
	}
	aliasGitHubKeys(answers) // Install does it before the resolver is made
	term, _ := fakeTerminal(g.tty, g.stdin)
	term.Out = &g.out
	r := newTestResolver(g.t, nil, answers, term)
	// What Install does: the saved settings of an earlier run, as the isolated tier.
	s, _, err := loadSaved("suiteward", settingsFile(g.home), false)
	if err != nil {
		g.t.Fatal(err)
	}
	s.Tier, s.Runtime = config.TierIsolated, RuntimePodman
	env := Env{
		Deps: g.f.deps(), Term: term, Sleep: func(time.Duration) { g.slept++ },
		ResolveGitHub: func(ctx context.Context, e githubEnv) (GitHubResult, error) {
			g.seen = append(g.seen, e)
			g.atFlow, g.pullsAtFlow = nil, 0
			for _, line := range g.f.lines() {
				if strings.Contains(line, "podman pull") {
					g.pullsAtFlow++
				}
			}
			for _, line := range g.f.lines() {
				if strings.Contains(line, "secret create") || strings.Contains(line, "start") || strings.Contains(line, "daemon-reload") {
					g.atFlow = append(g.atFlow, line)
				}
			}
			_, statErr := os.Stat(envFile(g.home))
			g.envAt = statErr == nil
			return g.flow(ctx, e)
		},
		HTTPGet: func(ctx context.Context, address string) ([]byte, error) {
			g.asked = append(g.asked, address)
			return g.status(ctx, address)
		},
	}
	ctx := g.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return installIsolated(ctx, s, r, env)
}

// terminalForSetup makes setup think it has a real terminal for the length of the test.
func terminalForSetup(t *testing.T) {
	t.Helper()
	was := setupTerminal
	setupTerminal = func() bool { return true }
	t.Cleanup(func() { setupTerminal = was })
}

func TestInstallIsolatedRunsTheGitHubFlowBeforeAnyChange(t *testing.T) {
	g := newIsoGitHub(t)
	g.f.noImage = true // the images are pulled
	g.answers["manifest-port"] = "18765"
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	if len(g.seen) != 1 {
		t.Fatalf("the GitHub flow ran %d times, want once", len(g.seen))
	}
	configDir := filepath.Join(g.home, ".config", "suiteward")
	if e := g.seen[0]; e.Tier != config.TierIsolated || e.SettingsDir != configDir || e.ManifestPort != 18765 {
		t.Errorf("the flow got tier %q, settings directory %q and manifest port %d", e.Tier, e.SettingsDir, e.ManifestPort)
	}
	if len(g.atFlow) != 0 || g.envAt {
		t.Errorf("something was changed before the flow ran: %v (env file %v)", g.atFlow, g.envAt)
	}
	if g.pullsAtFlow != 2 {
		t.Errorf("both images must be pulled before the flow runs, so a wrong digest fails before an App exists: %d pulled", g.pullsAtFlow)
	}
	if g.f.secrets[SecretGitHubAppKey] != appKeyStored(t) {
		t.Errorf("the App key did not reach the secret store")
	}
	env := "SUITEWARD_GITHUB_APP_ID=123\nSUITEWARD_GITHUB_INSTALLATION_ID=456\nSUITEWARD_GITHUB_REPOSITORIES=octo/one,octo/two\n"
	if got := string(readFileOrFail(t, envFile(g.home))); got != env {
		t.Errorf("the env file holds %q", got)
	}
	saved, err := LoadSettings(settingsFile(g.home))
	if err != nil || saved.GitHub.AppID != 123 || saved.GitHub.Owner != "octo" || saved.GitHub.TestChecks["octo/one"] != "CI / test" {
		t.Errorf("the settings keep %+v, %v", saved.GitHub, err)
	}
	if !strings.Contains(g.out.String(), "SHA256:") {
		t.Errorf("the key fingerprint note is gone:\n%s", g.out.String())
	}
	(&fakeEngine{calls: g.f.calls}).assertNoSecret(t, appKeyPEM(t), strings.TrimSpace(appKeyPEM(t)))
	g.f.secretValuesNotInArgs(t)
}

func TestInstallIsolatedStoresAKeyTheFlowCreatedRightAway(t *testing.T) {
	g := newIsoGitHub(t)
	inner := g.flow
	g.flow = func(ctx context.Context, e githubEnv) (GitHubResult, error) {
		if err := e.StoreKey(ctx, []byte(appKeyPEM(t))); err != nil {
			return GitHubResult{}, err
		}
		return inner(ctx, e)
	}
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	writes := 0
	for _, line := range g.f.lines() {
		if strings.Contains(line, "secret create "+SecretGitHubAppKey) {
			writes++
		}
	}
	if writes != 1 || g.f.secrets[SecretGitHubAppKey] != appKeyStored(t) {
		t.Errorf("the key was written %d times, want once, as the PEM: %q", writes, g.f.secrets[SecretGitHubAppKey])
	}
	if key, password := g.f.index("secret create "+SecretGitHubAppKey, 0), g.f.index("secret create "+SecretDBAuth, 0); key < 0 || password < key {
		t.Errorf("the key must be stored right after the App is created, before the other secrets:\n%s", strings.Join(g.f.lines(), "\n"))
	}
	(&fakeEngine{calls: g.f.calls}).assertNoSecret(t, appKeyPEM(t))
}

// An App whose key secret is missing is still refused before any change: the unit of an App mounts that secret.
func TestInstallIsolatedWithAnAppButNoKeyStopsBeforeAnyChange(t *testing.T) {
	g := newIsoGitHub(t)
	inner := g.flow
	g.flow = func(ctx context.Context, e githubEnv) (GitHubResult, error) {
		res, err := inner(ctx, e)
		res.PrivateKey = nil // an App that exists, whose key was neither given nor stored
		return res, err
	}
	err := g.run()
	if err == nil || !strings.Contains(err.Error(), SecretGitHubAppKey) || !strings.Contains(err.Error(), "--github-app-id none") {
		t.Fatalf("the unit mounts the App key secret, so an App without that secret must say so, and name the way out: %v", err)
	}
	if g.f.has("secret create") || g.f.has("start") || g.f.has("daemon-reload") || exists(unitDir(g.home)) || exists(envFile(g.home)) {
		t.Fatalf("nothing may change: %v", g.f.lines())
	}
}

func TestInstallIsolatedRerunDoesNotRunTheGitHubFlowAgain(t *testing.T) {
	g := newIsoGitHub(t)
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	env := readFileOrFail(t, envFile(g.home))
	before := len(g.f.calls)
	for _, key := range []string{"github-app-id", "github-installation-id", "github-repositories", "github-owner"} {
		g.answers[key] = ""
	}
	g.out.Reset()
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	if len(g.seen) != 1 {
		t.Errorf("a re-run with the settings complete asked GitHub again (%d runs)", len(g.seen))
	}
	again := &isoFake{calls: g.f.calls[before:]}
	if again.has("secret create") {
		t.Errorf("a re-run created a secret again: %v", again.lines())
	}
	if !bytes.Equal(env, readFileOrFail(t, envFile(g.home))) {
		t.Errorf("the env file changed")
	}
	if len(g.f.tty) != 0 {
		t.Errorf("setup ran on a re-run that did not go through the GitHub flow")
	}
}

func TestInstallIsolatedDefaultsToTheReleaseTheBinaryCarries(t *testing.T) {
	was := ImageDigest
	t.Cleanup(func() { ImageDigest = was })

	ImageDigest = testDigest
	g := newIsoGitHub(t)
	g.f.noImage = true
	g.answers["image"] = ""
	if err := g.run(); err != nil {
		t.Fatalf("a release binary needs no --image: %v", err)
	}
	if !g.f.has("podman pull " + releaseImageRepo + "@" + testDigest) {
		t.Errorf("the embedded release is not the one pulled: %v", g.f.lines())
	}

	ImageDigest = ""
	g = newIsoGitHub(t)
	g.answers["image"] = ""
	if err := g.run(); err == nil || !strings.Contains(err.Error(), "--image") || g.f.has("secret create") {
		t.Fatalf("a development build must still ask for --image before any change: %v", err)
	}
}

func TestInstallIsolatedRunsSetupOnATerminalAndNeverCapturesIt(t *testing.T) {
	terminalForSetup(t)
	g := newIsoGitHub(t)
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	if len(g.f.tty) != 1 {
		t.Fatalf("setup ran %d times, want once; calls:\n%s", len(g.f.tty), strings.Join(g.f.lines(), "\n"))
	}
	want := "exec -it suiteward /suiteward setup --owner alice --test-check octo/one=CI / test --test-check octo/two=CI / test"
	if c := g.f.tty[0]; c.name != "podman" || c.line() != want {
		t.Errorf("setup ran as %s %q, want podman %q", c.name, c.line(), want)
	}
	if !strings.Contains(g.out.String(), "session recording") && !strings.Contains(g.out.String(), "recording") {
		t.Errorf("the warning about terminal recording is missing:\n%s", g.out.String())
	}
	// The stability wait before it reads /readyz as well; the guard reads /status only once.
	if status := slices.DeleteFunc(slices.Clone(g.asked), func(a string) bool { return strings.HasSuffix(a, "/readyz") }); len(status) != 1 || !strings.HasSuffix(status[0], ":27081/status") {
		t.Errorf("the guard must read /status of the service on its HTTP port first: %v", g.asked)
	}
	(&fakeEngine{calls: g.f.calls, tty: g.f.tty}).assertNoSecret(t, appKeyPEM(t))
}

func TestInstallIsolatedPrintsTheExactSetupCommandWithoutATerminal(t *testing.T) {
	g := newIsoGitHub(t)
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	if len(g.f.tty) != 0 {
		t.Fatalf("setup must not run without a terminal: %v", g.f.tty)
	}
	want := "suiteward enroll --tier isolated"
	if !strings.Contains(g.out.String(), want) {
		t.Errorf("the output lacks the exact command %q:\n%s", want, g.out.String())
	}
	if strings.Contains(g.out.String(), "<login>") {
		t.Errorf("the command still holds a placeholder:\n%s", g.out.String())
	}
}

func TestInstallIsolatedSkipsSetupWhenAnOwnerIsEnrolled(t *testing.T) {
	for _, tty := range []bool{true, false} {
		if tty {
			terminalForSetup(t)
		}
		g := newIsoGitHub(t)
		g.status = func(context.Context, string) ([]byte, error) {
			return []byte(`{"version":"v0.2.0","owner":{"login":"bob","accountId":7,"enrolledAt":"2026-10-01T00:00:00Z"}}`), nil
		}
		if err := g.run(); err != nil {
			t.Fatal(err)
		}
		if len(g.f.tty) != 0 {
			t.Fatalf("setup ran although an owner is enrolled: %v", g.f.tty)
		}
		out := g.out.String()
		for _, want := range []string{"An owner (bob) is already enrolled", "owner setup was skipped", "authenticator secret is unchanged", "To enroll again", "suiteward enroll --tier isolated"} {
			if !strings.Contains(out, want) {
				t.Errorf("tty=%v: the output lacks %q:\n%s", tty, want, out)
			}
		}
	}
}

func TestInstallIsolatedSkipsSetupWhenStatusCannotBeRead(t *testing.T) {
	terminalForSetup(t)
	g := newIsoGitHub(t)
	g.status = func(_ context.Context, address string) ([]byte, error) {
		if strings.HasSuffix(address, "/readyz") { // the service is up and stable; only /status cannot be read
			return []byte("ok"), nil
		}
		return nil, errors.New("dial tcp: connection refused")
	}
	if err := g.run(); err != nil {
		t.Fatalf("the stack is up; only setup is skipped: %v", err)
	}
	if len(g.f.tty) != 0 {
		t.Fatalf("setup ran although /status could not be read: %v", g.f.tty)
	}
	out := g.out.String()
	for _, want := range []string{"connection refused", "suiteward enroll --tier isolated"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if g.slept == 0 || g.slept > 120 {
		t.Errorf("a service that has just started gets a bounded wait, slept %d times", g.slept)
	}
}

func TestInstallIsolatedWaitsForStatusAfterTheStart(t *testing.T) {
	terminalForSetup(t)
	g := newIsoGitHub(t)
	reads := 0
	g.status = func(context.Context, string) ([]byte, error) {
		reads++
		if reads < 3 {
			return nil, errors.New("connection refused")
		}
		return []byte(`{"version":"v0.2.0"}`), nil
	}
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	if len(g.f.tty) != 1 {
		t.Fatalf("a service that answers a moment later must still get its setup: %d runs, output:\n%s", len(g.f.tty), g.out.String())
	}
}

func TestGitHubAnswersAcceptTheIsolatedKeySpellings(t *testing.T) {
	m := map[string]string{"github-app-key": "pem", "github-app-key-file": "-"}
	aliasGitHubKeys(m)
	if m["app-key"] != "pem" || m["app-key-file"] != "-" {
		t.Errorf("the isolated answers github-app-key and github-app-key-file must reach the flow's keys: %v", m)
	}
}

func TestResolveGitHubReadsTheAppKeyFromStdinWhenTheFileIsADash(t *testing.T) {
	g := newFakeGitHub(t)
	flags := existingFlags(t, map[string]string{"app-key-file": "-"})
	env, out := flowEnv(t, g, flags, bytes.NewReader(testKeyPEM()), false)
	res, err := resolve(t, env)
	if err != nil {
		t.Fatalf("a key file of - reads standard input, as the flag's help says: %v", err)
	}
	if string(res.PrivateKey) != string(testKeyPEM()) {
		t.Errorf("the key from stdin is not the one the result carries")
	}
	assertNoKey(t, out.String())
}

// stepNumber finds a citation of a numbered step or section of the install guide, which the guide no longer has.
var stepNumber = regexp.MustCompile(`install\.md[^\n]{0,24}?\b\d+\.\d+\b|\b(?:step|steps|section) 1[01]\.\d+\b`)

func TestNoCitationOfAStepNumberOfTheInstallGuide(t *testing.T) {
	var files []string
	for _, pattern := range []string{"*.go", "../../deploy/quadlet/*"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	slices.Sort(files)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		for i, line := range strings.Split(string(readFileOrFail(t, file)), "\n") {
			if m := stepNumber.FindString(line); m != "" {
				t.Errorf("%s:%d cites %q: the guide has no such step, name the section or none", file, i+1, m)
			}
		}
	}
}

// withoutGitHub are the answers of a run that does not go through the GitHub flow, on a machine that has the App key secret
// already (a host installed by hand, or a re-run).
func withoutGitHub(f *isoFake) map[string]string {
	f.secrets[SecretGitHubAppKey] = "existing"
	return map[string]string{"github-app-id": "", "github-installation-id": "", "github-repositories": "", "github-owner": "", "github": "no"}
}

func TestInstallIsolatedRefusesAKeyThatIsNotAnRSAKeyBeforeAnyChange(t *testing.T) {
	certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")}))
	for name, bad := range map[string]string{"text": "not a key", "certificate": certificate} {
		g := newIsoGitHub(t)
		g.flow = func(context.Context, githubEnv) (GitHubResult, error) {
			return GitHubResult{
				AppID: 123, InstallationID: 456, Account: "octo", OwnerLogin: "alice",
				Repositories: []string{"octo/one"}, TestChecks: map[string]string{"octo/one": "CI"}, PrivateKey: []byte(bad),
			}, nil
		}
		if err := g.run(); err == nil || g.f.has("secret create") || g.f.has("start") {
			t.Fatalf("%s: a key that is not a private key must stop the run before any change: %v, %v", name, err, g.f.lines())
		}
	}
}

func TestResolveGitHubAsksForTheKeyFileAtATerminalBecauseAPastedPEMNeedsMoreThanOneLine(t *testing.T) {
	g := newFakeGitHub(t)
	flags := existingFlags(t, nil)
	path := flags["app-key-file"]
	delete(flags, "app-key-file")
	env, out := flowEnv(t, g, flags, strings.NewReader(path+"\n"), true) // its password reader fails, so the key cannot come from it
	res, err := resolve(t, env)
	if err != nil {
		t.Fatalf("a terminal run names the key file at a prompt: %v\n%s", err, out.String())
	}
	if string(res.PrivateKey) != string(testKeyPEM()) {
		t.Errorf("the key of the file is not the one the result carries")
	}
	if !strings.Contains(out.String(), "private key file") {
		t.Errorf("the prompt for the path is missing:\n%s", out.String())
	}
	assertNoKey(t, out.String())
}
