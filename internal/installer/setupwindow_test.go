package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeWindow is the window starter of a test: it records the commands it was asked to open, and the window lives as long as wait says.
type fakeWindow struct {
	started  []call
	startErr error
	wait     func(ctx context.Context) error // nil: the window ends at once, cleanly
	opened   chan struct{}                   // closed when the window has been opened
}

func newFakeWindow() *fakeWindow { return &fakeWindow{opened: make(chan struct{})} }

func (w *fakeWindow) start(ctx context.Context, name string, args []string) (func() error, error) {
	w.started = append(w.started, call{name: name, args: slices.Clone(args)})
	if w.startErr != nil {
		return nil, w.startErr
	}
	close(w.opened)
	return func() error {
		if w.wait == nil {
			return nil
		}
		return w.wait(ctx)
	}, nil
}

// withSetupMachine makes runSetup see another machine for the test: its system, its environment and its window.
func withSetupMachine(t *testing.T, goos string, env map[string]string, window windowStarter) {
	t.Helper()
	old := setupMachine
	setupMachine = machine{goos: goos, getenv: func(k string) string { return env[k] }, window: window}
	t.Cleanup(func() { setupMachine = old })
}

// setupChecks is the one repository setup is run for in these tests.
var setupChecks = map[string]string{"acme/app": "CI / test"}

// setupOnFullScreen runs runSetup on a full screen of a terminal whose input is typed, and closes the screen again.
func setupOnFullScreen(t *testing.T, ctx context.Context, typed io.Reader, rt Runtime) (*termConsole, error) {
	t.Helper()
	c := newTermConsole(&bytes.Buffer{})
	scr, err := c.open(ctx, typed, "v0", false)
	if err != nil {
		t.Fatal(err)
	}
	err = runSetup(ctx, "suiteward", rt, "alice", setupChecks, scr)
	if closeErr := scr.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return c, err
}

func TestSetupRunsInAWindowOnlyOnWindowsAndMacOnAFullScreenOutsideSSH(t *testing.T) {
	for _, tc := range []struct {
		name     string
		goos     string
		env      map[string]string
		plain    bool
		starter  bool
		inWindow bool
	}{
		{name: "Windows", goos: "windows", starter: true, inWindow: true},
		{name: "macOS", goos: "darwin", starter: true, inWindow: true},
		{name: "Linux has no standard window, and the isolated tier runs only there", goos: "linux", starter: true},
		{name: "Windows over SSH_CONNECTION", goos: "windows", env: map[string]string{"SSH_CONNECTION": "10.0.0.2 5000 10.0.0.1 22"}, starter: true},
		{name: "macOS over SSH_TTY", goos: "darwin", env: map[string]string{"SSH_TTY": "/dev/ttys001"}, starter: true},
		{name: "Windows in plain mode", goos: "windows", plain: true, starter: true},
		{name: "macOS where open does not exist", goos: "darwin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withTerminal(t, true)
			win := newFakeWindow()
			var starter windowStarter
			if tc.starter {
				starter = win.start
			}
			withSetupMachine(t, tc.goos, tc.env, starter)
			rt := &ttyRuntime{}
			var err error
			if tc.plain {
				err = runSetup(context.Background(), "suiteward", rt, "alice", setupChecks, io.Discard)
			} else {
				_, err = setupOnFullScreen(t, liveContext(t), strings.NewReader("\n\n"), rt)
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.inWindow && (len(win.started) != 1 || rt.calls != 0) {
				t.Errorf("setup opened %d windows and took the terminal %d times; want one window and none", len(win.started), rt.calls)
			}
			if !tc.inWindow && (len(win.started) != 0 || rt.calls != 1) {
				t.Errorf("setup opened %d windows and took the terminal %d times; want the terminal once and no window", len(win.started), rt.calls)
			}
		})
	}
}

