package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	err = runSetup(ctx, rt, "alice", setupChecks, scr)
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
				err = runSetup(context.Background(), rt, "alice", setupChecks, io.Discard)
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
		if strings.Contains(text, "otpauth") {
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
	if err := runSetup(context.Background(), rt, "alice", setupChecks, io.Discard); err == nil || !strings.Contains(err.Error(), "terminal") || len(win.started) != 0 || rt.calls != 0 {
		t.Fatalf("runSetup = %v after %d windows and %d runs; want the refusal that names the terminal", err, len(win.started), rt.calls)
	}
}

// A plain run is what it was: the warning and the terminal, no page, no window.
func TestPlainSetupIsUnchangedOnWindows(t *testing.T) {
	withTerminal(t, true)
	win := newFakeWindow()
	withSetupMachine(t, "windows", nil, win.start)
	var out strings.Builder
	if err := runSetup(context.Background(), &ttyRuntime{}, "alice", setupChecks, &out); err != nil {
		t.Fatal(err)
	}
	want := "\nSetup enrolls you as the owner and prints a TOTP enrollment URI and secret, only on this terminal. Stop any session recording or terminal logging now, and make sure no coding agent can read this terminal. The installer does not capture or log setup's output.\n"
	if out.String() != want || len(win.started) != 0 {
		t.Errorf("plain output = %q after %d windows; want %q and none", out.String(), len(win.started), want)
	}
}
