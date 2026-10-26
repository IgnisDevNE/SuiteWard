package ui

import (
	"strings"
	"testing"
)

// A page of lines with no title to keep (the checks of doctor) that is taller than the window shows its last rows, as a terminal would. It
// says so in its first row, as a summary does: nothing is cut without a word.

func TestAPageOfChecksThatIsTallerThanTheWindowSaysThatRowsAreHidden(t *testing.T) {
	s, _, frames := openChunks(t, windowOptions(80, 20), "\n")
	s.Steps("Doctor")
	for i := 1; i <= 16; i++ {
		s.Check("check %02d", i)
	}
	s.Say("\n16 ok, 0 warnings, 0 failures\n")
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	lines := contentLines((*frames)[0])
	text := strings.Join(lines, "\n")
	if len(lines) != 20-frameRows {
		t.Errorf("%d rows of content, want the body of %d, full:\n%s", len(lines), 20-frameRows, text)
	}
	if lines[0] != trimRow {
		t.Errorf("the first row is %q, want the row that says that rows are hidden:\n%s", lines[0], text)
	}
	if got := lines[len(lines)-1]; got != "16 ok, 0 warnings, 0 failures" {
		t.Errorf("the last row is %q, want the counts:\n%s", got, text)
	}
	if !strings.Contains(text, "check 16") || strings.Contains(text, "check 01") {
		t.Errorf("the page does not show the last checks and only them:\n%s", text)
	}
}

// A page that fits says nothing.
func TestAPageOfChecksThatFitsIsNotMarked(t *testing.T) {
	s, _, frames := openChunks(t, windowOptions(80, 20), "\n")
	s.Steps("Doctor")
	for i := 1; i <= 5; i++ {
		s.Check("check %02d", i)
	}
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	if text := strings.Join(contentLines((*frames)[0]), "\n"); strings.Contains(text, trimRow) {
		t.Errorf("a page that fits was marked as trimmed:\n%s", text)
	}
}
