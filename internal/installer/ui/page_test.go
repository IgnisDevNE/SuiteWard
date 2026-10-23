package ui

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// chunkReader hands out one chunk per read, as a terminal delivers a key press (an arrow arrives whole, in one write), and calls hook
// before every read with the number of reads so far: the screen waits for the terminal then, so the hook sees the frame of an open
// question. The end of the chunks is the end of the input.
type chunkReader struct {
	chunks []string
	reads  int
	hook   func(n int)
}

func (c *chunkReader) Read(b []byte) (int, error) {
	if c.hook != nil {
		c.hook(c.reads)
	}
	if c.reads >= len(c.chunks) {
		return 0, io.EOF
	}
	n := copy(b, c.chunks[c.reads])
	c.reads++
	return n, nil
}

// openChunks opens a full screen over chunks. frames[n] is the frame that was on screen before read n.
func openChunks(t *testing.T, o Options, chunks ...string) (*Screen, *bytes.Buffer, *[]frame) {
	t.Helper()
	var (
		s      *Screen
		out    bytes.Buffer
		frames []frame
	)
	o.Out, o.Version = &out, testVersion
	o.In = &chunkReader{chunks: chunks, hook: func(int) { frames = append(frames, snapshot(s)) }}
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, &out, &frames
}

// contentLines is the content column of a frame with the step column, one entry per row, without trailing spaces and without the blank
// rows at the end.
func contentLines(f frame) []string {
	lines := strings.Split(content(f), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func wantLines(t *testing.T, f frame, want ...string) {
	t.Helper()
	got := contentLines(f)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the content column differs.\ngot:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A page starts with a title, then paragraphs and label/value blocks, one blank line between them.
func TestPageDrawsATitleParagraphsAndFieldsSeparatedByBlankLines(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	s.Steps("Confirm", "Restore")
	s.Page("Restore preview",
		Para("Restoring replaces the database of this instance."),
		Fields(KV{"Instance", "da0aff25"}, KV{"Backup", "suiteward-backup.tar.age"}),
		Para("Nothing is changed until you confirm."))
	wantLines(t, snapshot(s),
		"Restore preview",
		"",
		"Restoring replaces the database of this instance.",
		"",
		"  Instance  da0aff25",
		"  Backup    suiteward-backup.tar.age",
		"",
		"Nothing is changed until you confirm.")
}

func TestPageWrapsLongParagraphsAndFieldValues(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	s.Page("T", Para(strings.Repeat("word ", 30)), Fields(KV{"Key", strings.Repeat("value ", 20)}))
	f := snapshot(s)
	for i, row := range f.rows {
		if w := visibleWidth(row); w != 80 {
			t.Fatalf("row %d is %d columns wide, want 80: %q", i+1, w, row)
		}
	}
	lines := contentLines(f)
	if len(lines) < 8 {
		t.Fatalf("long text must wrap onto several rows, got:\n%s", strings.Join(lines, "\n"))
	}
	for _, l := range lines[len(lines)-3:] { // the value goes on under itself, indented past the key
		if !strings.HasPrefix(l, "  Key  ") && !strings.HasPrefix(l, "       ") {
			t.Errorf("a wrapped field value is not indented under its value: %q", l)
		}
	}
}

// The title is a section title: bold and the brand green. The label of a field is muted.
func TestPageStylesTheTitleAndTheLabels(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorTrue), "")
	s.Page("Restore preview", Fields(KV{"Instance", "da0aff25"}))
	rows := strings.Join(snapshot(s).rows, "\n")
	for _, want := range []string{
		esc + "[1;38;2;61;214;160mRestore preview" + esc + "[0m",
		esc + "[38;2;143;163;181mInstance  ",
	} {
		if !strings.Contains(rows, want) {
			t.Errorf("the page lacks %q:\n%s", want, rows)
		}
	}
}

// A page is a new page of the same step: the steps stay as they are, the page before it is gone from the frame, and what Close keeps of
// it (checks, warnings) is still printed.
func TestPageKeepsTheStepsAndTheDurableLinesOfThePageBeforeIt(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "")
	s.Steps("One", "Two")
	s.Step(2)
	s.Say("the old page\n")
	s.Check("a check that must stay")
	s.Page("Next", Para("the new page"))
	rows := strings.Join(snapshot(s).rows, "\n")
	if !strings.Contains(rows, "● Two") || !strings.Contains(rows, "step 2 of 2") || !strings.Contains(rows, "✓ One") {
		t.Errorf("a page must not reset the step list:\n%s", rows)
	}
	if strings.Contains(rows, "the old page") || strings.Contains(rows, "a check that must stay") {
		t.Errorf("a page starts clean:\n%s", rows)
	}
	mustClose(t, s)
	if got, want := afterLastLeave(out.String()), "✓ a check that must stay\nNext\n\nthe new page\n"; got != want {
		t.Errorf("Close printed %q, want the check that was kept and then the page that is current: %q", got, want)
	}
}

