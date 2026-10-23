package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCarriageReturnAcrossWrites(t *testing.T) {
	tests := []struct {
		name    string
		writes  []string
		want    []string
		notWant []string
	}{
		{"CRLF split between two writes ends the line", []string{"keep me\r", "\nnext\n"}, []string{"keep me", "next"}, nil},
		{"text after a split carriage return overwrites the line", []string{"old\r", "new\n"}, []string{"new"}, []string{"old"}},
		{"a second carriage return keeps the line", []string{"a\r\r", "\nb\n"}, []string{"a", "b"}, nil},
		{"a dropped control character does not overwrite", []string{"old\r\x00", "\nnew\n"}, []string{"old", "new"}, nil},
		{"a dropped control character, then text", []string{"old\r\x00", "new\n"}, []string{"new"}, []string{"old"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := openScreen(t, fullOptions(ColorNone), "")
			for _, w := range tt.writes {
				if _, err := s.Write([]byte(w)); err != nil {
					t.Fatalf("Write: %v", err)
				}
			}
			rows := strings.Join(snapshot(s).rows, "\n")
			for _, w := range tt.want {
				if !strings.Contains(rows, w) {
					t.Errorf("the frame lacks %q:\n%s", w, rows)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(rows, w) {
					t.Errorf("the frame still shows %q:\n%s", w, rows)
				}
			}
		})
	}
}

func TestSuspendedOutputIsNeitherKeptNorRedrawn(t *testing.T) {
	const sentinel = "totp-sentinel-0123456789"
	s, out := openScreen(t, fullOptions(ColorNone), "")
	s.Suspend()
	s.Say("setup printed %s\n", sentinel)
	s.Check("checked %s", sentinel)
	s.Keep("kept while away\n")
	s.Resume()
	s.Say("back\n")
	mustClose(t, s)
	if n := strings.Count(out.String(), sentinel); n != 2 {
		t.Fatalf("the child's text must appear exactly where it was written (2 times), not on a redraw or in the keep block; got %d in %q", n, out.String())
	}
	if n := strings.Count(out.String(), "kept while away"); n != 1 {
		t.Fatalf("a line kept while suspended is written once, got %d", n)
	}
	if tail := afterLastLeave(out.String()); strings.Contains(tail, sentinel) || strings.Contains(tail, "kept while away") {
		t.Fatalf("nothing written while suspended may be repeated at Close: %q", tail)
	}
}

func TestCloseWithoutSummaryPrintsThePageAndTheWarnings(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "")
	s.Steps("One", "Two")
	s.Warn("early warning")
	s.Step(2)
	s.Say("a line\n")
	s.Check("a check")
	s.Progress("Database", 0.5, "pulling")
	s.Keep("kept line\n")
	s.Warn("late warning")
	s.Say("pending: ")
	s.Spin("Waiting", 0)
	mustClose(t, s)
	want := "a line\na check\nDatabase: pulling\npending: \nWaiting...\n" + "early warning\nkept line\nlate warning\n"
	if got := afterLastLeave(out.String()); got != want {
		t.Fatalf("the normal screen received\n%q\nwant\n%q", got, want)
	}
}

func TestCloseAfterAShownSummaryDoesNotRepeatThePage(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "\n\n")
	s.Say("on the page\n")
	s.Keep("k\n")
	for _, key := range []string{"A", "B"} {
		if err := s.Summary([]KV{{key, "1"}}, []string{"w" + key}); err != nil {
			t.Fatalf("Summary: %v", err)
		}
	}
	mustClose(t, s)
	if got, want := afterLastLeave(out.String()), "k\nB: 1\nwB\n"; got != want {
		t.Fatalf("the normal screen received %q, want %q (the last Summary replaces the first)", got, want)
	}
}

func TestCloseAfterACancelledChoiceKeepsTheQuestion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	in := &signalReader{r: pr, reading: make(chan struct{})}
	var out bytes.Buffer
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Version, o.Context = &out, in, testVersion, ctx
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Choose("How should SuiteWard run?", testChoices, 0); done <- err }()
	select {
	case <-in.reading: // the prompt is on the page and the screen waits for the terminal
	case <-time.After(2 * time.Second):
		t.Fatal("the choice never asked for input")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Choose = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the choice did not end when the context was cancelled")
	}
	mustClose(t, s)
	want := "How should SuiteWard run?\n1) co-located   alongside the agent, on this PC (default)\n2) isolated     on a separate host\nChoose [1]: \n"
	if got := afterLastLeave(out.String()); got != want {
		t.Fatalf("the normal screen received %q, want %q", got, want)
	}
}

