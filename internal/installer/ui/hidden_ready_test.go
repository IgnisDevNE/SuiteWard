package ui

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"
)

// A terminal that can be waited on (Options.InputReady) lets the context of a HiddenLine end its read for good: the wait for a key ends, and
// no read is left on the input, so the next question has it to itself and nothing it types goes to a read that nobody wants.

// typedInput is the keyboard of a test: what is typed waits in it until a read takes it, and Ready tells that a key is there without taking it.
type typedInput struct {
	mu     sync.Mutex
	buf    []byte
	closed bool
	wake   chan struct{} // closed, and replaced, whenever something is typed or the keyboard is closed
}

func newTypedInput(t *testing.T) *typedInput {
	t.Helper()
	in := &typedInput{wake: make(chan struct{})}
	t.Cleanup(in.close)
	return in
}

func (in *typedInput) change() { close(in.wake); in.wake = make(chan struct{}) } // the caller holds the lock

func (in *typedInput) Type(text string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.buf = append(in.buf, text...)
	in.change()
}

func (in *typedInput) close() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.closed = true
	in.change()
}

// wait returns when something is typed or the keyboard is closed, without taking what is typed.
func (in *typedInput) wait(done <-chan struct{}) error {
	for {
		in.mu.Lock()
		ready, wake := len(in.buf) > 0 || in.closed, in.wake
		in.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-wake:
		case <-done:
			return context.Canceled
		}
	}
}

func (in *typedInput) Read(p []byte) (int, error) {
	if err := in.wait(nil); err != nil {
		return 0, err
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if len(in.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, in.buf)
	in.buf = in.buf[n:]
	return n, nil
}

// Ready is Options.InputReady of the keyboard.
func (in *typedInput) Ready(ctx context.Context) error {
	if err := in.wait(ctx.Done()); err != nil {
		return ctx.Err()
	}
	return nil
}

// readyScreen opens a full screen on a keyboard that can be waited on, as a terminal.
func readyScreen(t *testing.T) (*Screen, *typedInput) {
	t.Helper()
	in := newTypedInput(t)
	keys := &fakeKeys{}
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Version, o.KeyInput, o.InputReady, o.InputIsTerminal = io.Discard, in, testVersion, keys.toggle, in.Ready, true
	s, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, in
}

func TestAHiddenLineThatCanWaitEndsWithItsContextAndLeavesNothingPending(t *testing.T) {
	s, in := readyScreen(t)
	ctx, cancel := context.WithCancel(context.Background())
	hidden := make(chan error, 1)
	go func() { _, err := s.HiddenLine(ctx, hiddenLabel, nil); hidden <- err }()
	time.Sleep(50 * time.Millisecond) // the question is open, and nothing is typed
	cancel()
	select {
	case err := <-hidden:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("HiddenLine = %v, want the end of its context", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the context did not end the question")
	}

	// The next question has the keyboard to itself: its Enter is its own, and no read that was given up takes it.
	type answer struct {
		picked []int
		err    error
	}
	asked := make(chan answer, 1)
	go func() { picked, err := s.MultiSelect(repoQuestion(1, 0, 0)); asked <- answer{picked, err} }()
	time.Sleep(50 * time.Millisecond)
	in.Type("\r")
	select {
	case a := <-asked:
		if a.err != nil || !slices.Equal(a.picked, []int{0}) {
			t.Fatalf("MultiSelect = %+v, want its default picked by the Enter it was typed", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the next question did not get the key that was typed for it: a read that was given up still holds the keyboard")
	}
}

func TestAHiddenLineThatCanWaitStillReadsALineAndShowsNothingOfIt(t *testing.T) {
	s, in := readyScreen(t)
	in.Type("pa")
	got := make(chan []byte, 1)
	go func() {
		line, err := s.HiddenLine(context.Background(), hiddenLabel, nil)
		if err != nil {
			t.Errorf("HiddenLine = %v", err)
		}
		got <- line
	}()
	time.Sleep(50 * time.Millisecond) // typed in two parts: the second part comes while the first is read
	in.Type("sted\r")
	select {
	case line := <-got:
		if string(line) != "pasted" {
			t.Fatalf("HiddenLine = %q, want pasted", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the line was not read")
	}
}

func TestAScreenSaysWhetherAHiddenLineStopsCleanly(t *testing.T) {
	s, _ := readyScreen(t)
	if !s.HiddenLineStopsCleanly() {
		t.Error("a screen on a terminal that can be waited on says its hidden line does not stop cleanly")
	}
	keys := &fakeKeys{}
	o := fullOptions(ColorNone)
	o.KeyInput = keys.toggle
	plain, _ := openScreen(t, o, "")
	if plain.HiddenLineStopsCleanly() {
		t.Error("a screen on a terminal that cannot be waited on says its hidden line stops cleanly")
	}
}
