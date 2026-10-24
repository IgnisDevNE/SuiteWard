package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// A command that opens the full screen ends on a page that waits for Enter, and only then closes it: what it printed last is the
// output the user came for (the command to run next, what was removed, the results of the checks), and a screen that vanishes at once
// is a flash.

// enterGate is the input of a command that waits for Enter: every read blocks until the test presses Enter.
type enterGate struct{ press, quit chan struct{} }

func (g *enterGate) Read(p []byte) (int, error) {
	select {
	case <-g.press:
		return copy(p, "\n"), nil
	case <-g.quit:
		return 0, io.EOF
	}
}

// pressesToEnd runs a command on the gate and presses Enter each time it reads, until it returns. It reports how many presses the
// command asked for.
func pressesToEnd(t *testing.T, run func(in io.Reader)) int {
	t.Helper()
	g := &enterGate{press: make(chan struct{}), quit: make(chan struct{})}
	t.Cleanup(func() { close(g.quit) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(g)
	}()
	presses, giveUp := 0, time.After(30*time.Second)
	for {
		select {
		case <-done:
			return presses
		case g.press <- struct{}{}:
			if presses++; presses > 5 {
				t.Fatal("the command asked for more than five presses of Enter")
			}
		case <-giveUp:
			t.Fatalf("the command did not end after %d presses of Enter", presses)
		}
	}
}

// heldUntilEnter checks that the last frame of the run, the one the command waited on, was drawn before the screen was left.
func heldUntilEnter(t *testing.T, c *termConsole) {
	t.Helper()
	out := c.out.String()
	hint, leave := strings.LastIndex(out, "Enter to finish"), strings.LastIndex(out, leaveAlt)
	if hint < 0 || leave < 0 || hint > leave {
		t.Errorf("the screen was left without waiting for Enter (hint at %d, screen left at %d)", hint, leave)
	}
	if !c.closed() {
		t.Error("the screen was not closed")
	}
}

func TestEveryCommandEndsOnAPageThatWaitsForEnter(t *testing.T) {
	ctx := liveContext(t)
	cases := []struct {
		name    string
		presses int // the Enters the whole command asks for
		run     func(t *testing.T, c *termConsole, in io.Reader) error
	}{
		{"uninstall", 1, func(t *testing.T, c *termConsole, in io.Reader) error {
			h := newHarness(t, "linux")
			podmanStackExists(h)
			h.writeFiles()
			env := h.newEnv()
			env.Term.In, env.OpenScreen = in, c.open
			args := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--yes"}
			return Uninstall(ctx, args, env)
		}},
		{"uninstall, isolated", 1, func(t *testing.T, c *termConsole, in io.Reader) error {
			u := newIsoUpgrade(t, nil)
			env := u.env()
			env.Term.In, env.OpenScreen = in, c.open
			return Uninstall(ctx, []string{"--tier", "isolated", "--settings", settingsFile(u.home), "--yes"}, env)
		}},
		{"install host", 1, func(t *testing.T, c *termConsole, in io.Reader) error {
			h := newHostFake()
			env := Env{Deps: h.deps(), Term: Terminal{In: in, Out: &bytes.Buffer{}}, PortFree: func(int) error { return nil }, Sleep: func(time.Duration) {}, OpenScreen: c.open}
			return installHost(ctx, []string{"--yes", "--metadata-rule", "no"}, env)
		}},
		{"enroll", 3, func(t *testing.T, c *termConsole, in io.Reader) error { // one Enter starts setup, one ends its page of the real terminal
			h := enrollHarness(t, statusNoOwner)
			env := h.newEnv()
			env.Term = Terminal{In: in, Out: &h.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}
			env.OpenScreen = c.open
			if code := Main(ctx, []string{"enroll", "--settings", h.settingsPath(), "--owner", "alice"}, env, io.Discard); code != 0 {
				return errors.New("enroll failed")
			}
			return nil
		}},
		{"upgrade with nothing to do", 1, func(t *testing.T, c *termConsole, in io.Reader) error {
			u := newUpgradeHarness(t, nil)
			env := u.env()
			env.Term.In, env.OpenScreen = in, c.open
			args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", oldImage, "--yes"}
			return Install(ctx, args, env)
		}},
		{"upgrade with nothing to do, isolated", 1, func(t *testing.T, c *termConsole, in io.Reader) error {
			u := newIsoUpgrade(t, func(p *UnitParams) { p.Digest = testDigest })
			fastHealthWait(t)
			env := u.env()
			env.Term.In, env.OpenScreen = in, c.open
			return Install(ctx, []string{"--upgrade", "--tier", "isolated", "--settings", settingsFile(u.home), "--backup-dir", u.backups, "--image", testDigest, "--yes"}, env)
		}},
		{"doctor", 1, func(t *testing.T, c *termConsole, in io.Reader) error {
			f, s := colocated{checkOut: doctorNoGitHub}.build(t)
			path := filepath.Join(t.TempDir(), "settings.yaml")
			if err := SaveSettings(path, s); err != nil {
				return err
			}
			env := Env{Deps: f.deps(), Term: Terminal{In: in, Out: &bytes.Buffer{}}, OpenScreen: c.open}
			Main(ctx, []string{"doctor", "--settings", path}, env, io.Discard)
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTermConsole(&bytes.Buffer{})
			var err error
			presses := pressesToEnd(t, func(in io.Reader) { err = tc.run(t, c, in) })
			if err != nil {
				t.Fatal(err)
			}
			if presses != tc.presses {
				t.Errorf("the command asked for %d presses of Enter, want %d: one for its last page", presses, tc.presses)
			}
			heldUntilEnter(t, c)
		})
	}
}

func TestDoctorOnAFullScreenKeepsItsExitCodeWhenTheWaitEnds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in       io.Reader
		wantCode int
		wantErr  string
	}{
		{"the end of the input", strings.NewReader(""), 0, ""},
		{"a failed read", iotest.ErrReader(errors.New("input lost")), 1, "wait for Enter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := colocated{checkOut: doctorNoGitHub}.build(t)
			path := filepath.Join(t.TempDir(), "settings.yaml")
			if err := SaveSettings(path, s); err != nil {
				t.Fatal(err)
			}
			c := newTermConsole(&bytes.Buffer{})
			env := Env{Deps: f.deps(), Term: Terminal{In: tc.in, Out: &bytes.Buffer{}}, OpenScreen: c.open}
			var stderr bytes.Buffer
			if code := Main(liveContext(t), []string{"doctor", "--settings", path}, env, &stderr); code != tc.wantCode {
				t.Errorf("exit %d, want %d (stderr %q)", code, tc.wantCode, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.wantErr) || (tc.wantErr == "" && stderr.Len() != 0) {
				t.Errorf("stderr is %q, want it to name %q", stderr.String(), tc.wantErr)
			}
			if !c.closed() {
				t.Error("the screen was not closed")
			}
		})
	}
}

