package installer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The fix round of the review: what outlives the screen, what an interrupted wait must not undo, and what a failed terminal says.

// A setup that succeeded has enrolled the owner. Ctrl-C (or the end of the input) at the wait that follows it only skips the wait: it
// must not report the enrollment as failed, and a setup that failed reports its own error whatever happens to the wait.
func TestInterruptedWaitAfterSetupDoesNotUndoTheEnrollment(t *testing.T) {
	run := func(t *testing.T, setupErr error, cancelAfterSetup bool) (*termConsole, error) {
		t.Helper()
		withTerminal(t, true)
		h := newHarness(t, "linux")
		h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		c := newTermConsole(&bytes.Buffer{})
		env := h.newEnv()
		env.OpenScreen = c.open
		env.Deps.Interactive = func(context.Context, string, []string) error {
			if cancelAfterSetup {
				cancel() // Ctrl-C while the user reads the secret
			}
			return setupErr
		}
		return c, Install(ctx, colocatedArgs(h), env)
	}

	c, err := run(t, nil, true)
	if err != nil {
		t.Errorf("a Ctrl-C at the wait after a successful setup failed the install: %v", err)
	}
	if !strings.Contains(c.tail(), "alice (enrolled now)") {
		t.Errorf("the summary does not say that the owner is enrolled:\n%s", c.tail())
	}

	if _, err = run(t, nil, false); err != nil { // the input ends at the wait
		t.Errorf("the end of the input at the wait failed the install: %v", err)
	}

	_, err = run(t, errInjected, true)
	if !errors.Is(err, errInjected) || errors.Is(err, context.Canceled) {
		t.Errorf("a failed setup must be reported as it failed, not as an interrupted wait: %v", err)
	}
}

// fullFlowEnv is flowEnv with its output and its questions on a full Screen that has the given context.
func fullFlowEnv(t *testing.T, ctx context.Context, g *fakeGitHub, flags map[string]string, in io.Reader, terminal bool) (githubEnv, *syncBuffer) {
	t.Helper()
	env, _ := flowEnv(t, g, flags, in, terminal)
	out := &syncBuffer{}
	br := bufio.NewReader(in)
	scr, err := ui.Open(ui.Options{Out: out, In: br, Context: ctx, Version: "v0", Mode: ui.ModeFull, Color: ui.ColorNone, Width: 100, Height: 30})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scr.Close() })
	r, err := NewResolver(env.R.flags, env.R.answers, Terminal{In: br, Out: scr, IsTerminal: terminal, ReadPassword: func() ([]byte, error) { return nil, errors.New("no password here") }})
	if err != nil {
		t.Fatal(err)
	}
	env.R, env.Out = r, scr
	return env, out
}

