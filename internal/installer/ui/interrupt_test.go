package ui

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// On a Windows console, Ctrl-C makes the pending read return before the signal has cancelled the context, so the prompt saw the end of
// the input and the command reported it as an error and exited 1, not as an interrupt.

func endOfInput() io.Reader { return readerFunc(func([]byte) (int, error) { return 0, io.EOF }) }

// openFull opens a full screen on in that cancels its context after after, and reports whether it is on a terminal.
func openFull(t *testing.T, in io.Reader, terminal bool, after time.Duration) *Screen {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if after > 0 {
		time.AfterFunc(after, cancel)
	}
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Context, o.InputIsTerminal = io.Discard, in, ctx, terminal
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestAReadThatEndsOnATerminalIsAnInterruptWhenTheContextIsDoneSoon(t *testing.T) {
	asks := map[string]func(*Screen) error{
		"Ask":    func(s *Screen) error { _, err := s.Ask("Name", "x"); return err },
		"Prompt": func(s *Screen) error { _, err := s.Prompt("Name: "); return err },
		"Choose": func(s *Screen) error { _, err := s.Choose("Q", testChoices, 0); return err },
		"Wait":   func(s *Screen) error { return s.Wait() },
	}
	for name, ask := range asks {
		t.Run(name, func(t *testing.T) {
			s := openFull(t, endOfInput(), true, 10*time.Millisecond)
			if err := ask(s); !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled: the end of a terminal's input is Ctrl-C when the context is done a moment later", err)
			}
		})
	}
}

func TestTheEndOfAnInputThatIsNotATerminalIsTheEndOfTheInput(t *testing.T) {
	s := openFull(t, endOfInput(), false, 10*time.Millisecond)
	if _, err := s.Ask("Name", "x"); !errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		t.Fatalf("Ask = %v, want the end of the input: a pipe that ends is not an interrupt", err)
	}
}

func TestTheEndOfATerminalsInputIsBelievedWhenTheContextStaysAlive(t *testing.T) {
	old := interruptGrace
	interruptGrace = 20 * time.Millisecond
	t.Cleanup(func() { interruptGrace = old })
	s := openFull(t, endOfInput(), true, 0)
	if _, err := s.Ask("Name", "x"); !errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		t.Fatalf("Ask = %v, want the end of the input: Ctrl-Z at a console is a real end", err)
	}
}
