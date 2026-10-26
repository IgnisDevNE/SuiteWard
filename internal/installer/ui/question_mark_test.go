package ui

import (
	"strings"
	"testing"
)

// A label that is a question ends with its own punctuation: its answer follows it without a colon ("Remove the copy? Yes", not "Remove
// the copy?: Yes"), on the page and in a typed question.

func TestAnAnsweredQuestionHasNoColonAfterItsQuestionMark(t *testing.T) {
	t.Run("a choice picked with the keys", func(t *testing.T) {
		keys := &fakeKeys{}
		s, out := openKeyScreen(t, "\r", keys)
		if got, err := s.Select("Remove the copy and its PATH entry?", []Choice{{Name: "Yes"}, {Name: "No"}}, 0); err != nil || got != 0 {
			t.Fatalf("Select = %d, %v", got, err)
		}
		if !strings.Contains(out.String(), "Remove the copy and its PATH entry? Yes") || strings.Contains(out.String(), "?:") {
			t.Errorf("the answer is not on the line of the question without a colon:\n%s", out.String())
		}
	})
	t.Run("a multiple choice picked with the keys", func(t *testing.T) {
		keys := &fakeKeys{}
		s, out, _ := openMultiKeys(t, keys, "\r")
		q := repoQuestion(1, 2, 0)
		q.Label = "Repositories to protect?"
		if got, err := s.MultiSelect(q); err != nil || len(got) != 1 {
			t.Fatalf("MultiSelect = %v, %v", got, err)
		}
		if !strings.Contains(out.String(), "Repositories to protect? acme/api") || strings.Contains(out.String(), "?:") {
			t.Errorf("the answer is not on the line of the question without a colon:\n%s", out.String())
		}
	})
	t.Run("a typed question", func(t *testing.T) {
		for _, tt := range []struct{ def, want string }{{"", "Proceed? "}, {"yes", "Proceed? [yes]: "}} {
			s, out := openScreen(t, plainOptions(), "\n")
			if _, err := s.Ask("Proceed?", tt.def); err != nil {
				t.Fatal(err)
			}
			if out.String() != tt.want {
				t.Errorf("Ask with the default %q wrote %q, want %q", tt.def, out.String(), tt.want)
			}
		}
	})
}
