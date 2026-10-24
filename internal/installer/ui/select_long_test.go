package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// longChoices is the check runs of a repository that has many: a Select taller than the page.
func longChoices(n int) []Choice {
	choices := make([]Choice, n)
	for i := range choices {
		choices[i] = Choice{Name: fmt.Sprintf("CI / job%02d", i+1)}
	}
	return choices
}

// openLongSelect asks a Select of 25 rows on a page of the step list and a title, over downs arrow presses and a final Enter. frames[n] is
// the frame on screen before key n.
func openLongSelect(t *testing.T, color ColorLevel, def, downs int) (*Screen, *[]frame) {
	t.Helper()
	keys := &fakeKeys{}
	o := fullOptions(color)
	o.KeyInput = keys.toggle
	s, _, frames := openChunks(t, o, append(slices.Repeat([]string{keyDownSeq}, downs), "\r")...)
	s.Steps("Welcome", "GitHub", "Protection", "Finish")
	s.Step(3)
	s.Page("Test check", Para("CI publishes these check runs for acme/app."))
	if _, err := s.Select("Required test check for acme/app", longChoices(25), def); err != nil {
		t.Fatalf("Select: %v", err)
	}
	return s, frames
}

func selectLongDemo(def, downs int) func(*testing.T, ColorLevel) frame {
	return func(t *testing.T, color ColorLevel) frame {
		t.Helper()
		_, frames := openLongSelect(t, color, def, downs)
		return (*frames)[downs]
	}
}

// highlightedRows is the rows of the content that show the highlight of the list, as the name of the check run.
func highlightedRows(lines []string) []string {
	var rows []string
	for _, l := range lines {
		if strings.HasPrefix(l, "● ") {
			rows = append(rows, strings.TrimSpace(strings.TrimSuffix(l, "(default)"))) // the default row says so
		}
	}
	return rows
}

// A list taller than the page scrolls around its highlight, and the title of the page and the label of the question stay: the highlight is
// never out of sight, wherever it starts and whichever way it goes round.
func TestALongSelectKeepsTheHighlightTheLabelAndTheTitleInSight(t *testing.T) {
	const n = 25
	for _, def := range []int{0, 12, n - 1, -1} {
		t.Run(fmt.Sprintf("default %d", def), func(t *testing.T) {
			_, frames := openLongSelect(t, ColorNone, def, n+1)
			for i, f := range *frames {
				lines := contentLines(f)
				text := strings.Join(lines, "\n")
				if !strings.Contains(text, "Test check") || !strings.Contains(text, "Required test check for acme/app") {
					t.Errorf("frame %d lacks the title or the label:\n%s", i, text)
				}
				at := (def + i) % n // the row the highlight is on after i presses; from no row the first press goes to row 0
				if def < 0 {
					at = (i - 1 + n) % n
				}
				want := []string{fmt.Sprintf("● CI / job%02d", at+1)}
				if def < 0 && i == 0 {
					want = nil // nothing is highlighted before the first press
				}
				if got := highlightedRows(lines); !slices.Equal(got, want) {
					t.Errorf("frame %d highlights %q, want %q:\n%s", i, got, want, text)
				}
			}
		})
	}
}
