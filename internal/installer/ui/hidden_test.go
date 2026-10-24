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

// HiddenLine reads a line that is not shown, as the code a user pastes: with the terminal in key mode (no echo, no line editing, the
// signals kept) and the context's read, so that Ctrl-C and a deadline end it. Nothing that is typed reaches the output.

const hiddenLabel = "Paste the address"

func TestHiddenLineReadsUntilEnterAndShowsNothingOfIt(t *testing.T) {
	keys := &fakeKeys{}
	s, out := openKeyScreen(t, "s3cret-text\r\n", keys)
	line, err := s.HiddenLine(context.Background(), hiddenLabel, nil)
	if err != nil || string(line) != "s3cret-text" {
		t.Fatalf("HiddenLine = %q, %v", line, err)
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Errorf("the typed text reached the output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), hiddenLabel+" (input is hidden): ") {
		t.Errorf("the question was not asked:\n%s", out.String())
	}
	keys.balanced(t, 1)
}

func TestHiddenLineEditsTheLineAsAKeyboardDoes(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"plain", "abc\r", "abc"},
		{"LF ends it too", "abc\n", "abc"},
		{"CR LF is one Enter", "abc\r\n", "abc"},
		{"backspace, DEL", "abd\x7fc\r", "abc"},
		{"backspace, BS", "abc\x08\x08x\r", "ax"},
		{"backspace takes a whole character", "é\x7f\r", ""},
		{"backspace on nothing", "\x7f\x7fa\r", "a"},
		{"Ctrl-U clears the line", "abc\x15de\r", "de"},
		{"an arrow is no text", "\x1b[Aab\x1b[B\r", "ab"},
		{"a control character is no text", "a\tb\r", "ab"},
		{"the last line may lack its newline", "ab", "ab"},
		{"an empty line is an empty answer", "\r", ""},
		{"a long paste", strings.Repeat("x", 3000) + "\r", strings.Repeat("x", 3000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := &fakeKeys{}
			s, _ := openKeyScreen(t, tt.input, keys)
			line, err := s.HiddenLine(context.Background(), hiddenLabel, nil)
			if err != nil || string(line) != tt.want {
				t.Fatalf("HiddenLine(%q) = %q, %v, want %q", tt.input, line, err, tt.want)
			}
			keys.balanced(t, 1)
		})
	}
}

func TestHiddenLineLeavesTheNextQuestionItsInput(t *testing.T) {
	keys := &fakeKeys{}
	s, _ := openKeyScreen(t, "first\r\nsecond\n", keys)
	if line, err := s.HiddenLine(context.Background(), hiddenLabel, nil); err != nil || string(line) != "first" {
		t.Fatalf("HiddenLine = %q, %v", line, err)
	}
	if line, err := s.Prompt("Next: "); err != nil || line != "second" {
		t.Fatalf("the next question got %q, %v", line, err)
	}
}

func TestHiddenLineThatLosesItsInputPutsTheConsoleBack(t *testing.T) {
	keys := &fakeKeys{}
	s, _ := openKeyScreen(t, "", keys)
	if _, err := s.HiddenLine(context.Background(), hiddenLabel, nil); !errors.Is(err, io.EOF) {
		t.Fatalf("HiddenLine = %v, want the end of the input", err)
	}
	keys.balanced(t, 1)
}

func TestHiddenLineEndsWithTheScreensContextAndPutsTheConsoleBack(t *testing.T) {
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
	go func() { _, err := s.HiddenLine(context.Background(), hiddenLabel, nil); done <- err }()
	select {
	case <-in.reading:
	case <-time.After(2 * time.Second):
		t.Fatal("HiddenLine never read")
	}
	typed := make(chan struct{})
	go func() { _, _ = io.WriteString(pw, "half-typed-secret"); close(typed) }() // the user has typed, and not yet pressed Enter
	select {
	case <-typed:
	case <-time.After(2 * time.Second):
		t.Fatal("the read did not take what was typed")
	}
	cancel() // Ctrl-C: the signal cancels the context
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("HiddenLine = %v, want the context's error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HiddenLine did not end with the screen's context: a hidden read that cannot be interrupted holds the run")
	}
	keys.balanced(t, 1)
	mustClose(t, s)
	if strings.Contains(out.String(), "half-typed") {
		t.Errorf("what was typed reached the output:\n%s", out.String())
	}
}

// The deadline of the caller (the wait for GitHub's callback) ends the read too, with its own error.
func TestHiddenLineEndsWithTheContextOfTheCall(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	keys := &fakeKeys{}
	var out bytes.Buffer
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Version, o.KeyInput = &out, pr, testVersion, keys.toggle
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.HiddenLine(ctx, hiddenLabel, nil); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("HiddenLine = %v, want the deadline of the call", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HiddenLine did not end with the deadline of its call")
	}
	keys.balanced(t, 1)
}

func TestHiddenLineWithADoneContextReadsNothingAndLeavesTheConsoleAlone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	keys := &fakeKeys{}
	in := &readCounter{}
	o := fullOptions(ColorNone)
	o.Out, o.In, o.KeyInput = io.Discard, in, keys.toggle
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.HiddenLine(ctx, hiddenLabel, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("HiddenLine = %v, want the context's error", err)
	}
	if in.reads != 0 {
		t.Errorf("the input was read %d times after the context was done", in.reads)
	}
	keys.balanced(t, 0)
}