// Plain mode keeps what it printed byte for byte: a page writes nothing, and must not make a bar or a spinner print again.
func TestPageWritesNothingInPlainMode(t *testing.T) {
	s, out := openScreen(t, plainOptions(), "")
	s.Progress("Total", 0.5, "half")
	s.Spin("Waiting", 0)
	before := out.String()
	s.Page("Restore preview", Para("text"), Fields(KV{"Instance", "x"}))
	if out.String() != before {
		t.Fatalf("Page wrote %q in plain mode", strings.TrimPrefix(out.String(), before))
	}
	s.Progress("Total", 0.5, "half")
	s.Spin("Waiting", time.Second)
	if got, want := out.String(), "Total: half\nWaiting...\n"; got != want {
		t.Errorf("plain output after a page is %q, want %q: a page must not make a bar or a spinner print again", got, want)
	}
	mustClose(t, s)
	if got := out.String(); got != "Total: half\nWaiting...\n" {
		t.Errorf("Close printed %q after a plain page", got)
	}
}

func TestPageWritesNothingWhileSuspended(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "")
	s.Suspend()
	before := out.String()
	s.Page("Restore preview", Para("text"))
	if out.String() != before {
		t.Fatalf("Page wrote %q on a suspended screen", strings.TrimPrefix(out.String(), before))
	}
	s.Resume()
	if rows := strings.Join(snapshot(s).rows, "\n"); strings.Contains(rows, "Restore preview") {
		t.Errorf("a page that was asked for while suspended came back on Resume:\n%s", rows)
	}
}

// The question of a page is set apart at the bottom: a blank line and a rule of the width of the column, then the question.
func TestTheQuestionOfAPageIsSetApart(t *testing.T) {
	s, _, frames := openChunks(t, fullOptions(ColorNone), "Pedro\n")
	s.Steps("One")
	s.Page("Who are you?", Para("We need a name."))
	if got, err := s.Ask("Your name", ""); err != nil || got != "Pedro" {
		t.Fatalf("Ask = %q, %v", got, err)
	}
	wantLines(t, (*frames)[0],
		"Who are you?",
		"",
		"We need a name.",
		"",
		strings.Repeat("─", 57),
		"Your name:")
	wantLines(t, snapshot(s),
		"Who are you?",
		"",
		"We need a name.",
		"",
		strings.Repeat("─", 57),
		"Your name: Pedro")
}

func TestTheRuleIsMuted(t *testing.T) {
	s, _, frames := openChunks(t, fullOptions(ColorTrue), "x\n")
	s.Page("T", Para("p"))
	_, _ = s.Ask("Q", "")
	if rows := strings.Join((*frames)[0].rows, "\n"); !strings.Contains(rows, esc+"[38;2;143;163;181m"+strings.Repeat("─", 57)+esc+"[0m") {
		t.Errorf("the rule above the question is not drawn in the muted tone:\n%s", rows)
	}
}

