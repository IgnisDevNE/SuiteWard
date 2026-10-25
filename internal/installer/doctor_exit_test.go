package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The exit code of `doctor` is the code of its checks, and it is 1 when the terminal could not take what it printed. A code that turns
// into 1, or is 1, because of the terminal says so on stderr: macOS answered 1 for `doctor --plain` with 11 ok, 5 warnings and no failure,
// and nothing on screen said why.

// doctorOn runs `doctor` on a stack that passes (GitHub off: warnings only) with the screen that open returns, and gives the exit code and
// what went to stderr.
func doctorOn(t *testing.T, in io.Reader, open func(ctx context.Context, in io.Reader, version string, plain bool) (*ui.Screen, error), args ...string) (int, string) {
	t.Helper()
	f, s := colocated{checkOut: doctorNoGitHub}.build(t)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	env := Env{Deps: f.deps(), Term: Terminal{In: in, Out: &bytes.Buffer{}}, OpenScreen: open}
	var stderr bytes.Buffer
	return Main(liveContext(t), append([]string{"doctor", "--settings", path}, args...), env, &stderr), stderr.String()
}

// openScreenOn is the console of a test on a terminal of 100x30, over out: plain when the command asks for it, full screen otherwise.
func openScreenOn(out io.Writer, opts func(*ui.Options)) func(context.Context, io.Reader, string, bool) (*ui.Screen, error) {
	return func(ctx context.Context, in io.Reader, version string, plain bool) (*ui.Screen, error) {
		env := ui.Env{StdinTTY: true, StdoutTTY: true, Plain: plain, Width: 100, Height: 30}
		o := ui.Options{Out: out, In: in, Context: ctx, Version: version, Mode: env.Mode(), Color: ui.ColorNone, Width: 100, Height: 30}
		if opts != nil {
			opts(&o)
		}
		return ui.Open(o)
	}
}

// leaveFails is a terminal that takes everything but the write that leaves the alternate screen, the first that Close makes.
type leaveFails struct{ out bytes.Buffer } // not embedded: the screen would use the WriteString of the buffer, and never reach Write

func (l *leaveFails) Write(p []byte) (int, error) {
	if strings.Contains(string(p), leaveAlt) {
		return 0, errors.New("the terminal went away")
	}
	return l.out.Write(p)
}

func TestDoctorSaysOnStderrWhyItExitsWithOneWhenTheTerminalFails(t *testing.T) {
	t.Run("plain, nothing can be written", func(t *testing.T) {
		code, stderr := doctorOn(t, strings.NewReader(""), openScreenOn(failingWriter{}, nil), "--plain")
		if code != 1 || !strings.Contains(stderr, "the pipe is closed") {
			t.Errorf("exit %d with stderr %q, want 1 and the write error named", code, stderr)
		}
	})
	t.Run("full screen, the screen cannot be left", func(t *testing.T) {
		code, stderr := doctorOn(t, strings.NewReader("\n"), openScreenOn(&leaveFails{}, nil))
		if code != 1 || !strings.Contains(stderr, "the terminal went away") {
			t.Errorf("exit %d with stderr %q, want 1 and the error named", code, stderr)
		}
	})
	t.Run("full screen, the console cannot be put back", func(t *testing.T) {
		code, stderr := doctorOn(t, strings.NewReader("\n"), openScreenOn(&bytes.Buffer{}, func(o *ui.Options) {
			o.RestoreConsole = func() error { return errors.New("the console mode was not restored") }
		}))
		if code != 1 || !strings.Contains(stderr, "the console mode was not restored") {
			t.Errorf("exit %d with stderr %q, want 1 and the error named", code, stderr)
		}
	})
}

// Opened as ui.OpenOS opens a screen at a terminal (stdin is a terminal, and key input is there for a hidden line), a doctor that finds
// only passes and warnings exits 0 and says nothing on stderr, plain and full screen: it asks no question, so the key mode is never entered,
// whatever it would fail to restore.
func TestDoctorAtATerminalWithOnlyPassesAndWarningsExitsZero(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		enter string // what the person types at the final wait of a full screen
	}{
		{"plain", []string{"--plain"}, ""},
		{"full screen", nil, "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keyModeEntered := false
			open := openScreenOn(&bytes.Buffer{}, func(o *ui.Options) {
				o.InputIsTerminal = true
				o.KeyInput = func() (func() error, error) {
					keyModeEntered = true
					return func() error { return errors.New("the terminal mode was not restored") }, nil
				}
			})
			code, stderr := doctorOn(t, strings.NewReader(tc.enter), open, tc.args...)
			if code != 0 || stderr != "" {
				t.Errorf("exit %d with stderr %q, want 0 and nothing on stderr", code, stderr)
			}
			if keyModeEntered {
				t.Error("doctor put the terminal in key mode, which it has no question for")
			}
		})
	}
}
