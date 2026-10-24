package installer

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The tier is chosen with the arrow keys on a full screen. A flag, the answers file and a terminal that cannot be asked keep the order
// of Value, and a plain screen keeps its typed question.

// keyConsole is the terminal of a test whose input can be put in per-key mode: the console that ui.OpenOS opens on a real one, over a
// buffer, with a toggle that counts how often the console was put in key mode and back.
type keyConsole struct {
	out               *bytes.Buffer
	enabled, restored int
}

func (c *keyConsole) toggle() (func() error, error) {
	c.enabled++
	return func() error { c.restored++; return nil }, nil
}

func (c *keyConsole) open(ctx context.Context, in io.Reader, version string, plain bool) (*ui.Screen, error) {
	env := ui.Env{StdinTTY: true, StdoutTTY: true, Plain: plain, Width: 100, Height: 30}
	o := ui.Options{Out: c.out, In: in, Context: ctx, Version: version, Mode: env.Mode(), Color: ui.ColorNone, Width: 100, Height: 30}
	if o.Mode == ui.ModeFull {
		o.KeyInput = c.toggle
	}
	return ui.Open(o)
}

// keyResolver is a Resolver on a screen of keyConsole, reading input as a terminal does.
func keyResolver(t *testing.T, plain bool, flags, answers map[string]string, input string) (*Resolver, *keyConsole) {
	t.Helper()
	c := &keyConsole{out: &bytes.Buffer{}}
	br := bufio.NewReader(strings.NewReader(input))
	scr, err := c.open(context.Background(), br, "v0", plain)
	if err != nil {
		t.Fatal(err)
	}
	return newTestResolver(t, flags, answers, Terminal{In: br, Out: scr, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}), c
}

func TestTheTierIsChosenWithTheArrowKeysOnAFullScreen(t *testing.T) {
	for _, tc := range []struct{ name, keys, want string }{
		{"Enter takes the default", "\r", "co-located"},
		{"down, Enter", "\x1b[B\r", "isolated"},
		{"down, up, Enter", "\x1b[B\x1b[A\r", "co-located"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, c := keyResolver(t, false, nil, nil, tc.keys)
			got, err := r.Select("tier", "Installation tier", tierChoices, "co-located")
			if err != nil || got != tc.want {
				t.Fatalf("Select = %q, %v; want %q", got, err, tc.want)
			}
			if c.enabled != 1 || c.restored != 1 {
				t.Errorf("the console was put in key mode %d times and back %d times, want once each", c.enabled, c.restored)
			}
			if want := "Installation tier: " + tc.want; !strings.Contains(c.out.String(), want) {
				t.Errorf("the page lacks the answered question %q:\n%s", want, c.out.String())
			}
		})
	}
}

func TestTheTierDefaultIsTheSettingsTier(t *testing.T) {
	r, _ := keyResolver(t, false, nil, nil, "\r")
	if got, err := r.Select("tier", "Installation tier", tierChoices, "isolated"); err != nil || got != "isolated" {
		t.Fatalf("Select = %q, %v; want the saved tier on Enter", got, err)
	}
}

func TestTheTierQuestionIsNotAskedWhenSomethingElseAnswersIt(t *testing.T) {
	for _, tc := range []struct {
		name            string
		flags, answers  map[string]string
		terminal, plain bool
		want            string
	}{
		{"a flag", map[string]string{"tier": "isolated"}, map[string]string{"tier": "co-located"}, true, false, "isolated"},
		{"the answers file", nil, map[string]string{"tier": "isolated"}, true, false, "isolated"},
		{"no terminal takes the default", nil, nil, false, false, "co-located"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &keyConsole{out: &bytes.Buffer{}}
			br := bufio.NewReader(strings.NewReader("\x1b[B\r"))
			scr, err := c.open(context.Background(), br, "v0", tc.plain)
			if err != nil {
				t.Fatal(err)
			}
			r := newTestResolver(t, tc.flags, tc.answers, Terminal{In: br, Out: scr, IsTerminal: tc.terminal, ReadPassword: func() ([]byte, error) { return nil, nil }})
			if got, err := r.Select("tier", "Installation tier", tierChoices, "co-located"); err != nil || got != tc.want {
				t.Fatalf("Select = %q, %v; want %q", got, err, tc.want)
			}
			if c.enabled != 0 {
				t.Error("the console was put in key mode for a question nobody asked")
			}
			if strings.Contains(c.out.String(), "Installation tier") {
				t.Errorf("a question was drawn:\n%s", c.out.String())
			}
		})
	}
}

// A plain screen on a terminal (--plain, NO_COLOR, a small window) has no list to draw: the question is the typed one it always was.
func TestThePlainTierQuestionIsTheTypedOneItAlwaysWas(t *testing.T) {
	r, c := keyResolver(t, true, nil, nil, "isolated\n")
	got, err := r.Select("tier", "Installation tier", tierChoices, "co-located")
	if err != nil || got != "isolated" {
		t.Fatalf("Select = %q, %v; want the typed answer", got, err)
	}
	if want := "Installation tier (co-located or isolated) [co-located]: "; c.out.String() != want {
		t.Errorf("the plain output is %q, want %q", c.out.String(), want)
	}
	if c.enabled != 0 {
		t.Error("the console was put in key mode on a plain screen")
	}
}

// A saved tier that is not one of the choices is a bug of the caller, not a reason to preselect the first row.
func TestAnUnknownDefaultTierIsAnErrorAndAsksNothing(t *testing.T) {
	r, c := keyResolver(t, false, nil, nil, "\r")
	if _, err := r.Select("tier", "Installation tier", tierChoices, "cloud"); err == nil || !strings.Contains(err.Error(), "cloud") {
		t.Fatalf("Select = %v, want an error that names the default", err)
	}
	if c.enabled != 0 {
		t.Error("the console was put in key mode before the default was checked")
	}
}

// Ctrl-C at the tier question is the signal that cancels the context: the command closes its screen, puts the console back, and exits 130.
func TestCtrlCAtTheTierQuestionExits130AfterPuttingTheConsoleBack(t *testing.T) {
	h := newHarness(t, "linux")
	c := &keyConsole{out: &h.out}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	env := h.newEnv()
	env.Term = Terminal{In: cancelOnRead{cancel: cancel}, Out: &h.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}
	env.OpenScreen = c.open

	var stderr bytes.Buffer
	probe := &writerFunc{w: &stderr}
	probe.before = func() { probe.screenClosed = strings.Contains(c.out.String(), leaveAlt) && c.restored == 1 }
	code := Main(ctx, []string{"install", "--settings", h.settingsPath(), "--token-file", h.tokenPath()}, env, probe)
	if code != 130 {
		t.Errorf("exit code %d, want 130", code)
	}
	if !strings.Contains(stderr.String(), "interrupted") || strings.Count(stderr.String(), "\n") != 1 {
		t.Errorf("stderr must be one line saying so: %q", stderr.String())
	}
	if c.enabled != 1 || c.restored != 1 {
		t.Errorf("the console was put in key mode %d times and back %d times, want once each", c.enabled, c.restored)
	}
	if !probe.screenClosed {
		t.Error("the line went to stderr while the screen was open or the console still in key mode")
	}
	h.mustNotMutate()
}
