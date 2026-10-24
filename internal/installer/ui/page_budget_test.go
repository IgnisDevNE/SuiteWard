package ui

import (
	"fmt"
	"strings"
	"testing"
)

// A page is longer than a small window. The title and the question stay, and the explanation between them is trimmed from the end,
// with one row that says so: nothing the user must answer is ever cut, and nothing is cut without a word.

const trimRow = "… enlarge the window to read the rest"

var sizes = []struct {
	name          string
	width, height int
}{{"80x24", 80, 24}, {"60x20", 60, 20}}

func windowOptions(width, height int) Options {
	o := fullOptions(ColorNone)
	o.Width, o.Height = width, height
	return o
}

// longPage is three paragraphs and a block of fields, longer than the body of either window.
func longPage(s *Screen) {
	para := func(first, last string) Block {
		return Para(first + " " + strings.Repeat("alpha beta gamma delta. ", 12) + last)
	}
	s.Page("Restore preview",
		para("First paragraph begins here.", "First ends."),
		Fields(KV{"Instance", "da0aff25"}, KV{"Backup", "suiteward-backup-20261022.tar.age"}),
		para("Second paragraph begins here.", "Second ends."),
		para("Third paragraph begins here.", "Third ends here."))
}

func ruleRow(width int) string { return strings.Repeat("─", width-23) }

func TestAPageThatDoesNotFitKeepsItsTitleAndItsQuestion(t *testing.T) {
	for _, w := range sizes {
		t.Run(w.name, func(t *testing.T) {
			s, _, frames := openChunks(t, windowOptions(w.width, w.height), "x\n")
			s.Steps("One", "Two")
			longPage(s)
			if _, err := s.Ask("Type the instance id to confirm", ""); err != nil {
				t.Fatal(err)
			}
			f := (*frames)[0]
			lines := contentLines(f)
			if got, want := len(lines), w.height-frameRows; got != want {
				t.Fatalf("%d rows of content, want %d (the body, full):\n%s", got, want, strings.Join(lines, "\n"))
			}
			if lines[0] != "Restore preview" {
				t.Errorf("the title is not the first row: %q", lines[0])
			}
			if got := lines[len(lines)-1]; got != "Type the instance id to confirm:" {
				t.Errorf("the question is not the last row: %q", got)
			}
			if got := lines[len(lines)-2]; got != ruleRow(w.width) {
				t.Errorf("the rule is not right above the question: %q", got)
			}
			text := strings.Join(lines, "\n")
			if strings.Count(text, trimRow) != 1 {
				t.Errorf("want exactly one row that says the page was trimmed (%q):\n%s", trimRow, text)
			}
			if !strings.Contains(squash(text), squash("First paragraph begins here.")) || strings.Contains(squash(text), squash("Third ends here.")) {
				t.Errorf("the explanation is trimmed from the end, not from the top:\n%s", text)
			}
			if !f.cursor || !strings.Contains(f.rows[f.curRow-1], "Type the instance id to confirm:") {
				t.Errorf("the cursor is not on the question: visible %v, row %d", f.cursor, f.curRow)
			}
		})
	}
}

// A page that fits is shown whole, with no row that says anything was cut.
func TestAPageThatFitsIsNotTrimmed(t *testing.T) {
	s, _, frames := openChunks(t, windowOptions(80, 50), "x\n")
	longPage(s)
	if _, err := s.Ask("Q", ""); err != nil {
		t.Fatal(err)
	}
	if text := content((*frames)[0]); strings.Contains(text, trimRow) || !strings.Contains(squash(text), squash("Third ends here.")) {
		t.Errorf("a page that fits was trimmed:\n%s", text)
	}
}

// A critical box is never the row that is trimmed: the explanation before it goes, and the indicator stands where the cut began.
func TestAPageKeepsItsCriticalBoxWhenItTrimsTheExplanation(t *testing.T) {
	for _, w := range sizes {
		t.Run(w.name, func(t *testing.T) {
			s, _, frames := openChunks(t, windowOptions(w.width, w.height), "x\n")
			longPage(s)
			s.Critical("Keep the kit off this machine.")
			if _, err := s.Ask("Type stored to go on", ""); err != nil {
				t.Fatal(err)
			}
			lines := contentLines((*frames)[0])
			text := strings.Join(lines, "\n")
			top, box, bottom, indicator := -1, -1, -1, -1
			for i, l := range lines {
				switch {
				case strings.HasPrefix(l, "╭"):
					top = i
				case strings.Contains(l, "Keep the kit off this machine."):
					box = i
				case strings.HasPrefix(l, "╰"):
					bottom = i
				case l == trimRow:
					indicator = i
				}
			}
			if top < 0 || box != top+1 || bottom != box+1 {
				t.Fatalf("the critical box was trimmed or split:\n%s", text)
			}
			if indicator < 0 || indicator > top {
				t.Errorf("the row that says the page was trimmed is not above the box:\n%s", text)
			}
			if lines[0] != "Restore preview" || lines[len(lines)-1] != "Type stored to go on:" {
				t.Errorf("the title or the question was cut:\n%s", text)
			}
		})
	}
}

