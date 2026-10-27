package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An App the flow created is recorded right after its key is stored, so a run that stops before the flow ends (no installation
// within the wait, Ctrl-C, a later prompt that fails) does not leave an orphan that blocks the next run: the next run takes up
// that App, with the key secret that is stored already, and creates no second one.

// realFlow is the real GitHub flow against g, with the waits shortened.
func realFlow(g *fakeGitHub, installWait time.Duration) func(context.Context, githubEnv) (GitHubResult, error) {
	return func(ctx context.Context, e githubEnv) (GitHubResult, error) {
		e.HTTP, e.APIURL, e.WebURL = g.srv.Client(), g.srv.URL, "https://github.example"
		e.SkipRulesetWait, e.InstallWait, e.PollEvery = true, installWait, 2*time.Millisecond
		return resolveGitHub(ctx, e)
	}
}

func (g *fakeGitHub) count(prefix string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, r := range g.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

// neverInstalled makes the App not installable (true), or installed at the first look (false).
func (g *fakeGitHub) neverInstalled(never bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.installAfter = 0
	if never {
		g.installAfter = 1 << 30
	}
}

// ---- the co-located tier ----

// colocatedFlow is a co-located install that creates its App through the real flow, with answers from a file and flags.
func colocatedFlow(t *testing.T) (*harness, *fakeGitHub, []string) {
	t.Helper()
	h := newHarness(t, "linux")
	g := newFakeGitHub(t)
	h.github = realFlow(g, 100*time.Millisecond)
	answers := filepath.Join(t.TempDir(), "answers.yaml")
	if err := os.WriteFile(answers, []byte("manifest-code: thecode1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--answers", answers, "--github", "yes", "--github-account", "acme", "--github-repositories", "acme/app", "--github-owner", "alice", "--test-check", "CI / test"}
	return h, g, args
}

// theKeySecretIsThere makes the key secret exist in the fake engine, as the first run left it.
func (h *harness) theKeySecretIsThere() {
	h.have(kindSecret, SecretGitHubAppKey)
	h.first("secret inspect --showsecret --format {{.SecretData}} "+SecretGitHubAppKey, string(testKeyPEM()))
}

// onlyTheKeySecretWasMade fails when the run changed anything but the App key secret.
func (h *harness) onlyTheKeySecretWasMade() {
	h.t.Helper()
	for _, m := range h.mutations() {
		if !strings.HasPrefix(m, "secret create "+SecretGitHubAppKey+" ") && !strings.HasPrefix(m, "secret rm") {
			h.t.Errorf("a change other than the key secret was made: %s", m)
		}
	}
}

func TestInstallColocatedResumesTheAppAFailedFlowLeft(t *testing.T) {
	h, g, args := colocatedFlow(t)
	g.neverInstalled(true)
	err := h.podman(args...)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("the first run = %v, want the installation wait to time out", err)
	}
	for _, want := range []string{"settings/apps/suiteward-abcd", SecretGitHubAppKey, "secret rm " + SecretGitHubAppKey, pendingAppPath(h.settingsPath()), "again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q:\n%v", want, err)
		}
	}
	h.onlyTheKeySecretWasMade()
	record := string(readFileOrFail(t, pendingAppPath(h.settingsPath())))
	if !strings.Contains(record, "4242") || !strings.Contains(record, "suiteward-abcd") || strings.Contains(record, "PRIVATE KEY") || strings.Contains(record, pemBody()) {
		t.Fatalf("the record of the App: %q", record)
	}

	g.neverInstalled(false)
	h.theKeySecretIsThere()
	if err := h.podman(args...); err != nil {
		t.Fatalf("the re-run must take up the App: %v", err)
	}
	if n := g.count("POST /app-manifests"); n != 1 {
		t.Errorf("%d Apps were created, want one", n)
	}
	if n := len(h.secretWrites(SecretGitHubAppKey)); n != 1 {
		t.Errorf("the key was stored %d times, want once", n)
	}
	if s := h.settings(); s.GitHub.AppID != fakeAppID || s.GitHub.InstallationID != fakeInstallationID {
		t.Errorf("the settings keep %+v", s.GitHub)
	}
	if exists(pendingAppPath(h.settingsPath())) {
		t.Error("the record is still there after the settings were saved")
	}
}