// The page of the GitHub step is gone before the install ends: the ruleset file of each repository, and how to import it, stay.
func TestRulesetFileAndImportInstructionsStayAfterTheScreenIsGone(t *testing.T) {
	g := newFakeGitHub(t)
	env, out := fullFlowEnv(t, liveContext(t), g, existingFlags(t, nil), strings.NewReader(""), false)
	scr := env.Out.(*ui.Screen)
	scr.Steps("GitHub", "Start")
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	scr.Step(2) // the install goes on: the page of the GitHub step is gone
	if err := scr.Close(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	tail := text[strings.LastIndex(text, leaveAlt):]
	for _, want := range []string{"Ruleset for acme/app: " + filepath.Join(env.SettingsDir, "suiteward-ruleset-acme-app.json"), "Import it: open https://github.example/acme/app/settings/rules"} {
		if !strings.Contains(tail, want) {
			t.Errorf("the scrollback after Close lacks %q:\n%s", want, tail)
		}
	}
	noRepeats(t, tail)
}

// The blank line before the question is printed on its own, so that the label of the question (which an interrupted run names in its
// error) is one line; and it is printed only when the question is asked.
func TestRulesetQuestionLabelIsOneLine(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	g := newFakeGitHub(t)
	env, _ := fullFlowEnv(t, cancelled, g, existingFlags(t, nil), strings.NewReader(""), true)
	env.SkipRulesetWait = false
	_, err := resolve(t, env)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "\n") {
		t.Errorf("an interrupted question must give a one-line error, got %q", err)
	}

	asked := func(flags map[string]string, input string) string {
		g := newFakeGitHub(t)
		env, out := flowEnv(t, g, flags, strings.NewReader(input), true)
		env.SkipRulesetWait = false
		if _, err := resolve(t, env); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if text := asked(existingFlags(t, nil), "\n"); !strings.Contains(text, "create the ruleset.\n\nPress Enter when the rulesets are imported [yes]: ") {
		t.Errorf("the question lost its blank line:\n%s", text)
	}
	text := asked(existingFlags(t, map[string]string{"ruleset-imported": "yes"}), "")
	if strings.Contains(text, "Press Enter") || strings.Contains(text, "\n\n\n") {
		t.Errorf("a question that is not asked left output behind:\n%s", text)
	}
}

// install host runs dnf, which can take minutes: a full screen shows a spinner for it, and plain output has none.
func TestHostDnfRunsUnderASpinnerOnAFullScreen(t *testing.T) {
	h := newHostFake()
	h.podman = ""
	scr, br, out := fullScreenOn(t, liveContext(t), strings.NewReader(""))
	r := newTestResolver(t, map[string]string{"yes": "true", "metadata-rule": "no"}, nil, Terminal{In: br, Out: scr, ReadPassword: func() ([]byte, error) { return nil, nil }})
	if err := PrepareHost(context.Background(), r, h.deps(), scr); err != nil {
		t.Fatal(err)
	}
	if !h.has("dnf", "install -y podman") {
		t.Fatalf("dnf did not run: %v", h.lines())
	}
	if !strings.Contains(out.String(), "Running dnf install -y podman") {
		t.Error("the screen never showed the spinner of dnf")
	}
}

// A terminal that failed to take the output is reported by the command, joined with the error of the command, as doctor does through
// its exit code: the install may have gone well, and what was printed was lost.
func TestCommandsReportAFailedTerminalWithTheirOwnError(t *testing.T) {
	broken := func(_ context.Context, in io.Reader, _ string, _ bool) (*ui.Screen, error) {
		return ui.Open(ui.Options{Out: failingWriter{}, In: in, Mode: ui.ModePlain})
	}
	const lost = "the pipe is closed"

	t.Run("install", func(t *testing.T) {
		h := newHarness(t, "linux")
		env := h.newEnv()
		env.OpenScreen = broken
		err := Install(context.Background(), []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--image", testImage, "--github-app-id", "none"}, env)
		if err == nil || !strings.Contains(err.Error(), lost) {
			t.Errorf("a successful install on a terminal that failed returned %v", err)
		}
		h = newHarness(t, "linux")
		env = h.newEnv()
		env.OpenScreen = broken
		err = Install(context.Background(), []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm"}, env) // no image
		if err == nil || !strings.Contains(err.Error(), "image") || !strings.Contains(err.Error(), lost) {
			t.Errorf("the error of the command and of the terminal must both be there: %v", err)
		}
	})
	t.Run("uninstall", func(t *testing.T) {
		h := newHarness(t, "linux")
		podmanStackExists(h)
		h.writeFiles()
		env := h.newEnv()
		env.OpenScreen = broken
		args := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--yes"}
		if err := Uninstall(context.Background(), args, env); err == nil || !strings.Contains(err.Error(), lost) {
			t.Errorf("uninstall returned %v", err)
		}
	})
	t.Run("install host", func(t *testing.T) {
		env := Env{Deps: newHostFake().deps(), Term: Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}}, PortFree: func(int) error { return nil }, Sleep: func(time.Duration) {}, OpenScreen: broken}
		if err := installHost(context.Background(), []string{"--yes", "--metadata-rule", "no"}, env); err == nil || !strings.Contains(err.Error(), lost) {
			t.Errorf("install host returned %v", err)
		}
	})
	t.Run("enroll", func(t *testing.T) {
		h := enrollHarness(t, statusNoOwner)
		env := h.newEnv()
		env.Term = Terminal{In: strings.NewReader(""), Out: &h.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}
		env.OpenScreen = broken
		var stderr bytes.Buffer
		if code := Main(context.Background(), []string{"enroll", "--settings", h.settingsPath(), "--owner", "alice"}, env, &stderr); code != 1 || !strings.Contains(stderr.String(), lost) {
			t.Errorf("enroll: exit %d, stderr %q", code, stderr.String())
		}
	})
}

// The Docker discouragement is one warning: amber on the screen and kept for Close, each point on a line of its own, and no line broken
// in the middle of a sentence (the frame wraps the text itself).
func TestTheDockerWarningIsOneWarningWithItsPointsOnTheScreenAndInTheScrollback(t *testing.T) {
	c := newTermConsole(&bytes.Buffer{})
	scr, err := c.open(liveContext(t), strings.NewReader(""), "dev", false)
	if err != nil {
		t.Fatal(err)
	}
	r := newTestResolver(t, map[string]string{"accept-docker": "yes"}, nil, Terminal{In: strings.NewReader(""), Out: scr})
	if err := confirmDocker(r); err != nil {
		t.Fatal(err)
	}
	if err := scr.Close(); err != nil {
		t.Fatal(err)
	}
	// A full screen has the same two points in fewer words, so that the smallest window still shows them above the question.
	want := "! Docker is allowed on this PC, but Podman is strongly recommended.\n" +
		"    - Docker's daemon runs as root: its containers are not isolated from whoever controls it.\n" +
		"    - Its secrets live in volumes that anyone who controls the daemon can read.\n"
	if got := c.tail(); got != want {
		t.Errorf("the scrollback holds\n%s\nwant\n%s", got, want)
	}

	// Plain output is the text of the warning as it is, with no mark.
	term, out := fakeTerminal(false, "")
	term.Out = out
	if err := confirmDocker(newTestResolver(t, map[string]string{"accept-docker": "yes"}, nil, term)); err != nil {
		t.Fatal(err)
	}
	if out.String() != dockerWarning {
		t.Errorf("a plain run printed\n%q\nwant the warning as it is\n%q", out.String(), dockerWarning)
	}
}

// A service that has just started does not answer at once. Without GitHub nothing waits for it before the summary, which then left its
// Instance row out of an instance that was up a moment later.
func TestTheSummaryOfAnInstallWithoutGitHubWaitsForTheServiceToAnswer(t *testing.T) {
	run := func(t *testing.T, refusals int) (tail string, asked int) {
		t.Helper()
		h := newHarness(t, "linux")
		h.status = func(context.Context, string) ([]byte, error) {
			if asked++; asked <= refusals {
				return nil, errors.New("connection refused")
			}
			return []byte(`{"version":"v0.2.0"}`), nil
		}
		c := newTermConsole(&bytes.Buffer{})
		env := h.newEnv()
		env.OpenScreen = c.open
		args := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--image", testImage, "--github-app-id", "none"}
		if err := Install(liveContext(t), args, env); err != nil {
			t.Fatal(err)
		}
		return c.tail(), asked
	}

	if tail, _ := run(t, 3); !strings.Contains(tail, "Instance: v0.2.0\n") {
		t.Errorf("the summary has no Instance row although the service answered a few seconds in:\n%s", tail)
	}
	// A service that never answers is waited for as long as the install waits for it with GitHub, and the summary still comes.
	tail, asked := run(t, 1000)
	if strings.Contains(tail, "Instance:") || !strings.Contains(tail, "Status: ") {
		t.Errorf("the summary of a service that does not answer has an Instance row or lacks the rest:\n%s", tail)
	}
	if asked != statusTries {
		t.Errorf("/status was asked %d times, want %d", asked, statusTries)
	}
}

func TestTheSummaryOfAnUpgradeWaitsForTheServiceToAnswer(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	afterStart := 0 // the reads once the new service was started: the first ones are the summary's
	u.status = func(ctx context.Context, address string) ([]byte, error) {
		if slices.ContainsFunc(u.f.calls, func(c call) bool { return strings.Contains(c.line(), "--name suiteward --network") }) {
			if afterStart++; afterStart <= 3 {
				return nil, errors.New("connection refused")
			}
		}
		return httpGet(ctx, address)
	}
	c := newTermConsole(&u.out)
	env := u.env()
	env.OpenScreen = c.open
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", testImage, "--yes"}
	if err := Install(liveContext(t), args, env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.tail(), "Instance: v0.2.0\n") {
		t.Errorf("the summary has no Instance row although the service answered a few tries in:\n%s", c.tail())
	}
}

// On a Windows console, Ctrl-C makes the pending read return the end of the input before the signal cancels the context. A terminal's
// end of input that the context follows a moment later is an interrupt: "interrupted" and exit 130, not "read the answer: EOF" and 1.
func TestCtrlCAtAPromptOfAWindowsConsoleExits130(t *testing.T) {
	h := newHarness(t, "linux")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	time.AfterFunc(10*time.Millisecond, cancel)
	env := h.newEnv()
	env.Term = Terminal{In: strings.NewReader(""), Out: &h.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}
	env.OpenScreen = func(ctx context.Context, in io.Reader, version string, plain bool) (*ui.Screen, error) {
		return ui.Open(ui.Options{Out: &h.out, In: in, Context: ctx, Version: version, Mode: ui.ModePlain, InputIsTerminal: true})
	}
	var stderr bytes.Buffer
	code := Main(ctx, []string{"install", "--settings", h.settingsPath(), "--token-file", h.tokenPath()}, env, &stderr)
	if code != 130 || !strings.Contains(stderr.String(), "interrupted") || strings.Count(stderr.String(), "\n") != 1 {
		t.Errorf("exit %d with stderr %q, want 130 and one line saying interrupted", code, stderr.String())
	}
	h.mustNotMutate()
}

// Whatever error a command ends with after Ctrl-C, the user pressed it: the command that wrapped a prompt's error in its own words is
// no less interrupted.
func TestAnyErrorAfterCtrlCExits130(t *testing.T) {
	h := newHarness(t, "linux")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stderr bytes.Buffer
	code := Main(ctx, []string{"install", "--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm"}, h.newEnv(), &stderr) // no --image: a plain error
	if code != 130 || !strings.Contains(stderr.String(), "image") {
		t.Errorf("exit %d with stderr %q, want 130 and the error that ended the command", code, stderr.String())
	}
}

// With GitHub the enrollment waits for /status first. A service that never answers is waited for once: the summary does not wait for it
// again after the enrollment gave up.
func TestAServiceThatNeverAnswersIsWaitedForOnceByAnInstallWithGitHub(t *testing.T) {
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	asked := 0
	h.status = func(context.Context, string) ([]byte, error) {
		asked++
		return nil, errors.New("connection refused")
	}
	c := newTermConsole(&bytes.Buffer{})
	env := h.newEnv()
	env.OpenScreen = c.open
	if err := Install(liveContext(t), colocatedArgs(h), env); err != nil {
		t.Fatal(err)
	}
	if asked != statusTries {
		t.Errorf("/status was asked %d times, want %d: the wait of the enrollment, and none after it", asked, statusTries)
	}
	if tail := c.tail(); strings.Contains(tail, "Instance:") || !strings.Contains(tail, "Status: ") {
		t.Errorf("the summary of a service that does not answer has an Instance row or lacks the rest:\n%s", tail)
	}
}

// Ctrl-C while the enrollment waits for /status ends the install: the wait gives up because the context is done, and that is not a
// service that never answered. The install went on to a summary and exit 0 before.
func TestCtrlCDuringTheEnrollmentWaitEndsAnInstallWithGitHubAsInterrupted(t *testing.T) {
	for _, screen := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "full screen"}[screen], func(t *testing.T) {
			h := newHarness(t, "linux")
			h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			h.status = func(ctx context.Context, _ string) ([]byte, error) {
				cancel() // Ctrl-C while the service starts
				return nil, ctx.Err()
			}
			env := h.newEnv()
			if screen {
				env.OpenScreen = newTermConsole(&bytes.Buffer{}).open
			}
			var stderr bytes.Buffer
			code := Main(ctx, append([]string{"install"}, colocatedArgs(h)...), env, &stderr)
			if code != 130 || !strings.Contains(stderr.String(), "interrupted") || strings.Count(stderr.String(), "\n") != 1 {
				t.Errorf("exit %d with stderr %q, want 130 and one line saying interrupted", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "suiteward enroll") {
				t.Errorf("the message does not say how to enroll the owner, which the interrupt left undone: %q", stderr.String())
			}
		})
	}
}

