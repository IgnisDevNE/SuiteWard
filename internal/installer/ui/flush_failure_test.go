package ui

import (
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// Dropping the keys that were typed before a question is a convenience guard, not the safety of data: a console handle that cannot be flushed
// (one opened for reading only, whose mode can be read and not set) must never fail a question, a page or a purge that has removed everything.
// The first failed flush turns flushing off for the rest of the screen; the reader still drops what it holds unused.

var errFlushDenied = errors.New("access denied")

// deniedFlush opens a full screen on the keyboard of a test whose flush always fails, and counts the flushes that were tried.
func deniedFlush(t *testing.T) (*Screen, *typedInput, *atomic.Int32) {
	t.Helper()
	in := newTypedInput(t)
	flushes := &atomic.Int32{}
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Version, o.KeyInput, o.InputIsTerminal = io.Discard, in, testVersion, (&fakeKeys{}).toggle, true
	o.FlushInput = func() error { flushes.Add(1); return errFlushDenied }
	s, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, in, flushes
}

// within waits for the result of a question that runs in its own goroutine.
func within[T any](t *testing.T, got <-chan T) T {
	t.Helper()
	select {
	case v := <-got:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("the question did not end")
	}
	var none T
	return none
}

func TestAQuestionIsNotFailedByAFlushThatCannotBeDone(t *testing.T) {
	type outcome struct {
		line string
		err  error
	}
	for _, tt := range []struct {
		name  string
		typed string
		ask   func(*Screen) outcome
		want  string
	}{
		{"a typed question", "bob\n", func(s *Screen) outcome { line, err := s.Ask("Owner", "alice"); return outcome{line, err} }, "bob"},
		{"a prompt", "yes\n", func(s *Screen) outcome { line, err := s.Prompt("Continue? "); return outcome{line, err} }, "yes"},
		{"a secret", "", func(s *Screen) outcome {
			secret, err := s.Secret("Token", func() ([]byte, error) { return []byte("hush"), nil })
			return outcome{string(secret), err}
		}, "hush"},
		{"a page that waits for Enter", "\n", func(s *Screen) outcome { return outcome{err: s.Wait()} }, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, in, _ := deniedFlush(t)
			asked := make(chan outcome, 1)
			go func() { asked <- tt.ask(s) }()
			in.Type(tt.typed)
			if got := within(t, asked); got.err != nil || got.line != tt.want {
				t.Errorf("the question answered %q with error %v, want %q and no error", got.line, got.err, tt.want)
			}
		})
	}
}

// The first failure is the last try: the questions that follow neither flush nor fail.
func TestAFlushThatFailedIsNotTriedAgain(t *testing.T) {
	s, in, flushes := deniedFlush(t)
	for range 3 {
		asked := make(chan error, 1)
		go func() { _, err := s.Ask("Owner", "alice"); asked <- err }()
		in.Type("bob\n")
		if err := within(t, asked); err != nil {
			t.Fatalf("a question failed after a flush that cannot be done: %v", err)
		}
	}
	if n := flushes.Load(); n != 1 {
		t.Errorf("the flush that failed was tried %d times, want once", n)
	}
}

// What the reader holds unused is dropped without the terminal: a second Enter that came in the read of the first never answers the question
// that opens next, whether or not the terminal can be flushed.
func TestAFlushThatCannotBeDoneStillDropsWhatTheReaderHolds(t *testing.T) {
	s, in, _ := deniedFlush(t)
	in.Type("\n\n") // the Enter of the choice, and a second one that came in the same read
	if got, err := s.Select(selectLabel, testChoices, 0); err != nil || got != 0 {
		t.Fatalf("Select = %d, %v", got, err)
	}
	type outcome struct {
		line string
		err  error
	}
	asked := make(chan outcome, 1)
	go func() { line, err := s.Ask("Owner", "alice"); asked <- outcome{line, err} }()
	select {
	case got := <-asked:
		t.Fatalf("the question took the key that the reader held, or failed: answered %q with error %v", got.line, got.err)
	case <-time.After(200 * time.Millisecond):
	}
	in.Type("bob\n")
	if got := within(t, asked); got.err != nil || got.line != "bob" {
		t.Errorf("the question answered %q with error %v, want what was typed for it (bob)", got.line, got.err)
	}
}
