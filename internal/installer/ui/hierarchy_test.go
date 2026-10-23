package ui

import (
	"fmt"
	"strings"
	"testing"
)

const (
	redSGR   = esc + "[38;2;242;107;107m"
	amberSGR = esc + "[38;2;242;196;109m"
	reset    = esc + "[0m"
)

// boxRow is a row of the critical box in the content column of an 80-column frame: 57 columns, a border and a space on each side.
func boxRow(text string) string { return fmt.Sprintf("│ %-53s │", text) }

// A critical warning is a red box on a full screen, so it cannot be read as one more "!" line.
func TestCriticalIsABoxOnAFullScreen(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	s.Critical("The recovery kit %s opens every backup. Copy it off this machine now.\n", "kit.txt")
	wantLines(t, snapshot(s),
		"╭"+strings.Repeat("─", 55)+"╮",
		boxRow("The recovery kit kit.txt opens every backup. Copy it"),
		boxRow("off this machine now."),
		"╰"+strings.Repeat("─", 55)+"╯")
}

func TestCriticalBoxFillsTheColumnAtEveryWidth(t *testing.T) {
	for _, width := range []int{60, 80, 120} {
		o := fullOptions(ColorNone)
		o.Width = width
		s, _ := openScreen(t, o, "")
		s.Critical("%s", strings.Repeat("word ", 60))
		f := snapshot(s)
		column := width - 2 - stepsWidth - 1 - 2
		lines := contentLines(f)
		if len(lines) < 5 {
			t.Fatalf("width %d: a long text must wrap inside the box, got:\n%s", width, strings.Join(lines, "\n"))
		}
		for i, l := range lines {
			if got := len([]rune(l)); got != column {
				t.Fatalf("width %d: row %d of the box is %d columns, want %d: %q", width, i, got, column, l)
			}
		}
	}
}

// The border is red in truecolor and in 256 colors; the text keeps the terminal's own color, in bold.
func TestCriticalBorderIsRed(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorTrue), "")
	s.Critical("Copy the kit.\n")
	rows := strings.Join(snapshot(s).rows, "\n")
	for _, want := range []string{redSGR + "╭", redSGR + "│" + reset, redSGR + "╰", esc + "[1mCopy the kit." + reset} {
		if !strings.Contains(rows, want) {
			t.Errorf("the box lacks %q:\n%s", want, rows)
		}
	}

	s, _ = openScreen(t, fullOptions(Color256), "")
	s.Critical("Copy the kit.\n")
	if rows := strings.Join(snapshot(s).rows, "\n"); !strings.Contains(rows, esc+"[38;5;203m╭") {
		t.Errorf("the 256-color border is not the nearest red (203):\n%s", rows)
	}
}

// In plain mode a critical warning is a warning: the text as given, with the newline added when it is missing. An agent or a script
// reads the same output it read when the kit warning was a Warn.
func TestCriticalIsTheWarningTextInPlainMode(t *testing.T) {
	s, out := openScreen(t, plainOptions(), "")
	s.Critical("Copy %s now.\n", "kit.txt")
	s.Critical("no newline")
	if got, want := out.String(), "Copy kit.txt now.\nno newline\n"; got != want {
		t.Fatalf("plain output = %q, want %q", got, want)
	}
}

// Close prints a critical warning again, like a Warn, so it survives the page and the alternate screen.
func TestCriticalIsKeptForTheNormalScreen(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "")
	s.Steps("One", "Two")
	s.Critical("Copy the kit off this machine.\n")
	s.Step(2)
	mustClose(t, s)
	if got, want := afterLastLeave(out.String()), "! Copy the kit off this machine.\n"; got != want {
		t.Errorf("Close printed %q, want %q", got, want)
	}
}

// The warning tone is the tone of the whole Warn line, not only of its glyph.
func TestWarnIsDrawnInTheWarningTone(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorTrue), "")
	s.Warn("Docker is also installed.\n")
	rows := strings.Join(snapshot(s).rows, "\n")
	for _, want := range []string{amberSGR + "! " + reset, amberSGR + "Docker is also installed." + reset} {
		if !strings.Contains(rows, want) {
			t.Errorf("the Warn line lacks %q:\n%s", want, rows)
		}
	}
}

func TestRedPaintsInTruecolorAnd256Colors(t *testing.T) {
	if got, want := ColorTrue.paint(toneRed, false, "x"), redSGR+"x"+reset; got != want {
		t.Errorf("truecolor red = %q, want %q", got, want)
	}
	if got, want := Color256.paint(toneRed, true, "x"), esc+"[1;38;5;203mx"+reset; got != want {
		t.Errorf("256-color bold red = %q, want %q", got, want)
	}
}

// Ask says what Enter does when there is a default, in the hint line, and only while the question is open.
func TestAskSaysThatEnterTakesTheDefault(t *testing.T) {
	hint := func(f frame) string { return f.rows[len(f.rows)-2] }

	s, _, frames := openChunks(t, fullOptions(ColorNone), "\n", "x\n")
	if got, err := s.Ask("Port", "8081"); err != nil || got != "8081" {
		t.Fatalf("Ask = %q, %v", got, err)
	}
	if got := hint((*frames)[0]); !strings.Contains(got, "Enter takes the default") || !strings.Contains(got, "Ctrl-C to quit") {
		t.Errorf("the hint of a question with a default is %q", got)
	}
	if got := hint(snapshot(s)); strings.Contains(got, "default") {
		t.Errorf("the hint still mentions the default after the answer: %q", got)
	}
	if _, err := s.Ask("Name", ""); err != nil {
		t.Fatal(err)
	}
	if got := hint((*frames)[1]); strings.Contains(got, "default") || !strings.Contains(got, "Enter to confirm") {
		t.Errorf("the hint of a question with no default is %q", got)
	}
}

func TestChooseSaysThatEnterTakesTheDefault(t *testing.T) {
	s, _, frames := openChunks(t, fullOptions(ColorNone), "\n")
	if _, err := s.Choose("Which?", testChoices, 0); err != nil {
		t.Fatal(err)
	}
	if got := (*frames)[0].rows[len((*frames)[0].rows)-2]; !strings.Contains(got, "Enter takes the default") {
		t.Errorf("the hint of a numbered question is %q", got)
	}
}

// The plain question is "label [def]: " and nothing else.
func TestPlainAskIsUnchanged(t *testing.T) {
	s, out := openScreen(t, plainOptions(), "\n")
	if got, err := s.Ask("Port", "8081"); err != nil || got != "8081" {
		t.Fatalf("Ask = %q, %v", got, err)
	}
	if got, want := out.String(), "Port [8081]: "; got != want {
		t.Errorf("plain Ask wrote %q, want %q", got, want)
	}
}