func TestCtrlCDuringTheEnrollmentWaitEndsAnIsolatedInstallAsInterrupted(t *testing.T) {
	g := newIsoGitHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	g.ctx = ctx
	g.status = func(ctx context.Context, address string) ([]byte, error) {
		if strings.HasSuffix(address, "/status") {
			cancel()
			return nil, ctx.Err()
		}
		return []byte(`{"version":"v0.2.0"}`), nil
	}
	if err := g.run(); !errors.Is(err, context.Canceled) {
		t.Fatalf("the install ended with %v, want an error that wraps context.Canceled: Ctrl-C during the wait for /status", err)
	}
}

// doctor --json is for a program: it never draws, never waits, and keeps the code of its checks, also after Ctrl-C.
func TestDoctorJSONAfterCtrlCKeepsTheCodeOfItsChecks(t *testing.T) {
	f, s := colocated{checkOut: doctorNoGitHub}.build(t)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, stderr bytes.Buffer
	env := Env{Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &out}}
	code := Main(ctx, []string{"doctor", "--json", "--settings", path}, env, &stderr)
	var results []doctorResult
	if err := json.Unmarshal(out.Bytes(), &results); err != nil {
		t.Fatalf("the output is not the JSON of the results: %v\n%s", err, out.String())
	}
	want := 0
	if slices.ContainsFunc(results, func(r doctorResult) bool { return !r.OK }) {
		want = 1
	}
	if code != want || stderr.Len() != 0 {
		t.Errorf("exit %d with stderr %q, want %d (the code of the checks) and nothing on stderr", code, stderr.String(), want)
	}
}