func TestInstallColocatedCtrlCDuringTheInstallationWaitLeavesAResumableRun(t *testing.T) {
	h, g, args := colocatedFlow(t)
	g.neverInstalled(true)
	h.github = realFlow(g, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)
	err := Install(ctx, append([]string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--image", testImage}, args...), h.newEnv())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Ctrl-C = %v, want the cancellation", err)
	}
	if !exists(pendingAppPath(h.settingsPath())) {
		t.Fatal("the App was not recorded")
	}

	g.neverInstalled(false)
	h.github = realFlow(g, 100*time.Millisecond)
	h.theKeySecretIsThere()
	if err := h.podman(args...); err != nil {
		t.Fatalf("the re-run must take up the App: %v", err)
	}
	if n := g.count("POST /app-manifests"); n != 1 {
		t.Errorf("%d Apps were created, want one", n)
	}
}

func TestInstallColocatedARecordedAppWithoutItsKeySecretIsANamedError(t *testing.T) {
	h, g, args := colocatedFlow(t)
	g.neverInstalled(true)
	if err := h.podman(args...); err == nil {
		t.Fatal("the first run must fail")
	}
	// The fake engine has no key secret now: purged, or removed by hand. The record names an App nobody can use.
	before := len(h.f.calls)
	err := h.podman(args...)
	if err == nil {
		t.Fatal("an App whose key secret is missing must stop the run")
	}
	for _, want := range []string{"settings/apps/suiteward-abcd", pendingAppPath(h.settingsPath()), SecretGitHubAppKey, "--github-app-key-file", "--github new-app"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q:\n%v", want, err)
		}
	}
	if g.count("POST /app-manifests") != 1 {
		t.Error("a second App was created")
	}
	for _, c := range h.f.calls[before:] {
		if strings.Contains(c.line(), "secret create") {
			t.Errorf("the refused run changed something: %s", c.line())
		}
	}
}

// A key secret with no record (an older run, or a record that could not be written) blocks the key of a new App: the error
// names the secret and how to remove it, not only the App, and no record of the new App is left to mislead the next run.
func TestInstallColocatedASecretWithoutARecordNamesTheSecretAndHowToRemoveIt(t *testing.T) {
	h, _, args := colocatedFlow(t)
	h.theKeySecretIsThere()
	err := h.podman(args...)
	if !errors.Is(err, ErrSecretExists) {
		t.Fatalf("error = %v, want ErrSecretExists", err)
	}
	for _, want := range []string{SecretGitHubAppKey, "podman --connection pm secret rm " + SecretGitHubAppKey, "settings/apps/suiteward-abcd"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q:\n%v", want, err)
		}
	}
	if exists(pendingAppPath(h.settingsPath())) {
		t.Error("a record of an App whose key was not stored would resume with the wrong key")
	}
}

func TestWantsGitHubWhenAnAppIsRecorded(t *testing.T) {
	r := newTestResolver(t, nil, nil, Terminal{In: strings.NewReader(""), Out: &strings.Builder{}})
	g := &githubInstall{s: DefaultSettings(), pending: &PendingApp{AppID: 1, Slug: "suiteward-abcd", Account: "acme", AccountType: "Organization"}}
	if run, err := g.wantsGitHub(context.Background(), r); err != nil || !run {
		t.Fatalf("wantsGitHub = %v, %v; an App that an earlier run did not finish with must be taken up even without a flag", run, err)
	}
	g.pending = nil
	if run, err := g.wantsGitHub(context.Background(), r); err != nil || run {
		t.Fatalf("wantsGitHub = %v, %v; without a record or a flag, a run that cannot ask skips GitHub", run, err)
	}
}

// ---- the isolated tier ----

func isolatedFlow(t *testing.T) (*isoGitHub, *fakeGitHub) {
	t.Helper()
	g := newIsoGitHub(t)
	fake := newFakeGitHub(t)
	g.flow = realFlow(fake, 100*time.Millisecond)
	for _, key := range []string{"github-app-id", "github-installation-id"} {
		g.answers[key] = ""
	}
	g.answers["github"] = "yes"
	g.answers["github-account"] = "acme"
	g.answers["github-repositories"] = "acme/app"
	g.answers["github-owner"] = "alice"
	g.answers["test-check"] = "CI / test"
	g.answers["manifest-code"] = "thecode1"
	return g, fake
}