// Only the first question of a page is set apart, and only a page that Page opened has one: the pages of the flows that do not use Page
// look as they did.
func TestOnlyTheFirstQuestionOfAPageIsSetApart(t *testing.T) {
	rules := countRules

	s, _, _ := openChunks(t, fullOptions(ColorNone), "a\n", "b\n")
	s.Page("T", Para("p"))
	_, _ = s.Ask("First", "")
	_, _ = s.Ask("Second", "")
	if got := rules(snapshot(s)); got != 1 {
		t.Errorf("%d rules on a page of two questions, want 1:\n%s", got, content(snapshot(s)))
	}

	s, _, _ = openChunks(t, fullOptions(ColorNone), "a\n")
	s.Say("a line of a page that Page did not open\n")
	_, _ = s.Ask("Question", "")
	if got := rules(snapshot(s)); got != 0 {
		t.Errorf("a page that Page did not open has %d rules, want none:\n%s", got, content(snapshot(s)))
	}

	s, _, _ = openChunks(t, fullOptions(ColorNone), "9\n", "1\n")
	s.Page("T", Para("p"))
	if got, err := s.Choose("Which?", testChoices, 0); err != nil || got != 0 {
		t.Fatalf("Choose = %d, %v", got, err)
	}
	if got := rules(snapshot(s)); got != 1 {
		t.Errorf("a question that is asked again set itself apart %d times, want 1:\n%s", got, content(snapshot(s)))
	}
}

// A new step is a new page: the next question is not set apart unless a page asks for it.
func TestAStepEndsThePageThatSetsItsQuestionApart(t *testing.T) {
	s, _, _ := openChunks(t, fullOptions(ColorNone), "a\n")
	s.Steps("One", "Two")
	s.Page("T", Para("p"))
	s.Step(2)
	_, _ = s.Ask("Question", "")
	if got := countRules(snapshot(s)); got != 0 {
		t.Errorf("a question after a step has %d rules, want none", got)
	}
}

// The rule is a drawing: Close prints the lines of the page without it.
func TestTheRuleIsNotPrintedOnTheNormalScreen(t *testing.T) {
	s, out, _ := openChunks(t, fullOptions(ColorNone), "Pedro\n")
	s.Page("Who?", Para("Name."))
	_, _ = s.Ask("Your name", "")
	mustClose(t, s)
	if got, want := afterLastLeave(out.String()), "Who?\n\nName.\n\nYour name: Pedro\n"; got != want {
		t.Errorf("Close printed %q, want %q", got, want)
	}
}

func TestEverySelectionAndSecretOfAPageIsSetApart(t *testing.T) {
	apart := func(f frame) bool { return strings.Contains(content(f), strings.Repeat("─", 57)) }
	secret := func() ([]byte, error) { return []byte("pw"), nil }

	s, _, _ := openChunks(t, fullOptions(ColorNone), "")
	s.Page("T", Para("p"))
	_, _ = s.Secret("Key", secret)
	if !apart(snapshot(s)) {
		t.Errorf("a secret question is not set apart:\n%s", content(snapshot(s)))
	}

	keys := &fakeKeys{}
	o := fullOptions(ColorNone)
	o.KeyInput = keys.toggle
	s, _, frames := openChunks(t, o, "\r")
	s.Page("T", Para("p"))
	_, _ = s.Select("Tier", testChoices, 0)
	if !apart((*frames)[0]) {
		t.Errorf("a Select list is not set apart:\n%s", content((*frames)[0]))
	}
}

// A warning that follows the text of a page is set off from it by a blank line; a run of warnings stays together.
func TestAWarningOfAPageIsSetOffByABlankLine(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	s.Page("T", Para("Text."))
	s.Warn("First.\n")
	s.Warn("Second.\n")
	wantLines(t, snapshot(s), "T", "", "Text.", "", "! First.", "! Second.")

	s, _ = openScreen(t, fullOptions(ColorNone), "")
	s.Say("a page that Page did not open\n")
	s.Warn("Not set off.\n")
	wantLines(t, snapshot(s), "a page that Page did not open", "! Not set off.")
}

// countRules is the number of rules that set a question apart in the content column of a frame.
func countRules(f frame) int {
	n := 0
	for _, l := range strings.Split(content(f), "\n") {
		if strings.TrimRight(l, " ") == strings.Repeat("─", 57) {
			n++
		}
	}
	return n
}
