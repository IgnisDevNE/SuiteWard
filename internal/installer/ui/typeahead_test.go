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

// openOnPipe opens a full screen whose input is a pipe, as a terminal (or not, as scripted input).
func openOnPipe(t *testing.T, terminal bool) (*Screen, *io.PipeWriter) {
	t.Helper()
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	keys := &fakeKeys{}
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Version, o.KeyInput, o.InputIsTerminal = io.Discard, pr, testVersion, keys.toggle, terminal
	s, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, pw
}

// readHiddenLineOfTwoEnters reads a hidden line that ends with an Enter, with a second Enter that comes in the same read.
func readHiddenLineOfTwoEnters(t *testing.T, s *Screen, pw *io.PipeWriter) {
	t.Helper()
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
}

type pick struct {
	picked []int
	err    error
}

// askMultiSelect opens a multiple choice, which an Enter picks the default of.
func askMultiSelect(s *Screen) <-chan pick {
	asked := make(chan pick, 1)
	go func() { picked, err := s.MultiSelect(repoQuestion(1, 0, 0)); asked <- pick{picked, err} }()
	return asked
}

func TestAQuestionTakesNoKeyThatWasTypedBeforeItOpened(t *testing.T) {
	s, pw := openOnPipe(t, true)
	readHiddenLineOfTwoEnters(t, s, pw)
	asked := askMultiSelect(s)
	select {
	case a := <-asked:
		t.Fatalf("the multiple choice took the Enter that was typed before it opened: %+v", a)
	case <-time.After(200 * time.Millisecond):
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
}

func TestScriptedInputKeepsTheKeysItWasGiven(t *testing.T) {
	s, pw := openOnPipe(t, false)
	readHiddenLineOfTwoEnters(t, s, pw)
	select {
	case a := <-askMultiSelect(s):
		if a.err != nil || !slices.Equal(a.picked, []int{0}) {
			t.Fatalf("MultiSelect = %+v, want the buffered Enter to pick the default", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the buffered Enter of a scripted input was dropped")
	}
}
