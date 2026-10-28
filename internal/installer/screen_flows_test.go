package installer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Every command of the installer opens one Screen, draws its steps on it and closes it on every path. A run that is not on a terminal
// is plain, and a plain run prints what it printed before the Screen existed.

const child = "CHILD-OUTPUT-0451 the TOTP secret\n"

// colocatedArgs are the arguments of a Podman install with the GitHub App given.
func colocatedArgs(h *harness, extra ...string) []string {
	args := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--image", testImage}
	return append(append(args, githubFlags...), extra...)
}

func TestInstallColocatedOnAFullScreenWalksTheStepsAndEndsWithASummary(t *testing.T) {
	withTerminal(t, true)
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	c := newTermConsole(&bytes.Buffer{})
	env := h.newEnv()
	env.OpenScreen = c.open
	inner := env.Deps.Interactive
	env.Deps.Interactive = func(ctx context.Context, name string, args []string) error { // what the child setup prints on the real terminal
		c.out.WriteString(child)
		return inner(ctx, name, args)
	}
	if err := Install(liveContext(t), colocatedArgs(h), env); err != nil {
		t.Fatal(err)
	}
	out := c.out.String()

	c.steps(t, "Environment", "Checks", "GitHub", "Start", "Owner", "Done")
	if !c.closed() {
		t.Error("the screen was not closed")
	}
	for _, want := range []string{h.tokenPath(), "http://127.0.0.1:27081/status", "v0.2.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary lacks %q", want)
		}
		if !strings.Contains(c.tail(), want) {
			t.Errorf("%q is not in the scrollback after Close:\n%s", want, c.tail())
		}
	}
	noRepeats(t, c.tail())

	// Setup runs on the real terminal: the screen was left before it and entered again after it, and its output is neither redrawn nor
	// repeated.
	at := strings.Index(out, child)
	if at < 0 || strings.Count(out, child) != 1 {
		t.Fatalf("the child's output must appear once, got %d", strings.Count(out, child))
	}
	if strings.LastIndex(out[:at], leaveAlt) < strings.LastIndex(out[:at], enterAlt) {
		t.Error("the child ran on the alternate screen")
	}
	back := strings.Index(out[at:], enterAlt)
	if back < 0 {
		t.Fatal("the screen was not entered again after setup")
	}
	if between := out[at : at+back]; !strings.Contains(between, "Press Enter") {
		t.Errorf("the secret must stay on the terminal until the user is done with it, and nothing asked for that:\n%s", between)
	}
	if strings.Contains(c.tail(), "CHILD-OUTPUT") {
		t.Error("the child's output was printed again by Close")
	}
	h.f.assertNoSecret(t, testPEM)
}

func TestInstallColocatedOnAFullScreenShowsTheWaitsAndTheLongCommands(t *testing.T) {
	h := newHarness(t, "linux")
	c := newTermConsole(&bytes.Buffer{})
	env := h.newEnv()
	env.OpenScreen = c.open
	if err := Install(liveContext(t), []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--image", testImage, "--github-app-id", "none"}, env); err != nil {
		t.Fatal(err)
	}
	out := c.out.String()
	for _, want := range []string{"Database", "Pulling"} {
		if !strings.Contains(out, want) {
			t.Errorf("the screen never showed %q", want)
		}
	}
	c.steps(t, "Environment", "Checks", "GitHub", "Start", "Owner", "Done")
}

