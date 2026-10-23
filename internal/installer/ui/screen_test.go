package ui

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testVersion = "v1.2.3"
	esc         = "\x1b"
	enterAlt    = esc + "[?1049h"
	leaveAlt    = esc + "[?1049l"
)

var testChoices = []Choice{
	{"co-located", "alongside the agent, on this PC"},
	{"isolated", "on a separate host"},
}

func plainOptions() Options { return Options{Mode: ModePlain} }

func fullOptions(color ColorLevel) Options {
	return Options{Mode: ModeFull, Color: color, Width: 80, Height: 24}
}

// openScreen opens a screen over a buffer and an input; the buffer holds what the terminal would receive.
func openScreen(t *testing.T, o Options, input string) (*Screen, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	o.Out, o.In, o.Version = &out, strings.NewReader(input), testVersion
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, &out
}

func mustClose(t *testing.T, s *Screen) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// snapshot is the frame the screen would draw now.
func snapshot(s *Screen) frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frame()
}

// afterLastLeave returns what the screen printed once it had left the alternate screen for good.
func afterLastLeave(out string) string {
	_, rest, _ := strings.Cut(out, leaveAlt)
	for strings.Contains(rest, leaveAlt) {
		_, rest, _ = strings.Cut(rest, leaveAlt)
	}
	return rest
}

