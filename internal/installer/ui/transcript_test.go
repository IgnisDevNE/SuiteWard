package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCloseKeepsTheMarkerOfEveryLine(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "")
	s.Check("first\nsecond")
	s.Warn("careful\nreally")
	s.Say("plain\n")
	mustClose(t, s)
	want := "✓ first\n  second\n! careful\n  really\nplain\n"
	if got := afterLastLeave(out.String()); got != want {
		t.Fatalf("the normal screen received %q, want %q: a line keeps the marker the frame drew in front of it", got, want)
	}
}

func TestCloseKeepsTheChecksAndWarningsOfEveryStep(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "")
	s.Steps("A", "B", "C")
	s.Check("a1")
	s.Say("noise of a\n")
	s.Warn("wa")
	s.Step(2)
	s.Check("b1")
	s.Keep("kept in b\n")
	s.Progress("Bar of b", 1, "done")
	s.Step(3)
	s.Check("c1")
	s.Say("page of c\n")
	mustClose(t, s)
	want := "✓ a1\n! wa\n✓ b1\nkept in b\n✓ c1\npage of c\n"
	if got := afterLastLeave(out.String()); got != want {
		t.Fatalf("the normal screen received %q, want %q", got, want)
	}
}

func TestCloseWithASummaryKeepsTheChecksBeforeIt(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "\n")
	s.Steps("A", "B")
	s.Check("a1")
	s.Step(2)
	s.Check("b1")
	s.Warn("wb")
	s.Say("text of b\n")
	if err := s.Summary([]KV{{"Tier", "co-located"}}, nil); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	mustClose(t, s)
	want := "✓ a1\n✓ b1\n! wb\nTier: co-located\n"
	if got := afterLastLeave(out.String()); got != want {
		t.Fatalf("the normal screen received %q, want %q", got, want)
	}
}

func TestCloseKeepsTheChecksThatScrolledOffThePage(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "")
	s.Check("first")
	for i := range maxItems + 50 {
		s.Say("noise %d\n", i)
	}
	s.Warn("last")
	mustClose(t, s)
	tail := afterLastLeave(out.String())
	if !strings.HasPrefix(tail, "✓ first\n") || !strings.HasSuffix(tail, "! last\n") || strings.Contains(tail, "noise 0\n") {
		t.Fatalf("the check of a page that outgrew its cap must stay, and the noise before the cap must not: %q...%q", tail[:min(40, len(tail))], tail[max(0, len(tail)-40):])
	}
}

func TestWaitHoldsThePageUntilEnter(t *testing.T) {
	var s *Screen
	var during string
	in := readerFunc(func(p []byte) (int, error) {
		during = strings.Join(snapshot(s).rows, "\n")
		return copy(p, "\nnext\n"), nil
	})
	var out strings.Builder
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Version = &out, in, testVersion
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Say("page line\n")
	s.Say("pending: ")
	if err := s.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !strings.Contains(during, "page line") || !strings.Contains(during, "Enter to finish") || strings.Contains(during, "Ctrl-C to quit") {
		t.Errorf("while it waits the page stays and the hint offers Enter to finish, also with a pending line:\n%s", during)
	}
	if answer, err := s.Prompt(""); err != nil || answer != "next" {
		t.Errorf("the next answer is %q (%v): Wait takes one line and no more", answer, err)
	}
	if after := strings.Join(snapshot(s).rows, "\n"); !strings.Contains(after, "Ctrl-C to quit") {
		t.Errorf("the hint must come back once the wait is over:\n%s", after)
	}
	mustClose(t, s)
	if got, want := afterLastLeave(out.String()), "page line\npending: next\n"; got != want {
		t.Errorf("the normal screen received %q, want %q: the wait adds nothing to the page", got, want)
	}
}

func TestWaitEndsWithTheEndOfTheInputAndAFinishedContext(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	if err := s.Wait(); err != nil {
		t.Errorf("Wait at the end of the input = %v, want nil: nothing more can be read", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := fullOptions(ColorNone)
	o.Out, o.Context = io.Discard, ctx
	o.In = readerFunc(func([]byte) (int, error) {
		t.Error("the input was read although the context was already done")
		return 0, io.EOF
	})
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Wait(); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait with a cancelled context = %v, want context.Canceled", err)
	}
}

func TestWaitDoesNothingInPlainMode(t *testing.T) {
	s, out := openScreen(t, plainOptions(), "answer\n")
	if err := s.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got, err := s.Ask("Name", ""); err != nil || got != "answer" {
		t.Errorf("Ask = %q (%v): a plain Wait must not take the answer", got, err)
	}
	if got, want := out.String(), "Name: "; got != want {
		t.Errorf("a plain Wait wrote %q", got)
	}
}