func TestPlainFlagKeepsATerminalFromDrawing(t *testing.T) {
	run := func(t *testing.T, terminal bool, extra ...string) (string, *termConsole) {
		t.Helper()
		h := newHarness(t, "linux")
		h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
		env := h.newEnv()
		c := newTermConsole(&h.out)
		if terminal {
			env.OpenScreen = c.open
		}
		if err := Install(liveContext(t), colocatedArgs(h, extra...), env); err != nil {
			t.Fatal(err)
		}
		return normalize(h.out.String(), h.dir, "DIR"), c
	}

	pipe, _ := run(t, false)
	plain, c := run(t, true, "--plain")
	if len(c.opened) != 1 || !c.plain[0] || strings.Contains(plain, esc) {
		t.Fatalf("--plain on a terminal drew: opened %d, asked plain %v, output has escapes: %v", len(c.opened), c.plain, strings.Contains(plain, esc))
	}
	if plain != pipe {
		t.Errorf("a plain run on a terminal differs from a run on a pipe.\nplain:\n%s\npipe:\n%s", plain, pipe)
	}

	// The answers file asks for it too, and a flag still wins over the file.
	answers := filepath.Join(t.TempDir(), "answers.yaml")
	if err := os.WriteFile(answers, []byte("plain: yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, c := run(t, true, "--answers", answers); strings.Contains(got, esc) || !c.plain[0] {
		t.Errorf("plain: yes in the answers file did not keep the terminal from drawing:\n%s", got)
	}
	if got, c := run(t, true, "--answers", answers, "--plain=false"); !strings.Contains(got, enterAlt) || c.plain[0] {
		t.Errorf("--plain=false must win over the answers file")
	}
	if got, _ := run(t, true); !strings.Contains(got, enterAlt) {
		t.Error("a terminal without --plain did not get the full screen")
	}
}

func TestInterruptedAtAPromptExits130AfterClosingTheScreen(t *testing.T) {
	h := newHarness(t, "linux")
	c := newTermConsole(&h.out)
	in := &countingReader{data: "co-located\n"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Ctrl-C was pressed before the first question
	env := h.newEnv()
	env.Term = Terminal{In: in, Out: &h.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}
	env.OpenScreen = c.open

	var stderr bytes.Buffer
	probe := &writerFunc{w: &stderr}
	probe.before = func() { probe.screenClosed = c.closed() && strings.Contains(c.out.String(), leaveAlt) }
	code := Main(ctx, []string{"install", "--settings", h.settingsPath(), "--token-file", h.tokenPath()}, env, probe)
	if code != 130 {
		t.Errorf("exit code %d, want 130", code)
	}
	if !strings.Contains(stderr.String(), "interrupted") || strings.Count(stderr.String(), "\n") != 1 {
		t.Errorf("stderr must be one line saying so: %q", stderr.String())
	}
	if !probe.screenClosed {
		t.Error("the screen was still open when the line went to stderr")
	}
	if in.reads != 0 {
		t.Errorf("the input was read %d times", in.reads)
	}
	h.mustNotMutate()
}

// writerFunc runs before each write, to look at the world as the writer finds it.
type writerFunc struct {
	w            *bytes.Buffer
	before       func()
	screenClosed bool
}

func (w *writerFunc) Write(p []byte) (int, error) {
	if w.before != nil {
		w.before()
	}
	return w.w.Write(p)
}

func TestInstallColocatedReportsAnErrorAfterClosingTheScreen(t *testing.T) {
	h := newHarness(t, "linux")
	c := newTermConsole(&h.out)
	env := h.newEnv()
	env.OpenScreen = c.open
	var stderr bytes.Buffer
	probe := &writerFunc{w: &stderr}
	probe.before = func() { probe.screenClosed = c.closed() }
	code := Main(liveContext(t), []string{"install", "--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm"}, env, probe) // no --image, and a development build carries none
	if code != 1 || !strings.Contains(stderr.String(), "image") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if !probe.screenClosed {
		t.Error("the error was printed while the screen was still open: it would vanish with the alternate screen")
	}
}

func TestUpgradeColocatedOnAFullScreenKeepsTheBackupPath(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	c := newTermConsole(&u.out)
	env := u.env()
	env.OpenScreen = c.open
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", testImage, "--yes"}
	if err := Install(liveContext(t), args, env); err != nil {
		t.Fatal(err)
	}
	c.steps(t, "Plan", "Backup", "Images", "Replace", "Check")
	if !c.closed() {
		t.Error("the screen was not closed")
	}
	if !strings.Contains(c.tail(), u.backup()) {
		t.Errorf("the backup path is not in the scrollback after Close:\n%s", c.tail())
	}
	noRepeats(t, c.tail())
	if !strings.Contains(c.out.String(), "Pulling") {
		t.Error("the pull showed no spinner")
	}
}

func TestUpgradeIsolatedOnAFullScreenKeepsTheBackupPath(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	u.f.noImage = true
	fastHealthWait(t)
	c := newTermConsole(&u.out)
	env := u.env()
	env.OpenScreen = c.open
	args := []string{"--upgrade", "--tier", "isolated", "--settings", settingsFile(u.home), "--backup-dir", u.backups, "--image", testDigest, "--yes"}
	if err := Install(liveContext(t), args, env); err != nil {
		t.Fatal(err)
	}
	c.steps(t, "Plan", "Backup", "Images", "Replace", "Check")
	if !strings.Contains(c.tail(), u.backups) {
		t.Errorf("the backup path is not in the scrollback after Close:\n%s", c.tail())
	}
	noRepeats(t, c.tail())
	if !strings.Contains(c.out.String(), "Service") {
		t.Error("the wait for the service showed no bar")
	}
}

func TestInstallIsolatedOnAFullScreenWalksTheStepsAndEndsWithASummary(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.noImage = true
	c := newTermConsole(&bytes.Buffer{})
	env := Env{
		Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}}, PortFree: func(int) error { return nil }, Sleep: func(time.Duration) {},
		HTTPGet:    func(context.Context, string) ([]byte, error) { return []byte(`{"version":"v0.2.0"}`), nil },
		OpenScreen: c.open,
	}
	args := []string{"--tier", "isolated", "--settings", settingsFile(f.home), "--image", testDigest, "--github-app-id", "none"}
	if err := Install(liveContext(t), args, env); err != nil {
		t.Fatal(err)
	}
	c.steps(t, "Checks", "Images", "GitHub", "Units", "Start", "Owner", "Done")
	for _, want := range []string{"http://127.0.0.1:27081/status", settingsFile(f.home)} {
		if !strings.Contains(c.tail(), want) {
			t.Errorf("%q is not in the scrollback after Close:\n%s", want, c.tail())
		}
	}
	noRepeats(t, c.tail())
	for _, want := range []string{"Database", "Service", "Pulling"} {
		if !strings.Contains(c.out.String(), want) {
			t.Errorf("the screen never showed %q", want)
		}
	}
}