func TestInstallIsolatedResumesTheAppAFailedFlowLeft(t *testing.T) {
	g, fake := isolatedFlow(t)
	fake.neverInstalled(true)
	err := g.run()
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("the first run = %v, want the installation wait to time out", err)
	}
	for _, want := range []string{"settings/apps/suiteward-abcd", SecretGitHubAppKey, "secret rm " + SecretGitHubAppKey, pendingAppPath(settingsFile(g.home)), "again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q:\n%v", want, err)
		}
	}
	if g.f.has("secret create "+SecretDBAuth) || g.f.has("start") || exists(envFile(g.home)) {
		t.Fatalf("only the key secret may have been made: %v", g.f.lines())
	}
	if record := string(readFileOrFail(t, pendingAppPath(settingsFile(g.home)))); !strings.Contains(record, "4242") || strings.Contains(record, pemBody()) {
		t.Fatalf("the record of the App: %q", record)
	}

	fake.neverInstalled(false)
	if err := g.run(); err != nil {
		t.Fatalf("the re-run must take up the App: %v", err)
	}
	if n := fake.count("POST /app-manifests"); n != 1 {
		t.Errorf("%d Apps were created, want one", n)
	}
	writes := 0
	for _, line := range g.f.lines() {
		if strings.Contains(line, "secret create "+SecretGitHubAppKey) {
			writes++
		}
	}
	if writes != 1 {
		t.Errorf("the key was stored %d times, want once", writes)
	}
	if saved, err := LoadSettings(settingsFile(g.home)); err != nil || saved.GitHub.AppID != fakeAppID {
		t.Errorf("the settings keep %+v, %v", saved.GitHub, err)
	}
	if exists(pendingAppPath(settingsFile(g.home))) {
		t.Error("the record is still there after the settings were saved")
	}
}

func TestInstallIsolatedCtrlCDuringTheInstallationWaitLeavesAResumableRun(t *testing.T) {
	g, fake := isolatedFlow(t)
	fake.neverInstalled(true)
	g.flow = realFlow(fake, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	g.ctx = ctx
	time.AfterFunc(150*time.Millisecond, cancel)
	if err := g.run(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ctrl-C = %v, want the cancellation", err)
	}
	if !exists(pendingAppPath(settingsFile(g.home))) {
		t.Fatal("the App was not recorded")
	}

	fake.neverInstalled(false)
	g.ctx, g.flow = nil, realFlow(fake, 100*time.Millisecond)
	if err := g.run(); err != nil {
		t.Fatalf("the re-run must take up the App: %v", err)
	}
	if n := fake.count("POST /app-manifests"); n != 1 {
		t.Errorf("%d Apps were created, want one", n)
	}
}

func TestInstallIsolatedARecordedAppWithoutItsKeySecretIsANamedError(t *testing.T) {
	g, fake := isolatedFlow(t)
	fake.neverInstalled(true)
	if err := g.run(); err == nil {
		t.Fatal("the first run must fail")
	}
	delete(g.f.secrets, SecretGitHubAppKey) // purged, or removed by hand
	err := g.run()
	if err == nil {
		t.Fatal("an App whose key secret is missing must stop the run")
	}
	for _, want := range []string{"settings/apps/suiteward-abcd", pendingAppPath(settingsFile(g.home)), SecretGitHubAppKey, "--github-app-key-file", "--github new-app"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q:\n%v", want, err)
		}
	}
	if fake.count("POST /app-manifests") != 1 {
		t.Error("a second App was created")
	}
}

func TestInstallIsolatedASecretWithoutARecordNamesTheSecretAndHowToRemoveIt(t *testing.T) {
	g, _ := isolatedFlow(t)
	g.f.secrets[SecretGitHubAppKey] = "left behind"
	err := g.run()
	if !errors.Is(err, ErrSecretExists) {
		t.Fatalf("error = %v, want ErrSecretExists", err)
	}
	for _, want := range []string{SecretGitHubAppKey, "podman secret rm " + SecretGitHubAppKey, "settings/apps/suiteward-abcd"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q:\n%v", want, err)
		}
	}
	if exists(pendingAppPath(settingsFile(g.home))) {
		t.Error("a record of an App whose key was not stored would resume with the wrong key")
	}
}