func TestPlainModeWritesOnlyPlainText(t *testing.T) {
	s, out := openScreen(t, plainOptions(), "2\n")
	if s.FullScreen() {
		t.Fatal("a plain screen reports full screen")
	}
	s.Steps("Welcome", "Checks")
	s.Say("Using %s (%s).\n", "Podman 5.8", "rootless")
	idx, err := s.Choose("How should SuiteWard run?", testChoices, 0)
	if err != nil || idx != 1 {
		t.Fatalf("Choose = %d, %v; want 1 (typed 2), nil", idx, err)
	}
	s.Step(2)
	s.Check("Podman 5.8, rootless")
	s.Warn("Note: Docker is also installed.\n")
	s.Spin("Waiting for GitHub", 0)
	s.Spin("Waiting for GitHub", 21*time.Second)
	s.EndSpin()
	s.Progress("Database", 0.5, "pulling")
	s.Progress("Database", 0.6, "pulling")
	s.Progress("Database", 1, "ready")
	s.Progress("Total", 0.62, "")
	s.Progress("Total", 0.65, "")
	s.Keep("Status URL: %s\n", "http://127.0.0.1:8080/status")
	if err := s.Summary([]KV{{"App", "suiteward-q"}, {"Owner", "octocat"}}, []string{"Note: restart the machine."}); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	mustClose(t, s)

	want := "Using Podman 5.8 (rootless).\n" +
		"How should SuiteWard run?\n" +
		"1) co-located   alongside the agent, on this PC (default)\n" +
		"2) isolated     on a separate host\n" +
		"Choose [1]: " +
		"Podman 5.8, rootless\n" +
		"Note: Docker is also installed.\n" +
		"Waiting for GitHub...\n" +
		"Database: pulling\n" +
		"Database: ready\n" +
		"Total: 60%\n" +
		"Status URL: http://127.0.0.1:8080/status\n" +
		"App: suiteward-q\n" +
		"Owner: octocat\n" +
		"Note: restart the machine.\n"
	if got := out.String(); got != want {
		t.Fatalf("plain output differs.\ngot:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(out.String(), esc) {
		t.Fatal("plain output holds an escape sequence")
	}
}

func TestPlainSayIsByteIdentical(t *testing.T) {
	s, out := openScreen(t, plainOptions(), "")
	s.Say("a %d %s\n", 1, "b")
	s.Say("no newline, and 100%% sure")
	if _, err := s.Write([]byte("\n  indented\ttab\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got, want := out.String(), "a 1 b\nno newline, and 100% sure\n  indented\ttab\r\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPlainSpinnerPrintsOneLinePerLabel(t *testing.T) {
	s, out := openScreen(t, plainOptions(), "")
	for i := range 50 {
		s.Spin("Waiting for GitHub", time.Duration(i)*200*time.Millisecond)
	}
	if got, want := out.String(), "Waiting for GitHub...\n"; got != want {
		t.Fatalf("50 ticks printed %q, want %q", got, want)
	}
	s.EndSpin()
	s.Spin("Waiting for GitHub", 0)
	s.Spin("Waiting for the database", time.Second)
	if got, want := out.String(), "Waiting for GitHub...\nWaiting for GitHub...\nWaiting for the database...\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestChoose(t *testing.T) {
	const (
		list0 = "How should SuiteWard run?\n1) co-located   alongside the agent, on this PC (default)\n2) isolated     on a separate host\n"
		list1 = "How should SuiteWard run?\n1) co-located   alongside the agent, on this PC\n2) isolated     on a separate host (default)\n"
		bad   = "Enter a number from 1 to 2.\n"
		ask   = "Choose [1]: "
	)
	tests := []struct {
		name    string
		input   string
		def     int
		want    int
		wantOut string
	}{
		{"a number", "2\n", 0, 1, list0 + ask},
		{"the first number", "1\n", 1, 0, list1 + "Choose [2]: "},
		{"empty takes the default", "\n", 0, 0, list0 + ask},
		{"empty takes another default", "\n", 1, 1, list1 + "Choose [2]: "},
		{"spaces are trimmed", "  2 \n", 0, 1, list0 + ask},
		{"the last line may lack its newline", "2", 0, 1, list0 + ask},
		{"invalid answers ask again", "9\nabc\n0\n-1\n2\n", 0, 1, list0 + ask + bad + ask + bad + ask + bad + ask + bad + ask},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, out := openScreen(t, plainOptions(), tt.input)
			got, err := s.Choose("How should SuiteWard run?", testChoices, tt.def)
			if err != nil || got != tt.want {
				t.Fatalf("Choose = %d, %v; want %d, nil", got, err, tt.want)
			}
			if out.String() != tt.wantOut {
				t.Fatalf("output:\n%q\nwant:\n%q", out.String(), tt.wantOut)
			}
		})
	}
}

func TestChooseFailures(t *testing.T) {
	s, _ := openScreen(t, plainOptions(), "")
	if _, err := s.Choose("Q", testChoices, 0); !errors.Is(err, io.EOF) {
		t.Fatalf("end of input: err = %v, want io.EOF", err)
	}
	s, _ = openScreen(t, plainOptions(), "9\n")
	if _, err := s.Choose("Q", testChoices, 0); !errors.Is(err, io.EOF) {
		t.Fatalf("an invalid answer, then the end of input: err = %v, want io.EOF", err)
	}
	for _, def := range []int{-1, 2} {
		if _, err := s.Choose("Q", testChoices, def); err == nil {
			t.Fatalf("a default of %d among 2 choices was accepted", def)
		}
	}
	if _, err := s.Choose("Q", nil, 0); err == nil {
		t.Fatal("no choices were accepted")
	}
}

func TestAskAndPrompt(t *testing.T) {
	tests := []struct {
		name, input, label, def, want, wantOut string
	}{
		{"an answer", "hello\n", "Name", "x", "hello", "Name [x]: "},
		{"empty takes the default", "\n", "Name", "x", "x", "Name [x]: "},
		{"no default, an answer", " hi \n", "Name", "", "hi", "Name: "},
		{"no default, empty", "\n", "Name", "", "", "Name: "},
		{"the last line may lack its newline", "hi", "Name", "x", "hi", "Name [x]: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, out := openScreen(t, plainOptions(), tt.input)
			got, err := s.Ask(tt.label, tt.def)
			if err != nil || got != tt.want || out.String() != tt.wantOut {
				t.Fatalf("Ask = %q, %v with output %q; want %q with output %q", got, err, out.String(), tt.want, tt.wantOut)
			}
		})
	}
	s, out := openScreen(t, plainOptions(), "yes\n")
	if got, err := s.Prompt("Remove it? [y/N]: "); err != nil || got != "yes" || out.String() != "Remove it? [y/N]: " {
		t.Fatalf("Prompt = %q, %v with output %q", got, err, out.String())
	}
	if _, err := s.Prompt("again: "); !errors.Is(err, io.EOF) {
		t.Fatalf("end of input: err = %v, want io.EOF", err)
	}
	if _, err := s.Ask("Name", "x"); !errors.Is(err, io.EOF) {
		t.Fatalf("Ask at the end of input: err = %v, want io.EOF", err)
	}
}

func TestSecretIsNeverShownOrKept(t *testing.T) {
	const secret = "hunter2-not-a-real-secret"
	read := func() ([]byte, error) { return []byte(secret), nil }
	for name, o := range map[string]Options{"plain": plainOptions(), "full": fullOptions(ColorNone)} {
		t.Run(name, func(t *testing.T) {
			s, out := openScreen(t, o, "")
			got, err := s.Secret("Token", read)
			if err != nil || string(got) != secret {
				t.Fatalf("Secret = %q, %v; want the reader's bytes", got, err)
			}
			s.Say("later line\n")
			s.Keep("kept line\n")
			mustClose(t, s)
			if strings.Contains(out.String(), secret) {
				t.Fatalf("the secret reached the terminal output:\n%s", out.String())
			}
			if name == "plain" && out.String() != "Token (input is hidden): \nlater line\nkept line\n" {
				t.Fatalf("plain output = %q", out.String())
			}
			if name == "full" && !strings.Contains(out.String(), "Token (input is hidden):") {
				t.Fatal("the full-screen prompt did not show its label")
			}
		})
	}
	s, _ := openScreen(t, plainOptions(), "")
	boom := errors.New("terminal closed")
	if _, err := s.Secret("Token", func() ([]byte, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("a reader's error must be returned, got %v", err)
	}
	if _, err := s.Secret("Token", nil); err == nil {
		t.Fatal("a missing reader was accepted")
	}
}

func TestOpenValidates(t *testing.T) {
	var out bytes.Buffer
	tests := []struct {
		name string
		o    Options
	}{
		{"no output", Options{In: strings.NewReader(""), Mode: ModePlain}},
		{"no input", Options{Out: &out, Mode: ModePlain}},
		{"full screen too narrow", Options{Out: &out, In: strings.NewReader(""), Mode: ModeFull, Width: 59, Height: 24}},
		{"full screen too short", Options{Out: &out, In: strings.NewReader(""), Mode: ModeFull, Width: 80, Height: 19}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if s, err := Open(tt.o); err == nil {
				t.Fatalf("Open accepted it (screen %v)", s)
			}
		})
	}
	if out.Len() != 0 {
		t.Fatalf("a rejected Open wrote %q", out.String())
	}
}

func TestStepRejectsOutOfRange(t *testing.T) {
	for name, o := range map[string]Options{"plain": plainOptions(), "full": fullOptions(ColorNone)} {
		for _, n := range []int{0, 3, -1} {
			t.Run(name, func(t *testing.T) {
				s, _ := openScreen(t, o, "")
				s.Steps("One", "Two")
				defer func() {
					if recover() == nil {
						t.Fatalf("Step(%d) of 2 did not panic", n)
					}
				}()
				s.Step(n)
			})
		}
	}
}

func TestFullScreenEntersTheAlternateScreen(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "")
	if !s.FullScreen() {
		t.Fatal("a full screen reports plain")
	}
	got := out.String()
	if !strings.HasPrefix(got, enterAlt) || !strings.Contains(got, "◆ SuiteWard installer "+testVersion) {
		t.Fatalf("Open must enter the alternate screen and draw the frame, wrote %q", got)
	}
	mustClose(t, s)
}