func TestUninstallOnAFullScreenShowsTheSteps(t *testing.T) {
	h := newHarness(t, "linux")
	podmanStackExists(h)
	h.writeFiles()
	c := newTermConsole(&bytes.Buffer{})
	env := h.newEnv()
	env.OpenScreen = c.open
	args := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--yes"}
	if err := Uninstall(liveContext(t), args, env); err != nil {
		t.Fatal(err)
	}
	c.steps(t, "Confirm", "Remove")
	if !c.closed() || !strings.Contains(c.tail(), "Removed the containers") {
		t.Errorf("the page it stopped on must stay in the scrollback:\n%s", c.tail())
	}

	u := newIsoUpgrade(t, nil)
	c = newTermConsole(&u.out)
	env = u.env()
	env.OpenScreen = c.open
	if err := Uninstall(liveContext(t), []string{"--tier", "isolated", "--settings", settingsFile(u.home), "--yes"}, env); err != nil {
		t.Fatal(err)
	}
	c.steps(t, "Confirm", "Remove")
}

func TestInstallHostOnAFullScreenShowsTheSteps(t *testing.T) {
	h := newHostFake()
	c := newTermConsole(&bytes.Buffer{})
	env := Env{Deps: h.deps(), Term: Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}}, PortFree: func(int) error { return nil }, Sleep: func(time.Duration) {}, OpenScreen: c.open}
	if err := installHost(liveContext(t), append([]string{"--yes", "--metadata-rule", "no"}, hostSumsArgs(t, h)...), env); err != nil {
		t.Fatal(err)
	}
	c.steps(t, "Podman", "Account", "Metadata", "Executor", "Next")
	if !c.closed() || !strings.Contains(c.tail(), "The host is ready.") {
		t.Errorf("the next command must stay in the scrollback:\n%s", c.tail())
	}
}

