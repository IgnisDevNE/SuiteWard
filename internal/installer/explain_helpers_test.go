package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The helpers of the tests that read the pages of the full screen: a terminal that answers one line per question and keeps what the screen
// showed while each was waiting, and a reader of one frame of that screen.

// noSecret is the password reader of a terminal that is not asked for a secret.
func noSecret() ([]byte, error) { return nil, errors.New("no secret is asked for") }

// answerer is the input of a terminal that answers one line per read. A read is a question that waits, so what the screen has drawn so far is
// kept: shown[i] is the output while the i-th question waited. When the answers are used up the input ends.
type answerer struct {
	out     *bytes.Buffer
	answers []string
	shown   []string
}

func (a *answerer) Read(p []byte) (int, error) {
	a.shown = append(a.shown, a.out.String())
	if len(a.answers) == 0 {
		return 0, io.EOF
	}
	n := copy(p, a.answers[0])
	a.answers = a.answers[1:]
	return n, nil
}

// sizedConsole is termConsole for a terminal of w x h: the screen of the tests of the pages, which must fit the default window.
type sizedConsole struct {
	*termConsole
	w, h int
}

func (c sizedConsole) open(ctx context.Context, in io.Reader, version string, plain bool) (*ui.Screen, error) {
	env := ui.Env{StdinTTY: true, StdoutTTY: true, Plain: plain, Width: c.w, Height: c.h}
	s, err := ui.Open(ui.Options{Out: c.out, In: in, Context: ctx, Version: version, Mode: env.Mode(), Color: ui.ColorNone, Width: c.w, Height: c.h})
	if err != nil {
		return nil, err
	}
	c.opened, c.plain = append(c.opened, s), append(c.plain, plain)
	return s, nil
}

// screenRun is one command on a full screen of w x h whose terminal types the answers, one per question.
type screenRun struct {
	console sizedConsole
	in      *answerer
}

func newScreenRun(w, h int, answers ...string) *screenRun {
	buf := &bytes.Buffer{}
	c := sizedConsole{termConsole: newTermConsole(buf), w: w, h: h}
	return &screenRun{console: c, in: &answerer{out: buf, answers: answers}}
}

// on makes env talk to the screen of this run, on a terminal that cannot ask for a secret.
func (s *screenRun) on(env Env) Env {
	env.Term = Terminal{In: s.in, Out: s.console.out, IsTerminal: true, ReadPassword: noSecret}
	env.OpenScreen = s.console.open
	return env
}

func (s *screenRun) output() string { return s.console.out.String() }

// question is the frame while the i-th question waited, counting from 0.
func (s *screenRun) question(t *testing.T, i int) frameView {
	t.Helper()
	if i >= len(s.in.shown) {
		t.Fatalf("the command asked %d questions, want a question %d; the last frame:\n%s", len(s.in.shown), i, s.output())
	}
	return viewOf(t, s.in.shown[i], s.console.w, s.console.h)
}

// frameView is one frame of the screen as a person reads it.
type frameView struct {
	title, hint string
	steps       string   // the step column, one row per line
	rows        []string // the content column, without trailing spaces
}

// text is the content as one line of words, so that a sentence that wrapped reads as it was written, also inside a box: its edges are not
// words.
func (f frameView) text() string {
	var words []string
	for _, r := range f.rows {
		if strings.HasPrefix(r, "╭") || strings.HasPrefix(r, "╰") {
			continue
		}
		words = append(words, strings.Fields(strings.Trim(r, "│ "))...)
	}
	return strings.Join(words, " ")
}

// row is the first row of the content that contains want, "" when there is none.
func (f frameView) row(want string) string {
	for _, r := range f.rows {
		if strings.Contains(r, want) {
			return r
		}
	}
	return ""
}

// viewOf reads the last frame that was drawn in out, on a terminal of w x h.
func viewOf(t *testing.T, out string, w, h int) frameView {
	t.Helper()
	start := strings.LastIndex(out, esc+"[1;1H")
	if start < 0 {
		t.Fatal("no frame was drawn")
	}
	rows := rowAddress.Split(out[start:], -1)[1:] // rows[i] is terminal row i+1
	if len(rows) < h {
		t.Fatalf("the frame has %d rows, want %d", len(rows), h)
	}
	var v frameView
	for _, r := range rows[3 : h-3] { // the body: after the title bar and its rule, before the rule above the hint
		cells := []rune(r)
		v.steps += strings.TrimSpace(string(cells[1:1+stepsCol])) + "\n"
		v.rows = append(v.rows, strings.TrimRight(string(cells[1+stepsCol+1+1:len(cells)-2]), " "))
	}
	cells := []rune(rows[h-2])
	v.title, v.hint = strings.TrimSpace(rows[1]), strings.TrimSpace(string(cells[1:len(cells)-1]))
	return v
}

// trimmed is the row a page shows when it does not fit the window.
const trimmed = "enlarge the window to read the rest"

// mustSay fails once, with the page, when it lacks any of the sentences.
func mustSay(t *testing.T, v frameView, sentences ...string) {
	t.Helper()
	var missing []string
	for _, want := range sentences {
		if !strings.Contains(v.text(), want) {
			missing = append(missing, strconv.Quote(want))
		}
	}
	if len(missing) > 0 {
		t.Errorf("the page lacks %s:\n%s", strings.Join(missing, ", "), v.dump())
	}
}

// dump is the content as rows, without the blank ones at the end.
func (f frameView) dump() string { return strings.TrimRight(strings.Join(f.rows, "\n"), "\n") }

// screenArgs are the arguments that name this harness's files and its Podman connection, then the ones of the test.
func (h *harness) screenArgs(extra ...string) []string {
	return append([]string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--connection", "pm"}, extra...)
}
