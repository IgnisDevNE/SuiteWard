package installer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvGitHubFlowDefaultsToTheRealFlow(t *testing.T) {
	flow := Env{}.githubFlow()
	if _, err := flow(context.Background(), githubEnv{}); err == nil || !strings.Contains(err.Error(), "needs a resolver") {
		t.Fatalf("the default flow = %v, want the real flow's refusal of an empty environment", err)
	}
}

func TestInstallStopsBeforeChangingAnythingWhenTheGitHubAnswersAreWrong(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness)
		args  []string
		want  string
	}{
		{"github is neither yes nor no", nil, []string{"--github", "maybe"}, "github"},
		{"a manifest port that is not a number", nil, []string{"--github", "yes", "--manifest-port", "http"}, `manifest-port "http" is not a number`},
		{"a record of an App that cannot be read", func(h *harness) {
			if err := os.WriteFile(h.settingsPath()+".pending-app", []byte("app_id: [\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, nil, "delete it if its App is gone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "linux")
			h.github = func(context.Context, githubEnv) (GitHubResult, error) {
				t.Error("the GitHub flow ran")
				return GitHubResult{}, nil
			}
			if tc.setup != nil {
				tc.setup(h)
			}
			err := h.podman(tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("install = %v, want an error containing %q", err, tc.want)
			}
			h.mustNotMutate()
		})
	}
}

// What the GitHub flow returns is checked before the stack is changed: an incomplete or invalid result stops the install.
func TestInstallRefusesAGitHubResultItCannotUse(t *testing.T) {
	cases := []struct {
		name   string
		result GitHubResult
		want   string
	}{
		{"nothing", GitHubResult{}, "finished without an App, an installation or repositories"},
		{"a repository that is not owner/name", GitHubResult{AppID: 1, InstallationID: 2, Account: "Acme", Repositories: []string{"not-a-repository"}}, "repositor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "linux")
			h.github = func(context.Context, githubEnv) (GitHubResult, error) { return tc.result, nil }
			err := h.podman(githubFlags...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("install = %v, want an error containing %q", err, tc.want)
			}
			h.mustNotMutate()
			if _, statErr := os.Stat(h.settingsPath()); !os.IsNotExist(statErr) {
				t.Errorf("settings were saved for a result that was refused: %v", statErr)
			}
		})
	}
}

// An App that is given with its key, whose secret cannot be looked for, stops the install before the stack changes.
func TestInstallStopsWhenTheKeySecretOfAGivenAppCannotBeLookedFor(t *testing.T) {
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	h.failFirst("secret ls --filter name=" + SecretGitHubAppKey)
	if err := h.podman(githubFlags...); !errors.Is(err, errInjected) {
		t.Fatalf("install = %v", err)
	}
	h.mustNotMutate()
	h.f.assertNoSecret(t, testPEM, "MIIEvQIBADANBgkq")
}

// A record that cannot be made is a failure of the flow, and the error says what the run left.
func TestInstallRefusesToRecordAnInvalidApp(t *testing.T) {
	h := newHarness(t, "linux")
	h.github = func(ctx context.Context, e githubEnv) (GitHubResult, error) {
		return GitHubResult{}, e.RecordApp(ctx, PendingApp{AppID: 0})
	}
	err := h.podman("--github", "yes")
	if err == nil || !strings.Contains(err.Error(), "refusing to record an invalid App") {
		t.Fatalf("install = %v", err)
	}
	if _, statErr := os.Stat(h.settingsPath() + ".pending-app"); !os.IsNotExist(statErr) {
		t.Errorf("a record of an invalid App was written: %v", statErr)
	}
	h.mustNotMutate()
}