func TestEnrollOnAFullScreenRunsSetupOnTheRealTerminal(t *testing.T) {
	h := enrollHarness(t, statusNoOwner)
	c := newTermConsole(&bytes.Buffer{})
	env := h.newEnv()
	env.Term = Terminal{In: strings.NewReader("\n"), Out: &h.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}
	env.OpenScreen = c.open
	inner := env.Deps.Interactive
	env.Deps.Interactive = func(ctx context.Context, name string, args []string) error {
		c.out.WriteString(child)
		return inner(ctx, name, args)
	}
	var stderr bytes.Buffer
	if code := Main(liveContext(t), []string{"enroll", "--settings", h.settingsPath(), "--owner", "alice"}, env, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	c.steps(t, "Instance", "Owner")
	out := c.out.String()
	at := strings.Index(out, child)
	if at < 0 || strings.LastIndex(out[:at], leaveAlt) < strings.LastIndex(out[:at], enterAlt) || !strings.Contains(out[at:], enterAlt) {
		t.Errorf("setup must run between leaving and entering the alternate screen:\n%s", out)
	}
	if strings.Contains(c.tail(), "CHILD-OUTPUT") {
		t.Error("the child's output was printed again by Close")
	}
	// The page it holds says what happened: the check is drawn on the screen that came back after setup, and kept for the scrollback.
	if at := strings.LastIndex(out, "alice is enrolled as the owner."); at < strings.LastIndex(out, enterAlt) {
		t.Errorf("the finished enrollment was not on the screen that the command waited on")
	}
	if !strings.Contains(c.tail(), "✓ alice is enrolled as the owner.\n") {
		t.Errorf("the scrollback does not say that the owner is enrolled:\n%s", c.tail())
	}
}

const doctorNoGitHub = `[{"name":"github.app","ok":true,"warning":true,"detail":"not configured: GitHub is off in this instance","fix":"set GitHub up"},` +
	`{"name":"database.schema","ok":true,"detail":"schema version 20","fix":""}]`

func TestDoctorOnAFullScreenShowsItsChecksAndKeepsItsExitCode(t *testing.T) {
	f, s := colocated{checkOut: doctorNoGitHub, ready: 503}.build(t)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	c := newTermConsole(&bytes.Buffer{})
	env := Env{Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}}, OpenScreen: c.open}
	var stderr bytes.Buffer
	if code := Main(liveContext(t), []string{"doctor", "--settings", path, "--token-file", coloTokenFile(t, f)}, env, &stderr); code != 1 {
		t.Errorf("exit %d, want 1: a failed check is a failed doctor", code)
	}
	c.steps(t, "Doctor")
	tail := c.tail()
	// A passing check is its name beside a check mark, a failed or doubtful one is the line doctor prints beside the warning mark.
	for _, want := range []string{"\n✓ settings\n", "\n! FAIL ports: ", "\n! WARN github.app: not configured: GitHub is off in this instance — set GitHub up\n"} {
		if !strings.Contains("\n"+tail, want) {
			t.Errorf("the scrollback lacks %q:\n%s", want, tail)
		}
	}
	noRepeats(t, tail)

	// The same checks on a pipe print the same lines, and JSON is never drawn.
	f, s = colocated{checkOut: doctorNoGitHub, ready: 503}.build(t)
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	var piped bytes.Buffer
	env = Env{Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &piped}}
	_ = Main(liveContext(t), []string{"doctor", "--settings", path, "--token-file", coloTokenFile(t, f)}, env, &stderr)
	port := regexp.MustCompile(`port \d+`) // the stack of each build listens on a port of its own
	for _, line := range strings.Split(strings.TrimSpace(piped.String()), "\n") {
		if name, ok := strings.CutPrefix(line, "ok "); ok {
			line = "✓ " + name
		} else {
			line = "! " + line
		}
		if line = port.ReplaceAllString(line, "port N"); !strings.Contains(port.ReplaceAllString(tail, "port N"), line) {
			t.Errorf("the line %q of the plain output is not in the full-screen scrollback", line)
		}
	}
	// The counts end the page and the scrollback; a warning is not an ok and not a failure.
	var oks, warnings, failures int
	for _, line := range strings.Split(strings.TrimSpace(piped.String()), "\n") {
		switch {
		case strings.HasPrefix(line, "ok "):
			oks++
		case strings.HasPrefix(line, "WARN "):
			warnings++
		default:
			failures++
		}
	}
	counts := fmt.Sprintf("%d ok, %d warnings, %d failures", oks, warnings, failures)
	if !strings.HasSuffix(strings.TrimSpace(tail), counts) {
		t.Errorf("the scrollback does not end with %q:\n%s", counts, tail)
	}
	if frames := c.out.String(); !strings.Contains(frames[:strings.LastIndex(frames, leaveAlt)], counts) {
		t.Errorf("the counts %q were never on the screen", counts)
	}
	if strings.Contains(piped.String(), "ok, ") {
		t.Errorf("a plain run prints no counts:\n%s", piped.String())
	}

	c = newTermConsole(&bytes.Buffer{})
	f, s = colocated{checkOut: doctorNoGitHub}.build(t)
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	env = Env{Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}}, OpenScreen: c.open}
	if code := Main(liveContext(t), []string{"doctor", "--json", "--settings", path, "--token-file", coloTokenFile(t, f)}, env, &stderr); code != 0 {
		t.Errorf("doctor --json exit %d", code)
	}
	if len(c.opened) != 0 || strings.Contains(c.out.String(), esc) {
		t.Errorf("doctor --json opened a screen (%d) or wrote escapes", len(c.opened))
	}
}

func TestPlainRunsPrintWhatTheyPrintedBefore(t *testing.T) {
	// The same upgrade, once on a pipe and once with --plain on a terminal: the Screen adds nothing a plain run prints.
	run := func(t *testing.T, terminal bool) string {
		t.Helper()
		u := newUpgradeHarness(t, nil)
		c := newTermConsole(&u.out)
		env := u.env()
		if terminal {
			env.OpenScreen = c.open
		}
		args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", testImage, "--yes"}
		if terminal {
			args = append(args, "--plain")
		}
		if err := Install(liveContext(t), args, env); err != nil {
			t.Fatal(err)
		}
		return normalize(u.out.String(), u.dir, "DIR", strconv.Itoa(u.port), "PORT", filepath.Base(u.backup()), "BACKUP")
	}
	if pipe, plain := run(t, false), run(t, true); pipe != plain {
		t.Errorf("the plain upgrade differs.\nplain:\n%s\npipe:\n%s", plain, pipe)
	}
}