func TestSuspendLeavesTheAlternateScreenUntilResume(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "")
	s.Suspend()
	s.Suspend()
	out.Reset()
	s.Say("while away\n")
	if out.Len() != 0 {
		t.Fatalf("a suspended screen drew %q", out.String())
	}
	s.Resume()
	got := out.String()
	if !strings.HasPrefix(got, enterAlt) || !strings.Contains(got, "while away") || !strings.Contains(got, "◆ SuiteWard installer") {
		t.Fatalf("Resume must enter the alternate screen and redraw, wrote %q", got)
	}
	out.Reset()
	s.Resume()
	if out.Len() != 0 {
		t.Fatalf("a second Resume wrote %q", out.String())
	}
	s.Suspend()
	if got := out.String(); !strings.Contains(got, leaveAlt) || !strings.Contains(got, esc+"[?25h") {
		t.Fatalf("Suspend must show the cursor and leave the alternate screen, wrote %q", got)
	}
	mustClose(t, s)
}

func TestCloseLeavesBeforePrintingTheKeepBlock(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "\n")
	s.Say("not kept\n")
	s.Keep("Token file: %s\n", `C:\tok`)
	s.Keep("two\nlines\n")
	if err := s.Summary([]KV{{"App", "suiteward-q"}, {"Status URL", "http://127.0.0.1:8080/status"}}, []string{"Restart the machine."}); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	mustClose(t, s)
	want := "Token file: C:\\tok\ntwo\nlines\nApp: suiteward-q\nStatus URL: http://127.0.0.1:8080/status\nRestart the machine.\n"
	if got := afterLastLeave(out.String()); got != want {
		t.Fatalf("the normal screen received %q, want %q", got, want)
	}
	n := out.Len()
	mustClose(t, s)
	if out.Len() != n {
		t.Fatal("a second Close wrote again")
	}
	s.Say("after close: %d\n", 7)
	if got := out.String()[n:]; got != "after close: 7\n" {
		t.Fatalf("after Close the screen must print plainly, wrote %q", got)
	}
}

