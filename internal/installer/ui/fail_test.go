package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// mustNotRead is an input that fails the test when it is read: a screen that does not wait never reads.
type mustNotRead struct{ t *testing.T }

func (r mustNotRead) Read([]byte) (int, error) {
	r.t.Error("the input was read")
	return 0, io.EOF
}

func squash(text string) string { return strings.Join(strings.Fields(text), "") }

// content is the content column of a frame with the step column, without its borders.
func content(f frame) string {
	var lines []string
	for _, r := range f.rows[3 : len(f.rows)-3] {
		cells := []rune(r)
		lines = append(lines, string(cells[1+stepsWidth+1+1:len(cells)-2]))
	}
	return strings.Join(lines, "\n")
}

func TestFailShowsTheErrorUnderThePageAndWaitsForEnter(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "\n")
	s.Steps("Confirm", "Remove")
	s.Say("This is the instance 1a2b3c4d.\n")
	s.Warn("Docker is also installed.\n")
	const message = "confirm the purge with --yes and --instance 1a2b3c4d: nothing can ask you to type the instance id here, because the input is not a terminal"
	if !s.Fail(message) {
		t.Fatal("Fail says that it showed nothing")
	}
	text := strings.Join(snapshot(s).rows, "\n")
	for _, want := range []string{
		"failed",    // the title bar
		"! Confirm", // the step that failed
		"This is the instance 1a2b3c4d.",
		"! Docker is also installed.", // the page it stopped on is still there
		"Enter to finish",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the frame lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "step 1 of 2") || strings.Contains(text, "Ctrl-C to quit") {
		t.Errorf("the title bar or the hint still describe a running command:\n%s", text)
	}
	if !strings.Contains(squash(content(snapshot(s))), squash("! "+message)) {
		t.Errorf("the error is not in the frame, wrapped to it and marked as a warning:\n%s", text)
	}

	mustClose(t, s)
	if got, want := afterLastLeave(out.String()), "This is the instance 1a2b3c4d.\n! Docker is also installed.\n"; got != want {
		t.Errorf("Close printed %q, want the page and not the error (the caller prints that): %q", got, want)
	}
}

func TestFailDoesNotWaitWhereThereIsNothingToWaitOn(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		o    Options
		prep func(s *Screen)
	}{
		{"plain mode", Options{Mode: ModePlain}, nil},
		{"an interrupt", Options{Mode: ModeFull, Color: ColorNone, Width: 80, Height: 24, Context: cancelled}, nil},
		{"a closed screen", fullOptions(ColorNone), func(s *Screen) { _ = s.Close() }},
		{"a suspended screen", fullOptions(ColorNone), func(s *Screen) { s.Suspend() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			tc.o.Out, tc.o.In = &out, mustNotRead{t}
			s, err := Open(tc.o)
			if err != nil {
				t.Fatal(err)
			}
			if tc.prep != nil {
				tc.prep(s)
			}
			before := out.String()
			if s.Fail("the command failed") {
				t.Error("Fail says that it showed the error")
			}
			if out.String() != before {
				t.Errorf("Fail wrote %q", strings.TrimPrefix(out.String(), before))
			}
		})
	}
}

// A wait that ends in an error (Ctrl-C at the page, a lost input) only ends the wait: the error that was shown is the command's own.
func TestFailKeepsAnAnswerWhenTheWaitFails(t *testing.T) {
	var out strings.Builder
	o := fullOptions(ColorNone)
	o.Out, o.In = &out, iotest.ErrReader(errors.New("input lost"))
	s, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Fail("the command failed") {
		t.Error("Fail says that it showed nothing, although the frame was drawn")
	}
	mustClose(t, s)
}

// What the error carries is text, never a command to the terminal.
func TestFailDrawsOnlyText(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "\n")
	s.Fail("bad \x1b[31mred\x1b[0m\ttext\x07")
	text := strings.Join(snapshot(s).rows, "\n")
	if strings.Contains(text, esc) || strings.Contains(text, "\x07") {
		t.Errorf("a control character reached the frame: %q", text)
	}
	if !strings.Contains(text, "! bad [31mred[0m text") {
		t.Errorf("the text is not drawn as text:\n%s", text)
	}
}

