package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// A command that fails on the full screen shows its error on the screen and waits for Enter, so that the failure is read before the
// frame goes: the live run of 2026-10-23 closed the frame at once and left a bare "suiteward: ..." line. Plain output, an interrupt and a
// usage error are not changed, and the stderr line and the exit code are the ones the command always had.

const (
	termWidth, termHeight = 100, 30 // termConsole's terminal
	stepsCol              = 18      // ui's step column
)

var rowAddress = regexp.MustCompile(esc + `\[(\d+);1H`)

// finalFrame is the frame that was on the screen when it was left: the text of its title bar, its content column with the lines joined
// by single spaces (so that a wrapped text reads as one), and its hint line.
func finalFrame(t *testing.T, out string) (title, content, hint string) {
	t.Helper()
	leave := strings.LastIndex(out, leaveAlt)
	if leave < 0 {
		t.Fatal("the screen was never left")
	}
	drawn := out[:leave]
	start := strings.LastIndex(drawn, esc+"[1;1H")
	if start < 0 {
		t.Fatal("no frame was drawn")
	}
	rows := rowAddress.Split(drawn[start:], -1)[1:] // rows[i] is terminal row i+1
	if len(rows) < termHeight {
		t.Fatalf("the last frame has %d rows, want %d", len(rows), termHeight)
	}
	var body []string
	for _, r := range rows[3 : termHeight-3] { // the body: after the title bar and its rule, before the rule above the hint
		cells := []rune(r)
		body = append(body, strings.TrimSpace(string(cells[1+stepsCol+1+1:len(cells)-2])))
	}
	cells := []rune(rows[termHeight-2])
	return strings.TrimSpace(rows[1]), strings.Join(strings.Fields(strings.Join(body, " ")), " "), strings.TrimSpace(string(cells[1 : len(cells)-1]))
}

// squash drops every space: a word that is longer than the frame is cut across two rows, and the rows are joined with a space.
func squash(text string) string { return strings.Join(strings.Fields(text), "") }

// failingRun is the live example of the owner: `uninstall --purge --yes` without --instance, which refuses.
func failingRun(h *harness) []string {
	return []string{"uninstall", "--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--purge", "--yes"}
}

// the line that was always printed to stderr for it, and its error as the screen shows it
var failingLine = "confirm the purge with --yes and --instance " + testInstance + ": nothing can ask you to type the instance id here, because the input is not a terminal"

func TestAFailedCommandOnAFullScreenShowsItsErrorAndWaitsForEnter(t *testing.T) {
	h := purgeHarness(t)
	c := newTermConsole(&bytes.Buffer{})
	ctx := liveContext(t)
	var code int
	var stderr bytes.Buffer
	presses := pressesToEnd(t, func(in io.Reader) {
		env := h.newEnv()
		env.Term.In, env.OpenScreen = in, c.open
		code = Main(ctx, failingRun(h), env, &stderr)
	})
	if presses != 1 {
		t.Errorf("the command asked for %d presses of Enter, want 1: the one that ends the page of its error", presses)
	}
	if want := "suiteward: " + failingLine + "\n"; code != 1 || stderr.String() != want {
		t.Errorf("exit %d, stderr %q, want 1 and %q: the screen changes neither", code, stderr.String(), want)
	}
	title, content, hint := finalFrame(t, c.out.String())
	if !strings.Contains(title, "failed") {
		t.Errorf("the title bar of the last frame does not say that the command failed: %q", title)
	}
	if !strings.Contains(squash(content), squash(failingLine)) {
		t.Errorf("the last frame does not show the error, wrapped to the frame:\n%s", content)
	}
	if !strings.Contains(hint, "Enter to finish") {
		t.Errorf("the hint of the last frame is %q, want it to offer Enter", hint)
	}
	if tail := c.tail(); strings.Contains(tail, "confirm the purge") {
		t.Errorf("the scrollback repeats the error that stderr carries:\n%s", tail)
	}
	if !c.closed() {
		t.Error("the screen was not closed")
	}
}