// ---- purge ----

func TestPurgeRemovesTheRecordOfAnApp(t *testing.T) {
	h := newHarness(t, "linux")
	if err := SaveSettings(h.settingsPath(), colocatedSettings()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pendingAppPath(h.settingsPath()), []byte("app_id: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := purgeState(context.Background(), newRootlessPodman(t, h.f, ""), []string{h.tokenPath()}, h.settingsPath()); err != nil {
		t.Fatal(err)
	}
	if exists(pendingAppPath(h.settingsPath())) {
		t.Error("the record of an App outlived the purge")
	}
}

// The record is for an App that no settings or env file name yet: once the env file names it, a later step that fails does not
// make the re-run ask GitHub again, as it did not before the record existed.
func TestInstallIsolatedARecordDoesNotForceTheFlowOnceTheEnvFileNamesTheApp(t *testing.T) {
	g, fake := isolatedFlow(t)
	fake.neverInstalled(false)
	g.f.failOn["systemctl --user start suiteward-db.service"] = errors.New("the database cannot start")
	if err := g.run(); err == nil {
		t.Fatal("the database start must fail")
	}
	if !exists(envFile(g.home)) || !exists(pendingAppPath(settingsFile(g.home))) {
		t.Fatalf("the test needs the env file and the record: %v %v", exists(envFile(g.home)), exists(pendingAppPath(settingsFile(g.home))))
	}
	delete(g.f.failOn, "systemctl --user start suiteward-db.service")
	for _, key := range []string{"github", "github-account", "github-repositories", "github-owner", "test-check", "manifest-code"} {
		g.answers[key] = ""
	}
	if err := g.run(); err != nil {
		t.Fatalf("a re-run with the App in the env file and no GitHub answers must work: %v", err)
	}
	if len(g.seen) != 1 {
		t.Errorf("the GitHub flow ran %d times, want once (the first run)", len(g.seen))
	}
	if exists(pendingAppPath(settingsFile(g.home))) {
		t.Error("the record is still there after the settings were saved")
	}
}

// ---- the other way out of "recorded, key missing": the key of that App, given again ----

func TestInstallColocatedTheKeyGivenAgainFinishesARecordedApp(t *testing.T) {
	h, g, args := colocatedFlow(t)
	g.neverInstalled(true)
	if err := h.podman(args...); err == nil {
		t.Fatal("the first run must fail")
	}
	g.neverInstalled(false) // the App is installed now; the fake engine has no key secret, as after a purge
	keyFile := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(keyFile, testKeyPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	extra := []string{"--github-app-id", "4242", "--github-installation-id", "77", "--github-app-key-file", keyFile}
	if err := h.podman(append(args, extra...)...); err != nil {
		t.Fatalf("the advertised way out does not work: %v", err)
	}
	if n := g.count("POST /app-manifests"); n != 1 {
		t.Errorf("%d Apps were created, want one", n)
	}
	if n := len(h.secretWrites(SecretGitHubAppKey)); n != 2 { // the first run, and the key given again
		t.Errorf("the key was stored %d times, want 2", n)
	}
	if exists(pendingAppPath(h.settingsPath())) {
		t.Error("the record is still there after the settings were saved")
	}
}

func TestInstallIsolatedTheKeyGivenAgainFinishesARecordedApp(t *testing.T) {
	g, fake := isolatedFlow(t)
	fake.neverInstalled(true)
	if err := g.run(); err == nil {
		t.Fatal("the first run must fail")
	}
	fake.neverInstalled(false)
	delete(g.f.secrets, SecretGitHubAppKey) // purged
	keyFile := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(keyFile, testKeyPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	g.answers["github-app-id"], g.answers["github-installation-id"], g.answers["github-app-key-file"] = "4242", "77", keyFile
	if err := g.run(); err != nil {
		t.Fatalf("the advertised way out does not work: %v", err)
	}
	if n := fake.count("POST /app-manifests"); n != 1 {
		t.Errorf("%d Apps were created, want one", n)
	}
	if g.f.secrets[SecretGitHubAppKey] == "" {
		t.Error("the key given again was not stored")
	}
	if exists(pendingAppPath(settingsFile(g.home))) {
		t.Error("the record is still there after the settings were saved")
	}
}

// ---- an App that is gone, and a run that leaves it ----

func (g *fakeGitHub) answersAppWith(status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.appStatus = status
}

// The App was deleted on GitHub, or its key is refused: the resume fails on GET /app. The error says what is left (the record,
// the key secret and how to remove it) and what to do when the App is gone.
func TestInstallColocatedAResumeThatFailsNamesWhatIsLeft(t *testing.T) {
	for _, status := range []int{404, 401} {
		h, g, args := colocatedFlow(t)
		g.neverInstalled(true)
		if err := h.podman(args...); err == nil {
			t.Fatal("the first run must fail")
		}
		g.answersAppWith(status)
		h.theKeySecretIsThere()
		err := h.podman(args...)
		if err == nil {
			t.Fatalf("HTTP %d: the resume must fail", status)
		}
		for _, want := range []string{"GET /app", pendingAppPath(h.settingsPath()), SecretGitHubAppKey, "podman --connection pm secret rm " + SecretGitHubAppKey, "create a new App"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("HTTP %d: the error does not name %q:\n%v", status, want, err)
			}
		}
		if n := g.count("POST /app-manifests"); n != 1 {
			t.Errorf("HTTP %d: %d Apps were created, want one", status, n)
		}
	}
}

func TestInstallIsolatedAResumeThatFailsNamesWhatIsLeft(t *testing.T) {
	for _, status := range []int{404, 401} {
		g, fake := isolatedFlow(t)
		fake.neverInstalled(true)
		if err := g.run(); err == nil {
			t.Fatal("the first run must fail")
		}
		fake.answersAppWith(status)
		err := g.run()
		if err == nil {
			t.Fatalf("HTTP %d: the resume must fail", status)
		}
		for _, want := range []string{"GET /app", pendingAppPath(settingsFile(g.home)), SecretGitHubAppKey, "podman secret rm " + SecretGitHubAppKey, "create a new App"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("HTTP %d: the error does not name %q:\n%v", status, want, err)
			}
		}
	}
}

// GitHub turned off must not silently drop an App that still exists with its key: the run says so, naming each leftover.
func TestInstallColocatedWithoutGitHubSaysWhichAppAndKeyAreLeft(t *testing.T) {
	h, g, args := colocatedFlow(t)
	g.neverInstalled(true)
	if err := h.podman(args...); err == nil {
		t.Fatal("the first run must fail")
	}
	h.theKeySecretIsThere()
	h.out.Reset()
	if err := h.podman("--github", "no"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"suiteward-abcd", pendingAppPath(h.settingsPath()), SecretGitHubAppKey, "secret rm " + SecretGitHubAppKey, "--github yes"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("the output does not name %q:\n%s", want, h.out.String())
		}
	}
	if !exists(pendingAppPath(h.settingsPath())) {
		t.Error("the record was dropped")
	}
}

func TestInstallIsolatedWithoutGitHubSaysWhichAppAndKeyAreLeft(t *testing.T) {
	g, fake := isolatedFlow(t)
	fake.neverInstalled(true)
	if err := g.run(); err == nil {
		t.Fatal("the first run must fail")
	}
	for _, key := range []string{"github", "github-account", "github-repositories", "github-owner", "test-check", "manifest-code"} {
		g.answers[key] = ""
	}
	g.answers["github-app-id"] = "none"
	g.out.Reset()
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"suiteward-abcd", pendingAppPath(settingsFile(g.home)), SecretGitHubAppKey, "secret rm " + SecretGitHubAppKey, "--github yes"} {
		if !strings.Contains(g.out.String(), want) {
			t.Errorf("the output does not name %q:\n%s", want, g.out.String())
		}
	}
	if !exists(pendingAppPath(settingsFile(g.home))) {
		t.Error("the record was dropped")
	}
}
