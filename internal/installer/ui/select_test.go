package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

const selectLabel = "How should SuiteWard run?"

// fakeKeys is the per-key toggle of a test: it counts how often the console was put in key mode and back.
type fakeKeys struct {
	enabled, restored     int
	enableErr, restoreErr error
}

func (f *fakeKeys) toggle() (func() error, error) {
	if f.enableErr != nil {
		return nil, f.enableErr
	}
	f.enabled++
	return func() error { f.restored++; return f.restoreErr }, nil
}

// balanced says that the console was put in key mode and back as many times as the test expects.
func (f *fakeKeys) balanced(t *testing.T, want int) {
	t.Helper()
	if f.enabled != want || f.restored != want {
		t.Errorf("the console was put in key mode %d times and back %d times, want %d each", f.enabled, f.restored, want)
	}
}

// openKeyScreen opens a full screen over input whose terminal has the per-key mode of keys.
func openKeyScreen(t *testing.T, input string, keys *fakeKeys) (*Screen, *bytes.Buffer) {
	t.Helper()
	o := fullOptions(ColorNone)
	o.KeyInput = keys.toggle
	return openScreen(t, o, input)
}

func TestSelectMovesWithTheArrowsAndPicksWithEnter(t *testing.T) {
	tests := []struct {
		name  string
		input string
		def   int
		want  int
	}{
		{"Enter takes the default", "\r", 0, 0},
		{"Enter takes a default that is not the first", "\r", 1, 1},
		{"down, Enter", "\x1b[B\r", 0, 1},
		{"down, up, Enter", "\x1b[B\x1b[A\r", 0, 0},
		{"down in the SS3 form", "\x1bOB\r", 0, 1},
		{"down at the last row goes round to the first", "\x1b[B\x1b[B\r", 0, 0},
		{"up at the first row goes round to the last", "\x1b[A\r", 0, 1},
		{"space, Esc and letters change nothing", "x \x1b\x1b[C\r", 1, 1},
		{"LF is Enter", "\x1b[B\n", 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := &fakeKeys{}
			s, _ := openKeyScreen(t, tt.input, keys)
			got, err := s.Select(selectLabel, testChoices, tt.def)
			if err != nil || got != tt.want {
				t.Fatalf("Select = %d, %v; want %d", got, err, tt.want)
			}
			keys.balanced(t, 1)
		})
	}
}

// The page shows the list with the highlighted row while it is open, and the answer on the question's line once it is picked, as Ask
// leaves its answer.
func TestSelectShowsTheHighlightAndThenTheAnswer(t *testing.T) {
	keys := &fakeKeys{}
	s, out := openKeyScreen(t, "\x1b[B\r", keys)
	if got, err := s.Select(selectLabel, testChoices, 0); err != nil || got != 1 {
		t.Fatalf("Select = %d, %v; want 1", got, err)
	}
	frames := strings.Split(out.String(), esc+"[1;1H")[1:]
	has := func(frame string, texts ...string) bool {
		for _, text := range texts {
			if !strings.Contains(frame, text) {
				return false
			}
		}
		return true
	}
	first, second := -1, -1
	for i, f := range frames {
		switch {
		case first < 0 && has(f, "● co-located   alongside the agent, on this PC (default)", "○ isolated     on a separate host"):
			first = i
		case first >= 0 && second < 0 && has(f, "○ co-located   alongside the agent, on this PC (default)", "● isolated     on a separate host"):
			second = i
		}
	}
	if first < 0 || second < 0 {
		t.Fatalf("the list was not drawn with the first row highlighted and then the second (frames at %d and %d):\n%s", first, second, out.String())
	}
	for _, f := range frames[first : second+1] {
		if !has(f, "to choose", "Enter to confirm", "Ctrl-C to quit") {
			t.Errorf("a frame of the open list lacks the hint:\n%s", f)
		}
		if !strings.HasSuffix(f, esc+"[?25l") {
			t.Errorf("a frame of the open list leaves the cursor shown: %q", f[len(f)-20:])
		}
	}
	last := frames[len(frames)-1]
	if !has(last, selectLabel+": isolated") || strings.ContainsAny(last, "●○") || strings.Contains(last, "to choose") {
		t.Errorf("the last frame is not the question with its answer:\n%s", last)
	}
	mustClose(t, s)
	if got, want := afterLastLeave(out.String()), selectLabel+": isolated\n"; got != want {
		t.Errorf("the normal screen received %q, want %q", got, want)
	}
}

