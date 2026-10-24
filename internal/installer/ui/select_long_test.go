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
				for _, want := range []string{"Test check", "Required test check for acme/app"} {
					if !strings.Contains(text, want) {
						t.Errorf("frame %d lacks %q:\n%s", i, want, text)
					}
				}
				var marked []string
				for _, l := range lines {
					if strings.HasPrefix(l, "● ") {
						marked = append(marked, strings.TrimSpace(strings.TrimSuffix(l, "(default)"))) // the default row says so
					}
				}
				at := (def + i + n) % n // the row the highlight is on after i presses; from no row the first press goes to row 0
				if def < 0 {
					at = (i - 1 + n) % n
				}
				switch want := fmt.Sprintf("● CI / job%02d", at+1); {
				case def < 0 && i == 0:
					if len(marked) != 0 {
						t.Errorf("frame 0 highlights %q although nothing is", marked)
					}
				case len(marked) != 1 || marked[0] != want:
					t.Errorf("frame %d highlights %q, want only %q:\n%s", i, marked, want, text)
				}
			}
		})
	}
}