// signalReader closes reading when the screen first reads: the prompt has been drawn by then.
type signalReader struct {
	r       io.Reader
	once    sync.Once
	reading chan struct{}
}

func (s *signalReader) Read(p []byte) (int, error) {
	s.once.Do(func() { close(s.reading) })
	return s.r.Read(p)
}

func TestPromptsEndWhenTheContextIsCancelled(t *testing.T) {
	asks := map[string]func(*Screen) error{
		"Ask":    func(s *Screen) error { _, err := s.Ask("Name", "x"); return err },
		"Prompt": func(s *Screen) error { _, err := s.Prompt("Name: "); return err },
		"Choose": func(s *Screen) error { _, err := s.Choose("Q", testChoices, 0); return err },
	}
	for name, ask := range asks {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pr, pw := io.Pipe()
			t.Cleanup(func() { _ = pw.Close() }) // lets the abandoned read end
			s, err := Open(Options{Out: io.Discard, In: pr, Mode: ModePlain, Context: ctx})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			done := make(chan error, 1)
			go func() { done <- ask(s) }()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err = %v, want context.Canceled", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the prompt did not end when the context was cancelled")
			}
		})
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestACancelledContextDoesNotReadAtAll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := readerFunc(func([]byte) (int, error) {
		t.Error("the input was read although the context was already cancelled")
		return 0, io.EOF
	})
	s, err := Open(Options{Out: io.Discard, In: in, Mode: ModePlain, Context: ctx})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Ask("Name", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ask = %v, want context.Canceled", err)
	}
}

func TestSecretPromptDoesNotPromiseCtrlC(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	var during string
	read := func() ([]byte, error) {
		during = strings.Join(snapshot(s).rows, "\n")
		return []byte("x"), nil
	}
	if _, err := s.Secret("Token", read); err != nil {
		t.Fatalf("Secret: %v", err)
	}
	if !strings.Contains(during, "Enter to confirm") || strings.Contains(during, "Ctrl-C") {
		t.Fatalf("a hidden prompt cannot be interrupted, so its hint must not offer Ctrl-C:\n%s", during)
	}
	if after := strings.Join(snapshot(s).rows, "\n"); !strings.Contains(after, "Ctrl-C to quit") {
		t.Fatalf("the hint must come back once the secret is read:\n%s", after)
	}
}

func TestOpenFailureRestoresTheConsole(t *testing.T) {
	var restored int
	o := Options{Out: io.Discard, Mode: ModeFull, Width: 80, Height: 24, RestoreConsole: func() error { restored++; return nil }}
	if _, err := Open(o); err == nil || restored != 1 {
		t.Fatalf("Open without an input: err = %v, restored %d times; want an error and one restore", err, restored)
	}
	boom := errors.New("cannot restore the console")
	o.RestoreConsole = func() error { return boom }
	if _, err := Open(o); !errors.Is(err, boom) {
		t.Fatalf("a restore failure must be reported with the Open error, got %v", err)
	}
}

func TestVersionIsOneSafeLine(t *testing.T) {
	var out bytes.Buffer
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Version = &out, strings.NewReader(""), "v1"+esc+"[2J\nx"
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i, row := range snapshot(s).rows {
		if strings.Contains(row, esc) || visibleWidth(row) != 80 {
			t.Fatalf("row %d is not a clean 80-column row: %q", i+1, row)
		}
	}
	if rows := strings.Join(snapshot(s).rows, "\n"); !strings.Contains(rows, "installer v1[2J x") {
		t.Fatalf("the version must be cleaned onto one line:\n%s", rows)
	}
}

func TestKeepAfterAPendingLineLeavesTheLineUnkept(t *testing.T) {
	tests := []struct{ name, say, want string }{
		{"a pending line", "Pulling image ", "Pulling image \nToken file: x\n"},
		{"a pending carriage return", "progress\r", "progress\nToken file: x\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, out := openScreen(t, fullOptions(ColorNone), "")
			s.Say("%s", tt.say)
			s.Keep("Token file: %s\n", "x")
			mustClose(t, s)
			if got := afterLastLeave(out.String()); got != tt.want {
				t.Fatalf("the normal screen received %q, want %q", got, tt.want)
			}
		})
	}
}