// A failure after a summary is shown on the page, with the steps, and not on the summary: the summary is the page of a success.
func TestFailAfterASummaryIsShownOnThePage(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "\n\n")
	s.Steps("Start", "Done")
	if err := s.Summary([]KV{{"Release", "v1"}}, nil); err != nil {
		t.Fatal(err)
	}
	s.Fail("the summary could not be written")
	text := strings.Join(snapshot(s).rows, "\n")
	if !strings.Contains(text, "failed") || !strings.Contains(text, "! the summary could not be written") || strings.Contains(text, "done") {
		t.Errorf("the failure is not shown on the page:\n%s", text)
	}
}

// A line that was pending (a question that was never answered) is a line of the page, which Close prints as it did.
func TestFailKeepsAPendingLine(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "\n")
	s.Say("Type the instance id 1a2b3c4d to remove it: ")
	s.Fail("the purge was not confirmed")
	mustClose(t, s)
	if got, want := afterLastLeave(out.String()), "Type the instance id 1a2b3c4d to remove it: \n"; got != want {
		t.Errorf("Close printed %q, want %q", got, want)
	}
}

// On a Windows console a Ctrl-C reaches the child process first, and the command that ran it returns the child's error a few
// milliseconds before the signal cancels the context. On a terminal Fail waits a moment for the signal, so that the command ends as
// interrupted (130) and no failure page is drawn for an error that the user caused.
func TestFailOnATerminalWaitsForTheSignalThatFollowsTheChildsError(t *testing.T) {
	old := interruptGrace
	interruptGrace = 2 * time.Second
	t.Cleanup(func() { interruptGrace = old })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	time.AfterFunc(20*time.Millisecond, cancel)
	var out strings.Builder
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Context, o.InputIsTerminal = &out, strings.NewReader("\n"), ctx, true
	s, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	before := out.String()
	start := time.Now()
	if s.Fail("exit status 0xc000013a") {
		t.Error("Fail says that it showed the error of a command that was interrupted")
	}
	if out.String() != before {
		t.Errorf("Fail drew %q for an interrupt", strings.TrimPrefix(out.String(), before))
	}
	if time.Since(start) > time.Second {
		t.Errorf("Fail waited %v for a signal that came after 20ms", time.Since(start))
	}
}

func TestFailOnATerminalDrawsAfterTheGraceWhenNoSignalFollows(t *testing.T) {
	old := interruptGrace
	interruptGrace = 50 * time.Millisecond
	t.Cleanup(func() { interruptGrace = old })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var out strings.Builder
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Context, o.InputIsTerminal = &out, strings.NewReader("\n"), ctx, true
	s, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if !s.Fail("the command failed") {
		t.Fatal("Fail says that it showed nothing: no signal followed the error")
	}
	if elapsed := time.Since(start); elapsed < interruptGrace {
		t.Errorf("Fail drew after %v, before the grace of %v for a signal had passed", elapsed, interruptGrace)
	}
	if rows := strings.Join(snapshot(s).rows, "\n"); !strings.Contains(rows, "! the command failed") {
		t.Errorf("the failure is not on the page:\n%s", rows)
	}
}

// Where the input is not a terminal, or the screen is plain, there is no console to abort a read first: Fail does not wait.
func TestFailDoesNotWaitForASignalWhereNoConsoleRacesIt(t *testing.T) {
	old := interruptGrace
	interruptGrace = 2 * time.Second
	t.Cleanup(func() { interruptGrace = old })
	for _, tc := range []struct {
		name     string
		mode     Mode
		terminal bool
	}{
		{"a pipe", ModeFull, false},
		{"a plain screen on a terminal", ModePlain, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			o := Options{Mode: tc.mode, Color: ColorNone, Width: 80, Height: 24, Out: io.Discard, In: strings.NewReader("\n"), Context: ctx, InputIsTerminal: tc.terminal}
			s, err := Open(o)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			s.Fail("the command failed")
			if time.Since(start) > time.Second {
				t.Errorf("Fail waited %v for a signal", time.Since(start))
			}
		})
	}
}