func TestCloseAfterSuspendLeavesOnce(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "")
	s.Keep("kept\n")
	s.Suspend()
	mustClose(t, s)
	if n := strings.Count(out.String(), leaveAlt); n != 1 {
		t.Fatalf("the alternate screen was left %d times, want 1", n)
	}
	if got := afterLastLeave(out.String()); got != "kept\n" {
		t.Fatalf("keep block = %q", got)
	}
}

func TestCloseRestoresTheConsoleAndReportsItsError(t *testing.T) {
	boom := errors.New("cannot restore the console")
	var restored int
	o := fullOptions(ColorNone)
	o.RestoreConsole = func() error { restored++; return boom }
	s, out := openScreen(t, o, "")
	s.Keep("kept\n")
	if err := s.Close(); !errors.Is(err, boom) {
		t.Fatalf("Close = %v, want the restore error", err)
	}
	if got := afterLastLeave(out.String()); got != "kept\n" {
		t.Fatalf("the keep block must still print, got %q", got)
	}
	if err := s.Close(); !errors.Is(err, boom) || restored != 1 {
		t.Fatalf("a second Close must report the same error without restoring again (restored %d, err %v)", restored, err)
	}
}

func TestWriteErrorsSurfaceOnClose(t *testing.T) {
	boom := errors.New("stdout closed")
	s, err := Open(Options{Out: failingWriter{boom}, In: strings.NewReader(""), Mode: ModePlain})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Say("lost\n")
	if err := s.Close(); !errors.Is(err, boom) {
		t.Fatalf("Close = %v, want the write error", err)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestFullScreenNeutralizesTerminalControls(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "")
	s.Say("a" + esc + "[2Jb\x07c\x00d\tE\n")
	s.Keep("kept" + esc + "]0;title\x07\n")
	f := snapshot(s)
	for _, row := range f.rows {
		if strings.ContainsAny(row, esc+"\x07\x00\t") {
			t.Fatalf("a control character reached the frame: %q", row)
		}
	}
	if !strings.Contains(strings.Join(f.rows, "\n"), "a[2Jbcd E") {
		t.Fatalf("the printable text was lost:\n%s", strings.Join(f.rows, "\n"))
	}
	mustClose(t, s)
	if tail := afterLastLeave(out.String()); strings.ContainsAny(tail, esc+"\x07") {
		t.Fatalf("a control character reached the normal screen: %q", tail)
	}
}

func TestCarriageReturnOverwritesTheLine(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	s.Say("pulling 10%%\rpulling 20%%\r\n")
	s.Say("done 1\r\n")
	rows := strings.Join(snapshot(s).rows, "\n")
	if strings.Contains(rows, "pulling 10%") || !strings.Contains(rows, "pulling 20%") || !strings.Contains(rows, "done 1") {
		t.Fatalf("a carriage return must overwrite the pending line, and CRLF must end it:\n%s", rows)
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorTrue), "")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 200 {
			s.Spin("Waiting", time.Duration(i)*100*time.Millisecond)
			s.Progress("Total", float64(i)/200, "")
		}
		s.EndSpin()
	}()
	go func() {
		defer wg.Done()
		for i := range 200 {
			s.Say("line %d\n", i)
			s.Check("check %d", i)
		}
	}()
	wg.Wait()
	mustClose(t, s)
}
