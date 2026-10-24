package ui

import (
	"strings"
	"testing"
)

// A Select that has no default highlights nothing: Enter is refused with a note until an arrow has picked a row.

const noPickNote = "Pick one with the arrows."

func TestASelectWithNoDefaultRefusesEnterUntilARowIsPicked(t *testing.T) {
	keys := &fakeKeys{}
	o := fullOptions(ColorNone)
	o.KeyInput = keys.toggle
	s, _, frames := openChunks(t, o, "\r", keyDownSeq, "\r")
	got, err := s.Select(selectLabel, testChoices, -1)
	if err != nil || got != 0 {
		t.Fatalf("Select = %d, %v; want 0 (the first row, one key down from none)", got, err)
	}
	text := func(i int) string { return strings.Join(contentLines((*frames)[i]), "\n") }
	if f := text(0); strings.Contains(f, "●") || strings.Contains(f, "(default)") || strings.Count(f, "○") != 2 || strings.Contains(f, noPickNote) {
		t.Errorf("the list at the start has a highlighted row, a default or a note:\n%s", f)
	}
	if hint := (*frames)[0].rows[len((*frames)[0].rows)-2]; !strings.Contains(hint, "to choose") || strings.Contains(hint, "default") {
		t.Errorf("the hint is %q", hint)
	}
	if f := text(1); !strings.Contains(f, noPickNote) || strings.Contains(f, "●") {
		t.Errorf("the Enter at the start was not refused with the note, with nothing highlighted:\n%s", f)
	}
	if f := text(2); strings.Contains(f, noPickNote) || !strings.Contains(f, "● co-located") {
		t.Errorf("a key down does not highlight the first row and take the note away:\n%s", f)
	}
	keys.balanced(t, 1)
}

func TestAKeyUpFromNoDefaultGoesToTheLastRow(t *testing.T) {
	keys := &fakeKeys{}
	s, _ := openKeyScreen(t, keyUpSeq+"\r", keys)
	if got, err := s.Select(selectLabel, testChoices, -1); err != nil || got != 1 {
		t.Fatalf("Select = %d, %v; want 1 (the last row)", got, err)
	}
}

// Without keys the question is numbered, and an empty answer, which has no default to take, asks again.
func TestANumberedQuestionWithNoDefaultAsksAgainOnAnEmptyAnswer(t *testing.T) {
	s, _, frames := openChunks(t, fullOptions(ColorNone), "\n", "x\n", "2\n")
	got, err := s.Select(selectLabel, testChoices, -1)
	if err != nil || got != 1 {
		t.Fatalf("Select = %d, %v; want 1 (typed 2)", got, err)
	}
	first := strings.Join(contentLines((*frames)[0]), "\n")
	if !strings.Contains(first, "Choose:") || strings.Contains(first, "(default)") || strings.Contains(first, "Choose [") {
		t.Errorf("the numbered question has a default:\n%s", first)
	}
	if hint := (*frames)[0].rows[len((*frames)[0].rows)-2]; strings.Contains(hint, "default") {
		t.Errorf("the hint is %q, want it not to say that Enter takes a default", hint)
	}
	if again := strings.Join(contentLines((*frames)[1]), "\n"); !strings.Contains(again, "Enter a number from 1 to 2.") {
		t.Errorf("an empty answer was not refused:\n%s", again)
	}
}

func TestAPlainQuestionWithNoDefaultAsksAgainOnAnEmptyAnswer(t *testing.T) {
	s, out := openScreen(t, plainOptions(), "\n2\n")
	if got, err := s.Select(selectLabel, testChoices, -1); err != nil || got != 1 {
		t.Fatalf("Select = %d, %v; want 1", got, err)
	}
	want := selectLabel + "\n1) co-located   alongside the agent, on this PC\n2) isolated     on a separate host\nChoose: Enter a number from 1 to 2.\nChoose: "
	if got := out.String(); got != want {
		t.Errorf("the plain question is\n%q\nwant\n%q", got, want)
	}
}
