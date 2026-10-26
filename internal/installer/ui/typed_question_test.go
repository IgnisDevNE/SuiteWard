package ui

import (
	"io"
	"testing"
	"time"
)

// No key typed before a question is drawn answers it, a typed question included: on a terminal every question starts by dropping what the
// terminal queued (Options.FlushInput) and what the reader holds unused. A summary or a page that waits for Enter is such a question too.
// Scripted input is not a terminal and keeps its keys.

// typedTerminal opens a full screen on the keyboard of a test, with a key mode and a flush of what was typed ahead, as a terminal (or not).
func typedTerminal(t *testing.T, terminal bool) (*Screen, *typedInput) {
	t.Helper()
	in := newTypedInput(t)
	keys := &fakeKeys{}
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Version, o.KeyInput, o.FlushInput, o.InputIsTerminal = io.Discard, in, testVersion, keys.toggle, in.Flush, terminal
	s, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, in
}

// owner asks the question that follows a choice in the install, the one of the owner, which an empty answer answers with its default.
func owner(s *Screen) <-chan string {
	asked := make(chan string, 1)
	go func() {
		line, err := s.Ask("Owner", "alice")
		if err != nil {
			line = err.Error()
		}
		asked <- line
	}()
	return asked
}

func TestATypedQuestionTakesNoKeyThatWasTypedBeforeItOpened(t *testing.T) {
	for _, tt := range []struct{ name, before, after string }{
		// The Enter of the choice, and a second one that the terminal queued while nothing read. A line typed at a terminal ends with a line
		// feed in cooked mode, which is what a typed question reads up to.
		{"a key the terminal queued while nothing read", "\n", "\n"},
		// The two Enters came in one read: the reader took the second one with the first.
		{"a key the reader took with the Enter before it", "\n\n", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, in := typedTerminal(t, true)
			in.Type(tt.before)
			if got, err := s.Select(selectLabel, testChoices, 0); err != nil || got != 0 {
				t.Fatalf("Select = %d, %v", got, err)
			}
			in.Type(tt.after)
			asked := owner(s)
			select {
			case line := <-asked:
				t.Fatalf("the question took a key that was typed before it opened, and answered %q", line)
			case <-time.After(200 * time.Millisecond):
			}
			in.Type("bob\n")
			select {
			case line := <-asked:
				if line != "bob" {
					t.Errorf("the question answered %q, want what was typed for it (bob)", line)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the question did not take its own answer")
			}
		})
	}
}

func TestAPageThatWaitsForEnterTakesNoKeyThatWasTypedBeforeItOpened(t *testing.T) {
	s, in := typedTerminal(t, true)
	in.Type("\n") // typed while nothing read
	waited := make(chan error, 1)
	go func() { waited <- s.Wait() }()
	select {
	case err := <-waited:
		t.Fatalf("the page ended with a key that was typed before it opened: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	in.Type("\n")
	select {
	case err := <-waited:
		if err != nil {
			t.Errorf("Wait = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the page did not take its own Enter")
	}
}

func TestScriptedInputKeepsTheAnswersItWasGivenToTypedQuestions(t *testing.T) {
	s, in := typedTerminal(t, false)
	in.Type("\n\n") // the choice and the owner question, answered at once
	if got, err := s.Select(selectLabel, testChoices, 0); err != nil || got != 0 {
		t.Fatalf("Select = %d, %v", got, err)
	}
	select {
	case line := <-owner(s):
		if line != "alice" {
			t.Errorf("the question answered %q, want its default (alice) from the Enter it was given", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the answer of a scripted input was dropped")
	}
	if in.flushes != 0 {
		t.Errorf("scripted input was flushed %d times: it is not a terminal", in.flushes)
	}
}