func TestSelectTakesNoMoreOfTheInputThanItsKeys(t *testing.T) {
	keys := &fakeKeys{}
	s, _ := openKeyScreen(t, "\x1b[B\r\nnext answer\n", keys) // the CRLF of an Enter is one press, and what is typed after it is the next question's
	if got, err := s.Select(selectLabel, testChoices, 0); err != nil || got != 1 {
		t.Fatalf("Select = %d, %v; want 1", got, err)
	}
	if line, err := s.Prompt("Next: "); err != nil || line != "next answer" {
		t.Fatalf("the next prompt read %q, %v; want the line typed after the Enter", line, err)
	}
}

func TestSelectEndsWithTheContextAndPutsTheConsoleBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	in := &signalReader{r: pr, reading: make(chan struct{})}
	keys := &fakeKeys{}
	var out bytes.Buffer
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Version, o.Context, o.KeyInput = &out, in, testVersion, ctx, keys.toggle
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Select(selectLabel, testChoices, 0); done <- err }()
	select {
	case <-in.reading: // the list is on the page and the screen waits for a key
	case <-time.After(2 * time.Second):
		t.Fatal("Select never read")
	}
	cancel() // Ctrl-C: the signal cancels the context, and the read that is pending is abandoned
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Select = %v, want the context's error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Select did not end with its context")
	}
	keys.balanced(t, 1) // the abandoned read does not hold the console in key mode
	mustClose(t, s)
	want := selectLabel + "\n1) co-located   alongside the agent, on this PC (default)\n2) isolated     on a separate host\n"
	if got := afterLastLeave(out.String()); got != want {
		t.Errorf("the normal screen received %q, want the unanswered question %q", got, want)
	}
}

func TestSelectWithADoneContextReadsNothingAndLeavesTheConsoleAlone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	keys := &fakeKeys{}
	in := &readCounter{}
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Context, o.KeyInput = io.Discard, in, ctx, keys.toggle
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Select(selectLabel, testChoices, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("Select = %v, want the context's error", err)
	}
	if in.reads != 0 {
		t.Errorf("the input was read %d times after the context was done", in.reads)
	}
	keys.balanced(t, 0)
}

type readCounter struct{ reads int }

func (r *readCounter) Read([]byte) (int, error) { r.reads++; return 0, io.EOF }

func TestSelectThatLosesItsInputPutsTheConsoleBack(t *testing.T) {
	keys := &fakeKeys{}
	s, _ := openKeyScreen(t, "\x1b[B", keys) // the input ends before Enter
	if _, err := s.Select(selectLabel, testChoices, 0); !errors.Is(err, io.EOF) {
		t.Fatalf("Select = %v, want the end of the input", err)
	}
	keys.balanced(t, 1)
}

// With ISIG, or the console's processed input, kept, Ctrl-C is a signal and never a character; a console that delivers it as one, and
// raises no signal, is reported as an error, since there is nothing for the context to follow.
func TestSelectReportsACtrlCThatArrivesAsACharacter(t *testing.T) {
	old := interruptGrace
	interruptGrace = 20 * time.Millisecond
	t.Cleanup(func() { interruptGrace = old })
	keys := &fakeKeys{}
	s, _ := openKeyScreen(t, "\x03", keys)
	_, err := s.Select(selectLabel, testChoices, 0)
	if err == nil || !strings.Contains(err.Error(), "Ctrl-C") || errors.Is(err, context.Canceled) {
		t.Fatalf("Select = %v, want an error that says Ctrl-C ended it", err)
	}
	keys.balanced(t, 1)
}