func TestWantsGitHubForTheValuesOfTheSavedApp(t *testing.T) {
	newResolver := func(flags map[string]string) *Resolver {
		r, err := NewResolver(flags, nil, Terminal{In: strings.NewReader(""), Out: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	g := &githubInstall{s: DefaultSettings()}
	g.s.GitHub = GitHub{AppID: 1, InstallationID: 2, Owner: "Acme", Repositories: []string{"Acme/api"}}
	if yes, err := g.wantsGitHub(context.Background(), newResolver(map[string]string{"owner": "alice"})); err != nil || !yes {
		t.Errorf("values for the App of the saved settings: %v, %v; want the flow to run", yes, err)
	}
	fresh := &githubInstall{s: DefaultSettings()}
	if _, err := fresh.wantsGitHub(context.Background(), newResolver(map[string]string{"owner": "alice"})); err == nil || !strings.Contains(err.Error(), "no GitHub App to use it with") {
		t.Errorf("values without an App: %v", err)
	}
}

func TestRemoveKeySecretSaysHowForEachRuntime(t *testing.T) {
	dk, _ := linuxDocker(t)
	g := &githubInstall{rt: dk, env: Env{Deps: Deps{GOOS: "linux"}}}
	if got := g.removeKeySecret(); !strings.Contains(got, "delete the file github-app-key from the Docker volume suiteward-secrets-suiteward") {
		t.Errorf("docker: %s", got)
	}
	pm, _ := linuxPodman(t)
	g = &githubInstall{rt: pm, env: Env{Deps: Deps{GOOS: "linux"}}}
	if got := g.removeKeySecret(); got != "podman secret rm "+SecretGitHubAppKey {
		t.Errorf("podman: %s", got)
	}
	g = &githubInstall{env: Env{Deps: Deps{GOOS: "linux"}}}
	if got := g.removeKeySecret(); got != "remove it from the secret store of the container runtime" {
		t.Errorf("no engine: %s", got)
	}
}

// A key that was stored without a record of its App is named, with how to remove it, so a re-run neither fails on it nor creates a
// second App.
func TestLeftoversNamesAKeyThatHasNoRecord(t *testing.T) {
	pm, _ := linuxPodman(t)
	g := &githubInstall{rt: pm, env: Env{Deps: Deps{GOOS: "linux"}}, pendingPath: "/x/settings.yaml.pending-app"}
	cause := errors.New("record failed")
	err := g.leftovers(cause, true, false)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "but the App was not recorded") || !strings.Contains(err.Error(), "podman secret rm "+SecretGitHubAppKey) {
		t.Errorf("leftovers = %v", err)
	}
	if got := g.leftovers(cause, false, false); got != cause {
		t.Errorf("a failure that left nothing was wrapped: %v", got)
	}
}

// A record that cannot be deleted does not stop an install whose settings are saved: the run says so.
func TestClearPendingReportsARecordItCannotDelete(t *testing.T) {
	record := filepath.Join(t.TempDir(), "settings.yaml.pending-app")
	if err := os.MkdirAll(filepath.Join(record, "inside"), 0o700); err != nil { // a directory that is not empty cannot be removed as a file
		t.Fatal(err)
	}
	var out strings.Builder
	g := &githubInstall{out: &out, pendingPath: record}
	g.s.GitHub = GitHub{AppID: 1}
	g.clearPending()
	if !strings.Contains(out.String(), "Could not delete the record of the App") || !strings.Contains(out.String(), "you can delete it yourself") {
		t.Errorf("the failure was not reported: %q", out.String())
	}

	// Without an App in the settings the record stays: the App and its key are still there.
	if err := os.WriteFile(record+"2", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	g = &githubInstall{out: io.Discard, pendingPath: record + "2"}
	g.clearPending()
	if _, err := os.Stat(record + "2"); err != nil {
		t.Errorf("the record of an App the settings do not name was deleted: %v", err)
	}
}

func TestEnrolledOwnerReadsWhatStatusSays(t *testing.T) {
	answer := func(body string) func(context.Context) ([]byte, error) {
		return func(context.Context) ([]byte, error) { return []byte(body), nil }
	}
	cases := []struct {
		name, body, want, wantErr string
	}{
		{"not JSON", "<html>", "", "is not JSON"},
		{"no owner", `{"version":"v1"}`, "", ""},
		{"an owner without a login", `{"owner":{}}`, "unknown login", ""},
		{"an owner", `{"owner":{"login":"alice"}}`, "alice", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &githubInstall{}
			got, err := g.enrolledOwner(context.Background(), answer(tc.body), nil)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("enrolledOwner = %q, %v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("enrolledOwner = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// Setup runs only when the installer can tell that no owner is enrolled and it has a real terminal; a refusal of setup itself is an
// error that says the stack is running.
func TestEnrollErrors(t *testing.T) {
	noOwner := func(context.Context) ([]byte, error) { return []byte(`{}`), nil }
	t.Run("setup fails on the terminal", func(t *testing.T) {
		withTerminal(t, true)
		f := newFake("linux").on("info --format", "true\n")
		deps := f.deps()
		deps.Interactive = func(context.Context, string, []string) error { return errors.New("exit status 1") }
		rt, err := NewPodman(context.Background(), deps, "")
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		g := &githubInstall{rt: rt, env: Env{Deps: deps}, out: &out, setupOwner: "alice", setupChecks: map[string]string{"acme/app": "CI"}}
		err = g.enroll(context.Background(), noOwner)
		if err == nil || !strings.Contains(err.Error(), "the service is running, but enrolling the owner failed") || !strings.Contains(err.Error(), "exit status 1") {
			t.Fatalf("enroll = %v", err)
		}
	})
	t.Run("an owner is already enrolled", func(t *testing.T) {
		withTerminal(t, true)
		pm, f := linuxPodman(t)
		var out strings.Builder
		g := &githubInstall{rt: pm, env: Env{Deps: f.deps()}, out: &out, setupOwner: "alice", setupChecks: map[string]string{"acme/app": "CI"}}
		err := g.enroll(context.Background(), func(context.Context) ([]byte, error) { return []byte(`{"owner":{"login":"bob"}}`), nil })
		if err != nil || !strings.Contains(out.String(), "An owner (bob) is already enrolled") || len(f.tty) != 0 {
			t.Errorf("enroll = %v, tty %v, output %q", err, f.tty, out.String())
		}
	})
}