// Ctrl-C at the page of the error, and the end of the input there, only close the screen: the exit code and the line are the command's own.
func TestTheEndOfTheWaitOnAFailedCommandKeepsItsExitCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   func(cancel func()) io.Reader
	}{
		{"the end of the input", func(func()) io.Reader { return strings.NewReader("") }},
		{"Ctrl-C", func(cancel func()) io.Reader { return cancelOnRead{cancel: cancel} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := purgeHarness(t)
			c := newTermConsole(&bytes.Buffer{})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			env := h.newEnv()
			env.Term.In, env.OpenScreen = tc.in(cancel), c.open
			var stderr bytes.Buffer
			if code := Main(ctx, failingRun(h), env, &stderr); code != 1 || stderr.String() != "suiteward: "+failingLine+"\n" {
				t.Errorf("exit %d, stderr %q, want 1 and the line of the command", code, stderr.String())
			}
			heldUntilEnter(t, c)
		})
	}
}

// cancelOnRead is an input that is Ctrl-C: reading it cancels the context, and the read never ends.
type cancelOnRead struct{ cancel func() }

func (r cancelOnRead) Read([]byte) (int, error) {
	r.cancel()
	select {}
}

// Plain mode has no page to wait on: the run is what it was, byte for byte, and never reads the input.
func TestAFailedCommandInPlainModeDoesNotWaitAndPrintsWhatItPrinted(t *testing.T) {
	ref := purgeHarness(t) // a run with no screen of its own: a plain one on the output
	var refErr bytes.Buffer
	if code := Main(context.Background(), failingRun(ref), ref.newEnv(), &refErr); code != 1 {
		t.Fatalf("the reference run exited %d: %s", code, refErr.String())
	}

	h := purgeHarness(t)
	c := newTermConsole(&bytes.Buffer{})
	ctx := liveContext(t)
	var code int
	var stderr bytes.Buffer
	presses := pressesToEnd(t, func(in io.Reader) {
		env := h.newEnv()
		env.Term.In, env.OpenScreen = in, c.open
		code = Main(ctx, append(failingRun(h), "--plain"), env, &stderr)
	})
	if presses != 0 {
		t.Errorf("a plain run asked for %d presses of Enter", presses)
	}
	if code != 1 || stderr.String() != refErr.String() {
		t.Errorf("exit %d, stderr %q, want 1 and %q", code, stderr.String(), refErr.String())
	}
	if strings.Contains(c.out.String(), esc) {
		t.Errorf("a plain run drew a frame:\n%q", c.out.String())
	}
	if got, want := normalize(c.out.String(), h.dir, "DIR"), normalize(ref.out.String(), ref.dir, "DIR"); got != want {
		t.Errorf("the plain output changed.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// A command that is interrupted (Ctrl-C at one of its questions) does not wait: it ends as it always did, with 130.
func TestAnInterruptedCommandOnAFullScreenDoesNotWait(t *testing.T) {
	h := purgeHarness(t)
	c := newTermConsole(&bytes.Buffer{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	env := h.newEnv()
	env.Term = Terminal{In: cancelOnRead{cancel: cancel}, Out: &h.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}
	env.OpenScreen = c.open
	var stderr bytes.Buffer
	args := []string{"uninstall", "--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--purge"} // asks for the instance id
	if code := Main(ctx, args, env, &stderr); code != 130 || !strings.Contains(stderr.String(), "interrupted") {
		t.Errorf("exit %d, stderr %q, want 130 and the interrupt", code, stderr.String())
	}
	if strings.Contains(c.out.String(), "Enter to finish") {
		t.Error("an interrupted command waited for Enter")
	}
	if !c.closed() {
		t.Error("the screen was not closed")
	}
}

// A usage error is not a failure of the work: it is printed with the usage text, and the screen that happened to be open does not wait.
func TestAUsageErrorOnAFullScreenDoesNotWait(t *testing.T) {
	h := purgeHarness(t)
	c := newTermConsole(&bytes.Buffer{})
	ctx := liveContext(t)
	var code int
	var stderr bytes.Buffer
	presses := pressesToEnd(t, func(in io.Reader) {
		env := h.newEnv()
		env.Term.In, env.OpenScreen = in, c.open
		code = Main(ctx, []string{"uninstall", "--settings", "", "--yes"}, env, &stderr)
	})
	if presses != 0 || code != 2 || !strings.Contains(stderr.String(), "usage") {
		t.Errorf("%d presses, exit %d, stderr %q, want none, 2 and the usage text", presses, code, stderr.String())
	}
	if strings.Contains(c.out.String(), "Enter to finish") {
		t.Error("a usage error waited for Enter")
	}
	if len(c.opened) != 0 {
		t.Errorf("%d screens were opened for a command line that cannot be run: a usage error comes before the screen", len(c.opened))
	}
}

// Every command that draws the screen ends a failure on the same page. Each one fails here on a settings file that does not exist, after
// its screen is open. doctor's failed checks are results and not an error: its page of counts is the only wait it has.
func TestEveryCommandShowsItsFailureOnTheScreen(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		goos       string
		settings   bool // the command is given a settings file that does not exist; the others fail on what they are asked
		wantFailed bool
	}{
		{"install", []string{"install", "--runtime", "podman", "--connection", "pm"}, "linux", false, true}, // no image
		{"install, isolated", []string{"install", "--tier", "isolated"}, "linux", false, true},              // not the service account
		{"install --upgrade", []string{"install", "--upgrade", "--yes", "--image", testImage}, "linux", true, true},
		{"install host", []string{"install", "host", "--yes"}, "windows", false, true}, // not AlmaLinux
		{"uninstall", []string{"uninstall", "--yes"}, "linux", true, true},
		{"enroll", []string{"enroll", "--owner", "alice"}, "linux", true, true},
		{"backup init", []string{"backup", "init", "--yes"}, "linux", true, true},
		{"backup", []string{"backup"}, "linux", true, true},
		{"restore", []string{"restore", "backup.tar.age", "--yes", "--instance", testInstance}, "linux", true, true},
		{"doctor", []string{"doctor"}, "linux", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTerminal(t, true) // enroll refuses a console that is not a terminal before it reads anything
			h := newHarness(t, tc.goos)
			missing := filepath.Join(h.dir, "nowhere", "settings.yaml")
			args := slices.Clone(tc.args)
			if tc.settings {
				args = append(args, "--settings", missing)
			}
			c := newTermConsole(&bytes.Buffer{})
			ctx := liveContext(t)
			var code int
			var stderr bytes.Buffer
			presses := pressesToEnd(t, func(in io.Reader) {
				env := h.newEnv()
				env.Term.In, env.OpenScreen = in, c.open
				code = Main(ctx, args, env, &stderr)
			})
			if code != 1 {
				t.Fatalf("exit %d, stderr %q, want 1", code, stderr.String())
			}
			if presses != 1 {
				t.Errorf("the command asked for %d presses of Enter, want 1", presses)
			}
			heldUntilEnter(t, c)
			title, content, _ := finalFrame(t, c.out.String())
			if failed := strings.Contains(title, "failed"); failed != tc.wantFailed {
				t.Errorf("the title bar of the last frame is %q: failed is %v, want %v", title, failed, tc.wantFailed)
			}
			if tc.wantFailed {
				if line := strings.TrimSpace(strings.TrimPrefix(stderr.String(), "suiteward: ")); !strings.Contains(squash(content), squash(line)) {
					t.Errorf("the last frame does not show the error that stderr carries (%q):\n%s", line, content)
				}
			}
		})
	}
}

// The error is drawn as text, with the credentials of a URL hidden, as the doctor hides them.
func TestTheErrorOnTheScreenHidesTheCredentialsOfAURL(t *testing.T) {
	c := newTermConsole(&bytes.Buffer{})
	scr, err := c.open(liveContext(t), strings.NewReader("\n"), "dev", false)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("connect to postgres://suiteward:hunter2@suiteward-db:5432/suiteward: connection refused")
	endConsole(scr, &failure)
	if strings.Contains(c.out.String(), "hunter2") {
		t.Errorf("the password reached the screen:\n%q", c.out.String())
	}
	if _, content, _ := finalFrame(t, c.out.String()); !strings.Contains(squash(content), squash("connection refused")) {
		t.Errorf("the error is not on the screen:\n%s", content)
	}
}