// A page that only waits for Enter keeps its title too, and shows what the explanation has room for.
func TestAWaitingPageKeepsItsTitle(t *testing.T) {
	for _, w := range sizes {
		t.Run(w.name, func(t *testing.T) {
			s, _, frames := openChunks(t, windowOptions(w.width, w.height), "\n")
			longPage(s)
			if err := s.Wait(); err != nil {
				t.Fatal(err)
			}
			f := (*frames)[0]
			lines := contentLines(f)
			if lines[0] != "Restore preview" || lines[len(lines)-1] != trimRow || len(lines) != w.height-frameRows {
				t.Errorf("a waiting page must keep its title, fill the body and end in the row that says it was trimmed:\n%s", strings.Join(lines, "\n"))
			}
			if hint := f.rows[len(f.rows)-2]; !strings.Contains(hint, "Enter to finish") {
				t.Errorf("hint = %q", hint)
			}
		})
	}
}

// The error of a command that failed stays at the bottom of its page, and the title stays at the top.
func TestAFailedPageKeepsItsTitleAndTheError(t *testing.T) {
	for _, w := range sizes {
		t.Run(w.name, func(t *testing.T) {
			s, _, frames := openChunks(t, windowOptions(w.width, w.height), "\n")
			longPage(s)
			if !s.Fail("the purge was refused") {
				t.Fatal("Fail showed nothing")
			}
			lines := contentLines((*frames)[0])
			text := strings.Join(lines, "\n")
			if lines[0] != "Restore preview" || lines[len(lines)-1] != "! the purge was refused" || !strings.Contains(text, trimRow) {
				t.Errorf("the title, the error and the trimmed row are not all there:\n%s", text)
			}
		})
	}
}

// A list under a long explanation keeps the question, the cursor and its title.
func TestAListKeepsTheTitleAndTheCursorUnderALongPage(t *testing.T) {
	for _, w := range sizes {
		t.Run(w.name, func(t *testing.T) {
			var choices []Choice
			for i := 1; i <= 12; i++ {
				choices = append(choices, Choice{Name: fmt.Sprintf("acme/repo%02d", i)})
			}
			keys := &fakeKeys{}
			o := windowOptions(w.width, w.height)
			o.KeyInput = keys.toggle
			s, _, frames := openChunks(t, o, keyDownSeq, keyDownSeq, "\r")
			longPage(s)
			if _, err := s.MultiSelect(Multi{Label: "Repositories to protect", Choices: choices}); err != nil {
				t.Fatal(err)
			}
			for i, f := range *frames {
				lines := contentLines(f)
				text := strings.Join(lines, "\n")
				if lines[0] != "Restore preview" || !strings.Contains(text, "Repositories to protect") || !strings.Contains(text, fmt.Sprintf("> [ ] acme/repo%02d", i+1)) {
					t.Fatalf("frame %d lost its title, its question or its cursor:\n%s", i, text)
				}
				if len(lines) > w.height-frameRows {
					t.Fatalf("frame %d is %d rows of content, the body has %d", i, len(lines), w.height-frameRows)
				}
			}
		})
	}
}

// When the question alone is longer than the window there is nothing to trim in its favor: the last rows show, as for any page.
func TestAQuestionLongerThanTheWindowStillShowsItsLastRows(t *testing.T) {
	var choices []Choice
	for i := 1; i <= 30; i++ {
		choices = append(choices, Choice{Name: fmt.Sprintf("choice %02d", i)})
	}
	s, _, frames := openChunks(t, windowOptions(60, 20), "1\n")
	longPage(s)
	if _, err := s.Choose("Which?", choices, 0); err != nil {
		t.Fatal(err)
	}
	lines := contentLines((*frames)[0])
	if len(lines) != 14 || lines[len(lines)-1] != "Choose [1]:" {
		t.Errorf("the prompt is not the last row of a full body (%d rows):\n%s", len(lines), strings.Join(lines, "\n"))
	}
}

// restorePage is a page of the length of the restore preview: a paragraph, fields, a warning of two rows and a critical box of one row of
// text, with a question below. In the default window its body is 18 rows, and the page is three rows longer than that with seven fields.
func restorePage(s *Screen, fields int) {
	var rows []KV
	for i := 1; i <= fields; i++ {
		rows = append(rows, KV{fmt.Sprintf("Field %d", i), fmt.Sprintf("value %d", i)})
	}
	s.Page("Restore preview", Para("Restoring replaces the database of this instance."), Fields(rows...))
	s.Warn("The default branch on GitHub may be ahead of the restored canonical.")
	s.Critical("Keep the kit off this machine.")
}