func TestHiddenLineReportsACtrlCThatArrivesAsACharacter(t *testing.T) {
	old := interruptGrace
	interruptGrace = 20 * time.Millisecond
	t.Cleanup(func() { interruptGrace = old })
	keys := &fakeKeys{}
	s, _ := openKeyScreen(t, "abc\x03", keys)
	_, err := s.HiddenLine(context.Background(), hiddenLabel, nil)
	if err == nil || !strings.Contains(err.Error(), "Ctrl-C") || errors.Is(err, context.Canceled) {
		t.Fatalf("HiddenLine = %v, want an error that says Ctrl-C ended it", err)
	}
	keys.balanced(t, 1)
}

func TestHiddenLineReportsAConsoleThatCannotBePutBack(t *testing.T) {
	broken := errors.New("the console went away")
	keys := &fakeKeys{restoreErr: broken}
	s, _ := openKeyScreen(t, "ab\r", keys)
	if _, err := s.HiddenLine(context.Background(), hiddenLabel, nil); !errors.Is(err, broken) {
		t.Fatalf("HiddenLine = %v, want the failure to restore the console", err)
	}
}

// A terminal that cannot be put in key mode, or a screen that was opened without a way to, reads through the caller's own no-echo
// reader, which cannot be interrupted: the question is asked all the same.
func TestHiddenLineFallsBackToTheCallersReader(t *testing.T) {
	read := func() ([]byte, error) { return []byte("from-the-fallback"), nil }
	t.Run("no way to enter key mode", func(t *testing.T) {
		s, out := openScreen(t, fullOptions(ColorNone), "")
		line, err := s.HiddenLine(context.Background(), hiddenLabel, read)
		if err != nil || string(line) != "from-the-fallback" {
			t.Fatalf("HiddenLine = %q, %v", line, err)
		}
		if !strings.Contains(out.String(), hiddenLabel+" (input is hidden): ") {
			t.Errorf("the question was not asked:\n%s", out.String())
		}
	})
	t.Run("a terminal that refuses key mode", func(t *testing.T) {
		keys := &fakeKeys{enableErr: errors.New("not a terminal")}
		s, _ := openKeyScreen(t, "", keys)
		if line, err := s.HiddenLine(context.Background(), hiddenLabel, read); err != nil || string(line) != "from-the-fallback" {
			t.Fatalf("HiddenLine = %q, %v", line, err)
		}
	})
	t.Run("no fallback either", func(t *testing.T) {
		s, _ := openScreen(t, fullOptions(ColorNone), "")
		if _, err := s.HiddenLine(context.Background(), hiddenLabel, nil); err == nil {
			t.Fatal("a hidden question with no way to hide the answer was asked")
		}
	})
}

// A plain screen on a terminal hides the line the same way (a plain run is still at a terminal that echoes), and asks Select and
// MultiSelect by number as it always did.
func TestHiddenLineOnAPlainScreenUsesTheKeyModeAndSelectStaysTyped(t *testing.T) {
	keys := &fakeKeys{}
	o := plainOptions()
	o.KeyInput = keys.toggle
	s, out := openScreen(t, o, "typed-secret\r\n2\n")
	line, err := s.HiddenLine(context.Background(), hiddenLabel, nil)
	if err != nil || string(line) != "typed-secret" {
		t.Fatalf("HiddenLine = %q, %v", line, err)
	}
	keys.balanced(t, 1)
	if want := hiddenLabel + " (input is hidden): \n"; out.String() != want {
		t.Errorf("a plain screen wrote %q, want %q", out.String(), want)
	}
	if got, err := s.Select(selectLabel, testChoices, 0); err != nil || got != 1 {
		t.Fatalf("Select = %d, %v", got, err)
	}
	keys.balanced(t, 1) // Select asked by number: the console was not touched again
}

// A hidden line that its call gave up on (the deadline of the wait for GitHub) leaves a read pending in its goroutine, which cannot be
// ended. The next read of the screen must not start beside it: the two would share the one buffered input, and the Enter of the user
// would go to the read that was given up. So the next question waits for that read to end, and what the read took is dropped; and a wait
// for Enter (the page of a failure) counts the Enter that ends it as its own.

// givenUpHiddenLine opens a screen over a pipe and has a HiddenLine give up on its 50ms call context, which leaves its read pending.
func givenUpHiddenLine(t *testing.T) (*Screen, *io.PipeWriter) {
	t.Helper()
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	keys := &fakeKeys{}
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Version, o.KeyInput = io.Discard, pr, testVersion, keys.toggle
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.HiddenLine(ctx, hiddenLabel, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("HiddenLine = %v, want the deadline of the call", err)
	}
	return s, pw
}

func TestAWaitForEnterTakesTheEnterThatEndsAHiddenLineThatWasGivenUp(t *testing.T) {
	s, pw := givenUpHiddenLine(t)
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	time.Sleep(50 * time.Millisecond) // the wait is open
	go func() { _, _ = io.WriteString(pw, "pasted\r\n") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("one Enter did not end the wait: it went to the read that was given up")
	}
}

func TestTheQuestionAfterAHiddenLineThatWasGivenUpGetsItsOwnInput(t *testing.T) {
	s, pw := givenUpHiddenLine(t)
	type answer struct {
		line string
		err  error
	}
	done := make(chan answer, 1)
	go func() { line, err := s.Prompt("Next: "); done <- answer{line, err} }()
	time.Sleep(50 * time.Millisecond) // the question is open
	go func() { _, _ = io.WriteString(pw, "taken\r\nnext\n") }()
	select {
	case a := <-done:
		if a.err != nil || a.line != "next" {
			t.Fatalf("Prompt = %q, %v, want the line after the one the hidden read took", a.line, a.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the question never got its input: the read that was given up still holds it")
	}
}