// A host may put the character in the input a moment before it raises the signal, as a Windows console aborts a read before it does:
// the signal that follows is the interrupt, and the command ends as one.
func TestSelectEndsAsAnInterruptWhenTheSignalFollowsACtrlCCharacter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	time.AfterFunc(20*time.Millisecond, cancel)
	keys := &fakeKeys{}
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Context, o.KeyInput = io.Discard, strings.NewReader("\x03"), ctx, keys.toggle
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Select(selectLabel, testChoices, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("Select = %v, want the context's error: the signal came right after the character", err)
	}
	keys.balanced(t, 1)
}

func TestSelectReportsAConsoleThatCannotBePutBack(t *testing.T) {
	broken := errors.New("the console went away")
	keys := &fakeKeys{restoreErr: broken}
	s, _ := openKeyScreen(t, "\r", keys)
	if _, err := s.Select(selectLabel, testChoices, 0); !errors.Is(err, broken) {
		t.Fatalf("Select = %v, want the failure to restore the console: a terminal left in key mode must be reported", err)
	}
}

// A console that cannot be put in key mode, or has no way to be, keeps the numbered question it always had, byte for byte.
func TestSelectAsksByNumberWhenKeyInputIsNotAvailable(t *testing.T) {
	choose := func() (int, string) {
		s, out := openScreen(t, fullOptions(ColorNone), "2\n")
		got, err := s.Choose(selectLabel, testChoices, 0)
		if err != nil {
			t.Fatalf("Choose: %v", err)
		}
		return got, out.String()
	}
	wantIdx, wantOut := choose()
	if wantIdx != 1 {
		t.Fatalf("Choose = %d, want 1", wantIdx)
	}
	for name, open := range map[string]func() (*Screen, *bytes.Buffer, *fakeKeys){
		"the toggle fails": func() (*Screen, *bytes.Buffer, *fakeKeys) {
			keys := &fakeKeys{enableErr: errors.New("not a console")}
			s, out := openKeyScreen(t, "2\n", keys)
			return s, out, keys
		},
		"there is no toggle": func() (*Screen, *bytes.Buffer, *fakeKeys) {
			s, out := openScreen(t, fullOptions(ColorNone), "2\n")
			return s, out, &fakeKeys{}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, out, keys := open()
			if got, err := s.Select(selectLabel, testChoices, 0); err != nil || got != wantIdx {
				t.Fatalf("Select = %d, %v; want %d", got, err, wantIdx)
			}
			if out.String() != wantOut {
				t.Errorf("the numbered fallback differs from Choose.\ngot:\n%q\nwant:\n%q", out.String(), wantOut)
			}
			keys.balanced(t, 0)
		})
	}
}

func TestSelectInPlainModeIsChoose(t *testing.T) {
	ask := func(pick func(*Screen) (int, error)) (int, string) {
		keys := &fakeKeys{}
		o := plainOptions()
		o.KeyInput = keys.toggle
		s, out := openScreen(t, o, "2\n")
		got, err := pick(s)
		if err != nil {
			t.Fatalf("ask: %v", err)
		}
		keys.balanced(t, 0)
		return got, out.String()
	}
	wantIdx, wantOut := ask(func(s *Screen) (int, error) { return s.Choose(selectLabel, testChoices, 0) })
	gotIdx, gotOut := ask(func(s *Screen) (int, error) { return s.Select(selectLabel, testChoices, 0) })
	if gotIdx != wantIdx || gotIdx != 1 || gotOut != wantOut {
		t.Fatalf("plain Select = %d, %q; want what Choose gives, %d, %q", gotIdx, gotOut, wantIdx, wantOut)
	}
	if strings.Contains(gotOut, esc) {
		t.Errorf("plain output holds an escape sequence: %q", gotOut)
	}
}

func TestSelectRefusesADefaultOutsideItsChoices(t *testing.T) {
	keys := &fakeKeys{}
	s, _ := openKeyScreen(t, "\r", keys)
	for _, def := range []int{-2, 2} {
		if _, err := s.Select(selectLabel, testChoices, def); err == nil {
			t.Errorf("Select accepted the default %d of 2 choices", def)
		}
	}
	if _, err := s.Select(selectLabel, nil, 0); err == nil {
		t.Error("Select accepted no choices")
	}
	keys.balanced(t, 0)
}