// A page that is a few rows too long loses its blank rows, and none of its words: the blank row above the rule stays, and the others go
// before a row of text does.
func TestBlankRowsGoBeforeText(t *testing.T) {
	for _, tc := range []struct {
		name          string
		fields, blank int
	}{
		{"three rows too long", 7, 2},
		{"four rows too long, the blank above the rule is all that is left", 8, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, frames := openChunks(t, windowOptions(80, 24), "x\n")
			restorePage(s, tc.fields)
			if _, err := s.Ask("Type the instance id to confirm", ""); err != nil {
				t.Fatal(err)
			}
			lines := contentLines((*frames)[0])
			text := strings.Join(lines, "\n")
			if len(lines) != 18 || strings.Contains(text, trimRow) {
				t.Fatalf("%d rows, and a row that says the page was trimmed (%v), want 18 rows and none:\n%s", len(lines), strings.Contains(text, trimRow), text)
			}
			for _, want := range []string{"Restoring replaces the database of this instance.", fmt.Sprintf("Field %d  value %d", tc.fields, tc.fields), "! The default branch on GitHub may be ahead of the", "restored canonical.", "Keep the kit off this machine."} {
				if !strings.Contains(text, want) {
					t.Errorf("the page lost %q:\n%s", want, text)
				}
			}
			blank := 0
			for _, l := range lines {
				if l == "" {
					blank++
				}
			}
			if blank != tc.blank || lines[len(lines)-3] != "" || lines[len(lines)-2] != ruleRow(80) {
				t.Errorf("%d blank rows (want %d, one of them right above the rule):\n%s", blank, tc.blank, text)
			}
		})
	}
}

// When the blank rows are not enough, the explanation goes first, from the end, and a warning stays whole next to the critical box.
func TestAWarningOutlivesTheExplanationItIsTrimmedWith(t *testing.T) {
	s, _, frames := openChunks(t, windowOptions(60, 20), "x\n")
	s.Page("Restore preview", Para("First paragraph begins here. "+strings.Repeat("alpha beta gamma delta. ", 12)+"First ends."))
	s.Warn("Docker is also installed; Podman is used.")
	if _, err := s.Ask("Type the instance id to confirm", ""); err != nil {
		t.Fatal(err)
	}
	lines := contentLines((*frames)[0])
	text := strings.Join(lines, "\n")
	warn, marker := -1, -1
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "! Docker is also installed;"):
			warn = i
		case l == trimRow:
			marker = i
		}
	}
	if warn < 0 || !strings.Contains(squash(text), squash("Docker is also installed; Podman is used.")) || marker < 0 || marker > warn {
		t.Errorf("the warning must be whole, below the row that says the explanation was trimmed:\n%s", text)
	}
	if lines[0] != "Restore preview" || lines[len(lines)-1] != "Type the instance id to confirm:" {
		t.Errorf("the title or the question was cut:\n%s", text)
	}
}

// When the explanation is gone and the page still does not fit, whole warnings go, from the end, behind the row that says so: the title,
// the critical box and the question stay, and no warning is shown in part.
func TestWarningsGoWholeWhenTheExplanationIsGone(t *testing.T) {
	s, _, frames := openChunks(t, windowOptions(60, 20), "x\n")
	s.Page("Restore preview", Para("Short explanation."))
	for _, name := range []string{"A", "B", "C"} {
		s.Warn("Warn %s starts here and goes on for a good while, so that it takes three rows. %s ends.", name, name)
	}
	s.Critical("Keep the kit off this machine.")
	if _, err := s.Ask("Type stored to go on", ""); err != nil {
		t.Fatal(err)
	}
	lines := contentLines((*frames)[0])
	text := strings.Join(lines, "\n")
	squashed := squash(text)
	for _, name := range []string{"A", "B", "C"} {
		starts, ends := strings.Contains(squashed, squash("Warn "+name+" starts here")), strings.Contains(squashed, squash(name+" ends."))
		if starts != ends {
			t.Errorf("warning %s is shown in part:\n%s", name, text)
		}
	}
	if !strings.Contains(squashed, squash("Warn A starts here")) || strings.Contains(squashed, squash("Warn C starts here")) || !strings.Contains(text, trimRow) {
		t.Errorf("want the first warnings whole, the last one gone and the row that says so:\n%s", text)
	}
	if lines[0] != "Restore preview" || lines[len(lines)-1] != "Type stored to go on:" || !strings.Contains(text, "Keep the kit off this machine.") {
		t.Errorf("the title, the box or the question was cut:\n%s", text)
	}
}
