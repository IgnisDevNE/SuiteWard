package ui

import (
	"context"
	"io"
	"slices"
	"testing"
	"time"
)

// A key that was typed before a question was drawn is not its answer. On a real terminal, key mode starts by dropping what the user typed
// while nothing was reading (the console queued it) and what the reader had already taken from the terminal and not used (a second Enter
// that came in the same read as the Enter of the line before): an Enter pressed twice must not answer the next multiple choice with its
// defaults. Scripted input goes through the same reader and keeps its keys.

func TestAQuestionTakesNoKeyThatWasTypedBeforeItOpened(t *testing.T) {
	for _, tt := range []struct {
		name     string
		terminal bool
	}{{"a terminal", true}, {"scripted input", false}} {
		t.Run(tt.name, func(t *testing.T) {
			pr, pw := io.Pipe()
			t.Cleanup(func() { _ = pw.Close() })
			keys := &fakeKeys{}
			o := fullOptions(ColorNone)
			o.Out, o.In, o.Version, o.KeyInput, o.InputIsTerminal = io.Discard, pr, testVersion, keys.toggle, tt.terminal
			s, err := Open(o)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })

			// The line ends with an Enter, and a second Enter comes in the same read.
			hidden := make(chan error, 1)
			go func() { _, err := s.HiddenLine(context.Background(), hiddenLabel, nil); hidden <- err }()
			go func() { _, _ = io.WriteString(pw, "pasted\r\r") }()
			select {
			case err := <-hidden:
				if err != nil {
					t.Fatalf("HiddenLine = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the hidden line did not end")
			}

			type answer struct {
				picked []int
				err    error
			}
			asked := make(chan answer, 1)
			go func() { picked, err := s.MultiSelect(repoQuestion(1, 0, 0)); asked <- answer{picked, err} }()
			select {
			case a := <-asked:
				if tt.terminal {
					t.Fatalf("the multiple choice took the Enter that was typed before it opened: %+v", a)
				}
				if a.err != nil || !slices.Equal(a.picked, []int{0}) {
					t.Fatalf("scripted input: MultiSelect = %+v, want the buffered Enter to pick the default", a)
				}
				return
			case <-time.After(200 * time.Millisecond):
				if !tt.terminal {
					t.Fatal("scripted input: the buffered Enter was dropped")
				}
			}
			go func() { _, _ = io.WriteString(pw, "\r") }()
			select {
			case a := <-asked:
				if a.err != nil || !slices.Equal(a.picked, []int{0}) {
					t.Fatalf("MultiSelect = %+v, want its own Enter to pick the default", a)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the multiple choice did not take its own Enter")
			}
		})
	}
}