func TestSetupInAWindowRunsTheCommandOfExecTTYAndTellsNoSecret(t *testing.T) {
	withTerminal(t, true)
	win := newFakeWindow()
	withSetupMachine(t, "windows", nil, win.start)
	rt := &ttyRuntime{}
	c, err := setupOnFullScreen(t, liveContext(t), strings.NewReader("\n"), rt)
	if err != nil {
		t.Fatal(err)
	}
	argv := []string{"/suiteward", "setup", "--owner", "alice", "--test-check", "acme/app=CI / test"}
	wantName, wantArgs := rt.ExecTTYCommand(ContainerServe, argv)
	if len(win.started) != 1 || win.started[0].name != wantName || !slices.Equal(win.started[0].args, wantArgs) {
		t.Fatalf("the window ran %+v, want %s %v: the command ExecTTY would run, engine flags included", win.started, wantName, wantArgs)
	}
	for _, text := range []string{strings.Join(win.started[0].args, "\n"), c.out.String()} {
		if strings.Contains(text, "otpauth://totp") || strings.Contains(text, "secret=") {
			t.Errorf("the secret's URI is in what the installer built or printed:\n%s", text)
		}
	}
}

func TestSetupInAWindowKeepsTheInstallerOnItsOwnScreenWhileItWaits(t *testing.T) {
	withTerminal(t, true)
	win := newFakeWindow()
	withSetupMachine(t, "windows", nil, win.start)
	c, err := setupOnFullScreen(t, liveContext(t), strings.NewReader("\n"), &ttyRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	out := c.out.String()
	// The alternate screen is left once, when the screen closes after the wait: the wait was drawn on it.
	if wait, leave := strings.Index(out, "waiting for the owner enrollment in the other window"), strings.Index(out, leaveAlt); wait < 0 || leave < wait {
		t.Errorf("the installer left its screen before it showed that it waits (wait at %d, leave at %d):\n%s", wait, leave, out)
	}
	for _, want := range []string{"Enroll the owner", "Press Enter to open setup in a new window", "Ctrl-C"} {
		if !strings.Contains(out, want) {
			t.Errorf("the screens lack %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Press Enter to return to the installer") {
		t.Errorf("the installer asked for an Enter to come back from a window it never left:\n%s", out)
	}
}

func TestSetupInAWindowEndsWithTheExitOfTheWindow(t *testing.T) {
	withTerminal(t, true)
	win := newFakeWindow()
	win.wait = func(context.Context) error { return errors.New("the setup window ended with exit code 1") }
	withSetupMachine(t, "windows", nil, win.start)
	c, err := setupOnFullScreen(t, liveContext(t), strings.NewReader("\n"), &ttyRuntime{})
	if err == nil || !strings.Contains(err.Error(), "setup failed") || !strings.Contains(err.Error(), "exit code 1") {
		t.Fatalf("runSetup = %v; want setup's failure with the exit code of the window", err)
	}
	if tail := c.tail(); !strings.Contains(tail, "Setup did not finish") || strings.Contains(tail, "Press Enter to open setup") {
		t.Errorf("the screen the failure lands on is still the question:\n%s", tail)
	}
}

func TestCtrlCWhileSetupRunsInAWindowCancelsTheWaitAndTheWindow(t *testing.T) {
	withTerminal(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	win := newFakeWindow()
	closed := false
	win.wait = func(ctx context.Context) error {
		<-ctx.Done()
		closed = true // the starter takes the window with it
		return ctx.Err()
	}
	withSetupMachine(t, "windows", nil, win.start)
	go func() {
		<-win.opened
		cancel() // Ctrl-C at the installer
	}()
	rt := &ttyRuntime{}
	_, err := setupOnFullScreen(t, ctx, strings.NewReader("\n"), rt)
	if !errors.Is(err, context.Canceled) || !closed {
		t.Fatalf("runSetup = %v, window closed = %v; want the interrupt, and the window gone", err, closed)
	}
	if rt.calls != 0 {
		t.Error("setup took the terminal after the window was cancelled")
	}
}

func TestSetupTakesTheTerminalWhenTheWindowDoesNotOpen(t *testing.T) {
	withTerminal(t, true)
	win := newFakeWindow()
	win.startErr = errors.New("powershell.exe not found")
	withSetupMachine(t, "windows", nil, win.start)
	rt := &ttyRuntime{}
	c, err := setupOnFullScreen(t, liveContext(t), strings.NewReader("\n\n"), rt)
	if err != nil {
		t.Fatal(err)
	}
	if rt.calls != 1 {
		t.Fatalf("setup took the terminal %d times, want once", rt.calls)
	}
	for _, want := range []string{"window", "powershell.exe not found"} {
		if !strings.Contains(c.out.String(), want) {
			t.Errorf("the output does not say why setup runs here: no %q\n%s", want, c.out.String())
		}
	}
}

func TestSetupExplainsItselfBeforeAndAfterTheHandOver(t *testing.T) {
	withTerminal(t, true)
	c, err := setupOnFullScreen(t, liveContext(t), strings.NewReader("\n\n"), &ttyRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	out := c.out.String()
	leave, enter := strings.Index(out, leaveAlt), strings.LastIndex(out, enterAlt)
	if leave < 0 || enter < leave {
		t.Fatalf("setup did not leave the screen and come back:\n%s", out)
	}
	for _, want := range []string{"Enroll the owner", "recording", "never sees", "Press Enter to start setup"} {
		if !strings.Contains(out[:leave], want) {
			t.Errorf("the page before the hand-over lacks %q:\n%s", want, out[:leave])
		}
	}
	if tail := c.tail(); !strings.Contains(tail, "Setup finished") || strings.Contains(tail, "Press Enter to start setup") {
		t.Errorf("what stays in the scrollback is the question, not the result:\n%s", tail)
	}
	noRepeats(t, c.tail())
	// The page before the hand-over names the TOTP secret; the terminal text and the page after it do not name it again.
	if terminal := out[leave:enter]; strings.Contains(terminal, "TOTP") || !strings.Contains(terminal, "the authenticator secret (as a link and a key)") {
		t.Errorf("the terminal text repeats the name of the TOTP secret, or lost the secret:\n%s", terminal)
	}
	if tail := c.tail(); strings.Contains(tail, "TOTP") {
		t.Errorf("the page after setup repeats the name of the TOTP secret:\n%s", tail)
	}
}

func TestSetupSaysItDidNotFinishWhenItFailsOnTheTerminal(t *testing.T) {
	withTerminal(t, true)
	rt := &ttyRuntime{err: errors.New("exit status 1")}
	c, err := setupOnFullScreen(t, liveContext(t), strings.NewReader("\n\n"), rt)
	if !errors.Is(err, rt.err) {
		t.Fatalf("runSetup = %v", err)
	}
	if tail := c.tail(); !strings.Contains(tail, "Setup did not finish") || strings.Contains(tail, "Press Enter to start setup") {
		t.Errorf("the screen that comes back after a failure is not the failure's:\n%s", tail)
	}
}

func TestCtrlCAtThePageBeforeSetupStartsNothing(t *testing.T) {
	withTerminal(t, true)
	win := newFakeWindow()
	withSetupMachine(t, "windows", nil, win.start)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Ctrl-C at the page: the input never answers
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	rt := &ttyRuntime{}
	_, err := setupOnFullScreen(t, ctx, pr, rt)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runSetup = %v, want the interrupt", err)
	}
	if rt.calls != 0 || len(win.started) != 0 {
		t.Errorf("setup started after Ctrl-C at its page: %d terminal runs, %d windows", rt.calls, len(win.started))
	}
}

func TestSetupInAWindowStillNeedsARealTerminal(t *testing.T) {
	withTerminal(t, false)
	win := newFakeWindow()
	withSetupMachine(t, "windows", nil, win.start)
	rt := &ttyRuntime{}
	if err := runSetup(context.Background(), "suiteward", rt, "alice", setupChecks, io.Discard); err == nil || !strings.Contains(err.Error(), "terminal") || len(win.started) != 0 || rt.calls != 0 {
		t.Fatalf("runSetup = %v after %d windows and %d runs; want the refusal that names the terminal", err, len(win.started), rt.calls)
	}
}

// A plain run is what it was: the warning and the terminal, no page, no window.
func TestPlainSetupIsUnchangedOnWindows(t *testing.T) {
	withTerminal(t, true)
	win := newFakeWindow()
	withSetupMachine(t, "windows", nil, win.start)
	var out strings.Builder
	if err := runSetup(context.Background(), "suiteward", &ttyRuntime{}, "alice", setupChecks, &out); err != nil {
		t.Fatal(err)
	}
	want := "\nSetup makes you the owner and shows an authenticator secret (the TOTP secret, as a link and a key), only on this terminal. Stop any screen recording or terminal logging now, and make sure no coding agent can read this terminal. The installer does not capture or log what setup prints.\n"
	if out.String() != want || len(win.started) != 0 {
		t.Errorf("plain output = %q after %d windows; want %q and none", out.String(), len(win.started), want)
	}
}

// Through the whole command: the window runs the command line of the real runtime (its connection flag included), nothing goes through
// the terminal of the installer, and the page the command ends on says that the owner is enrolled, once.
func TestEnrollOnWindowsRunsSetupInAWindowWithTheEnginesCommand(t *testing.T) {
	h := enrollHarness(t, statusNoOwner)
	win := newFakeWindow()
	withSetupMachine(t, "windows", nil, win.start)
	c := newTermConsole(&bytes.Buffer{})
	env := h.newEnv()
	env.Term = Terminal{In: strings.NewReader("\n\n"), Out: &h.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}
	env.OpenScreen = c.open
	var stderr bytes.Buffer
	if code := Main(liveContext(t), []string{"enroll", "--settings", h.settingsPath(), "--owner", "alice"}, env, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if len(h.f.tty) != 0 || len(win.started) != 1 {
		t.Fatalf("setup took the terminal %d times and opened %d windows; want none and one", len(h.f.tty), len(win.started))
	}
	want := "--connection pm exec -it suiteward /suiteward setup --owner alice --test-check Acme/api=CI / test --test-check Acme/web=CI / web"
	if got := win.started[0].name + " " + strings.Join(win.started[0].args, " "); got != "podman "+want {
		t.Errorf("the window ran %q, want %q", got, "podman "+want)
	}
	if tail := c.tail(); strings.Count(tail, "alice is enrolled as the owner.") != 1 {
		t.Errorf("the scrollback must say once that the owner is enrolled:\n%s", tail)
	}
}

// A window that the installer waits on can end with a code of its own after setup has ended: the user closes it with the X, or Ctrl-C
// ends its Enter prompt. What setup left in the file, before it waited for Enter, is how it ended.
func TestSetupWindowEndsWithTheCodeSetupLeftNotWithTheCodeOfTheWindowThatWasClosed(t *testing.T) {
	closed := errors.New("the setup window was closed before setup ended (exit code 0xc000013a)")
	for _, tc := range []struct {
		name     string
		file     string // what the file holds; empty: no file
		fallback error
		want     string // empty: success; otherwise a part of the error
		wantIs   error
	}{
		{name: "closed after setup ended well", file: "0\r\n", fallback: closed},
		{name: "Ctrl-C at the installer after setup ended well", file: "0\n", fallback: context.Canceled},
		{name: "setup ended with an error", file: "7\n", want: "exit code 7"},
		{name: "closed before setup ended", fallback: closed, wantIs: closed},
		{name: "Ctrl-C before setup ended", fallback: context.Canceled, wantIs: context.Canceled},
		{name: "ended with no code left and no error", want: ""},
		{name: "a file that is not a code", file: "?\n", want: "not a number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "exit")
			if tc.file != "" {
				if err := os.WriteFile(file, []byte(tc.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := windowOutcome(file, tc.fallback)
			switch {
			case tc.want == "" && tc.wantIs == nil && err != nil:
				t.Errorf("windowOutcome = %v, want success", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("windowOutcome = %v, want an error with %q", err, tc.want)
			case tc.wantIs != nil && !errors.Is(err, tc.wantIs):
				t.Errorf("windowOutcome = %v, want %v", err, tc.wantIs)
			}
		})
	}
}

// What the pages promise of the window is what the window does on the system: it clears itself and closes on Windows, and Terminal keeps it
// open on macOS; the installer goes on when the window closes on Windows, and when setup ends on macOS, before the Enter.
func TestWindowPagesSayWhatTheWindowDoesOnTheSystem(t *testing.T) {
	for _, tc := range []struct {
		goos string
		want []string
		not  []string
	}{
		{"windows", []string{"it clears itself and closes", "goes on when the setup window closes"}, []string{"goes on when setup ends"}},
		{"darwin", []string{"it clears itself.", "goes on when setup ends"}, []string{"clears itself and closes", "when the setup window closes"}},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			withTerminal(t, true)
			withSetupMachine(t, tc.goos, nil, newFakeWindow().start)
			c, err := setupOnFullScreen(t, liveContext(t), strings.NewReader("\n"), &ttyRuntime{})
			if err != nil {
				t.Fatal(err)
			}
			out := c.out.String()
			for _, want := range tc.want {
				if !shows(out, want) {
					t.Errorf("the pages lack %q:\n%s", want, out)
				}
			}
			for _, not := range tc.not {
				if shows(out, not) {
					t.Errorf("the pages say %q, which the window does not do on %s:\n%s", not, tc.goos, out)
				}
			}
		})
	}
}
