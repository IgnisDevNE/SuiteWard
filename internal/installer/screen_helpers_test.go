package installer

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

const (
	esc      = "\x1b"
	enterAlt = esc + "[?1049h"
	leaveAlt = esc + "[?1049l"
)

// termConsole is the terminal of a test: the console that ui.OpenOS would open on a real one, over a buffer. A run on it is a run on a
// terminal of 100x30 where both stdin and stdout are terminals, so only --plain keeps it from drawing.
type termConsole struct {
	out    *bytes.Buffer
	opened []*ui.Screen
	plain  []bool // what each open was asked for
}

func newTermConsole(out *bytes.Buffer) *termConsole { return &termConsole{out: out} }

func (c *termConsole) open(ctx context.Context, in io.Reader, version string, plain bool) (*ui.Screen, error) {
	env := ui.Env{StdinTTY: true, StdoutTTY: true, Plain: plain, Width: 100, Height: 30}
	s, err := ui.Open(ui.Options{Out: c.out, In: in, Context: ctx, Version: version, Mode: env.Mode(), Color: ui.ColorNone, Width: 100, Height: 30})
	if err != nil {
		return nil, err
	}
	c.opened, c.plain = append(c.opened, s), append(c.plain, plain)
	return s, nil
}

// closed says that exactly one screen was opened and it has been closed again.
func (c *termConsole) closed() bool { return len(c.opened) == 1 && !c.opened[0].FullScreen() }

// tail is what was printed after the screen left the alternate screen for good: what stays in the scrollback.
func (c *termConsole) tail() string {
	s := c.out.String()
	i := strings.LastIndex(s, leaveAlt)
	if i < 0 {
		return ""
	}
	return s[i+len(leaveAlt):]
}

var stepCounter = regexp.MustCompile(`step (\d+) of (\d+)`)

// steps checks that the title bar went through steps 1 to len(names) of len(names), in order, and that each name was drawn.
func (c *termConsole) steps(t *testing.T, names ...string) {
	t.Helper()
	var seen []int
	for _, m := range stepCounter.FindAllStringSubmatch(c.out.String(), -1) {
		n, _ := strconv.Atoi(m[1])
		if of, _ := strconv.Atoi(m[2]); of != len(names) {
			t.Fatalf("the title bar says %q, want %d steps %v", m[0], len(names), names)
		}
		if len(seen) == 0 || seen[len(seen)-1] != n {
			seen = append(seen, n)
		}
	}
	var want []int
	for i := range names {
		want = append(want, i+1)
	}
	if !slices.Equal(seen, want) {
		t.Errorf("the steps drawn were %v, want %v (%v)", seen, want, names)
	}
	for _, name := range names {
		if !strings.Contains(c.out.String(), name) {
			t.Errorf("the step %q was never drawn", name)
		}
	}
}

// noRepeats fails when a line shows twice in text: Close prints what must stay once, whether it was kept, warned or summarized.
func noRepeats(t *testing.T, text string) {
	t.Helper()
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if seen[line] {
			t.Errorf("the line %q is printed twice after Close:\n%s", line, text)
		}
		seen[line] = true
	}
}

// countingReader serves its data once and counts the reads.
type countingReader struct {
	data  string
	reads int
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads++
	if r.data == "" {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// normalize hides what differs between two runs on two machines of a test: the directories and the ports.
func normalize(text string, replace ...string) string {
	for i := 0; i+1 < len(replace); i += 2 {
		text = strings.ReplaceAll(text, replace[i], replace[i+1])
	}
	return text
}