func TestPlainDoctorOutputIsTheResultLinesAndNothingElse(t *testing.T) {
	results := []doctorResult{
		{Name: "settings", OK: true},
		{Name: "github.app", OK: true, Warning: true, Detail: "not configured", Fix: "set GitHub up"},
		{Name: "ports", Detail: "in use", Fix: "free it"},
	}
	const want = "ok settings\nWARN github.app: not configured — set GitHub up\nFAIL ports: in use — free it\n"
	var piped bytes.Buffer
	if err := writeDoctor(&piped, results, false); err != nil || piped.String() != want {
		t.Errorf("a pipe got %q (%v), want %q", piped.String(), err, want)
	}
	var out bytes.Buffer
	scr, err := ui.Open(ui.Options{Out: &out, In: strings.NewReader(""), Mode: ui.ModePlain})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDoctor(scr, results, false); err != nil || out.String() != want {
		t.Errorf("a plain screen got %q (%v), want %q: no counts, no marks", out.String(), err, want)
	}
}

// Ctrl-C ends doctor as it ends the rest of the installer: "interrupted" and 130, whatever the checks found. A Ctrl-C at the final wait
// does not: the checks are done, and doctor keeps its own code.
func TestCtrlCDuringDoctorExits130AndCtrlCAtItsWaitKeepsItsCode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ready int // 503: a check fails
		code  int
	}{
		{"checks that pass", 0, 0},
		{"checks that fail", 503, 1},
	} {
		run := func(t *testing.T, ctx context.Context, in io.Reader, screen bool) (code int, stderr string) {
			t.Helper()
			f, s := colocated{checkOut: doctorNoGitHub, ready: tc.ready}.build(t)
			path := filepath.Join(t.TempDir(), "settings.yaml")
			if err := SaveSettings(path, s); err != nil {
				t.Fatal(err)
			}
			env := Env{Deps: f.deps(), Term: Terminal{In: in, Out: &bytes.Buffer{}}}
			if screen {
				env.OpenScreen = newTermConsole(&bytes.Buffer{}).open
			}
			var errOut bytes.Buffer
			code = Main(ctx, []string{"doctor", "--settings", path}, env, &errOut)
			return code, errOut.String()
		}
		t.Run(tc.name+", Ctrl-C during the checks", func(t *testing.T) {
			for _, screen := range []bool{true, false} {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if code, stderr := run(t, ctx, strings.NewReader(""), screen); code != 130 || !strings.Contains(stderr, "interrupted") || strings.Count(stderr, "\n") != 1 {
					t.Errorf("full screen %v: exit %d with stderr %q, want 130 and one line saying interrupted", screen, code, stderr)
				}
			}
		})
		t.Run(tc.name+", Ctrl-C at the final wait", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			pressed := readFunc(func([]byte) (int, error) { // the wait reads: Ctrl-C comes then
				cancel()
				return 0, io.EOF
			})
			if code, stderr := run(t, ctx, pressed, true); code != tc.code || stderr != "" {
				t.Errorf("exit %d with stderr %q, want %d and nothing on stderr: the checks were done", code, stderr, tc.code)
			}
		})
	}
}

type readFunc func([]byte) (int, error)

func (f readFunc) Read(p []byte) (int, error) { return f(p) }
