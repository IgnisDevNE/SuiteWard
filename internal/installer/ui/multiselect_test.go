package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

const (
	keyDownSeq = "\x1b[B"
	keyUpSeq   = "\x1b[A"
)

var repoChoices = []Choice{{"acme/api", ""}, {"acme/web", "the website"}, {"acme/docs", ""}}

// repoQuestion is a question over repoChoices with the given bounds (a max of 0 has no upper bound) and the rows picked at first.
func repoQuestion(lo, hi int, defaults ...int) Multi {
	return Multi{Label: "Repositories to protect", Choices: repoChoices, Defaults: defaults, Min: lo, Max: hi}
}

func openMultiKeys(t *testing.T, keys *fakeKeys, chunks ...string) (*Screen, *bytes.Buffer, *[]frame) {
	t.Helper()
	o := fullOptions(ColorNone)
	o.KeyInput = keys.toggle
	return openChunks(t, o, chunks...)
}

func TestMultiSelectKeys(t *testing.T) {
	tests := []struct {
		name   string
		q      Multi
		chunks []string
		want   []int
	}{
		{"Enter takes the preselected rows", repoQuestion(1, 2, 0), []string{"\r"}, []int{0}},
		{"down, space, Enter adds the second row", repoQuestion(1, 2, 0), []string{keyDownSeq, " ", "\r"}, []int{0, 1}},
		{"down, space, down, space, Enter", repoQuestion(1, 0), []string{keyDownSeq, " ", keyDownSeq, " ", "\r"}, []int{1, 2}},
		{"space on a picked row unpicks it", repoQuestion(0, 0, 0, 1), []string{" ", "\r"}, []int{1}},
		{"nothing picked is fine when none is needed", repoQuestion(0, 0, 0), []string{" ", "\r"}, nil},
		{"up at the first row goes round to the last", repoQuestion(1, 0), []string{keyUpSeq, " ", "\r"}, []int{2}},
		{"down at the last row goes round to the first", repoQuestion(1, 0), []string{keyDownSeq, keyDownSeq, keyDownSeq, " ", "\r"}, []int{0}},
		{"Esc, letters and other arrows change nothing", repoQuestion(1, 0, 1), []string{"x", "\x1b", "\x1b[C", "\r"}, []int{1}},
		{"an Enter below the minimum is refused and the list stays open", repoQuestion(1, 0), []string{"\r", keyDownSeq, " ", "\r"}, []int{1}},
		{"a pick above the maximum is refused", repoQuestion(1, 1, 0), []string{keyDownSeq, " ", "\r"}, []int{0}},
		{"LF is Enter", repoQuestion(1, 2, 0), []string{"\n"}, []int{0}},
		{"the answer is in the order of the list", repoQuestion(1, 0), []string{keyDownSeq, keyDownSeq, " ", keyUpSeq, keyUpSeq, " ", "\r"}, []int{0, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := &fakeKeys{}
			s, _, _ := openMultiKeys(t, keys, tt.chunks...)
			got, err := s.MultiSelect(tt.q)
			if err != nil || !slices.Equal(got, tt.want) {
				t.Fatalf("MultiSelect = %v, %v; want %v", got, err, tt.want)
			}
			keys.balanced(t, 1)
		})
	}
}

// Each row has a checkbox and the cursor is a mark, so the list reads without color; the answer replaces the list on the question's line.
func TestMultiSelectShowsTheBoxesTheCursorAndThenTheAnswer(t *testing.T) {
	keys := &fakeKeys{}
	s, out, frames := openMultiKeys(t, keys, keyDownSeq, " ", "\r")
	if got, err := s.MultiSelect(repoQuestion(1, 2, 0)); err != nil || !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("MultiSelect = %v, %v", got, err)
	}
	wantLines(t, (*frames)[0],
		"Repositories to protect",
		"> [x] acme/api",
		"  [ ] acme/web    the website",
		"  [ ] acme/docs")
	wantLines(t, (*frames)[1],
		"Repositories to protect",
		"  [x] acme/api",
		"> [ ] acme/web    the website",
		"  [ ] acme/docs")
	wantLines(t, (*frames)[2],
		"Repositories to protect",
		"  [x] acme/api",
		"> [x] acme/web    the website",
		"  [ ] acme/docs")
	for i, f := range *frames {
		hint := f.rows[len(f.rows)-2]
		if !strings.Contains(hint, "Space toggle") || !strings.Contains(hint, "Enter confirm") || !strings.Contains(hint, "Ctrl-C quit") {
			t.Errorf("frame %d: the hint of the open list is %q", i, hint)
		}
		if f.cursor {
			t.Errorf("frame %d leaves the cursor shown", i)
		}
	}
	wantLines(t, snapshot(s), "Repositories to protect: acme/api, acme/web")
	if hint := snapshot(s).rows[len(snapshot(s).rows)-2]; strings.Contains(hint, "toggle") {
		t.Errorf("the hint of the list is still there after the answer: %q", hint)
	}
	mustClose(t, s)
	if got, want := afterLastLeave(out.String()), "Repositories to protect: acme/api, acme/web\n"; got != want {
		t.Errorf("the normal screen received %q, want %q", got, want)
	}
}

func TestMultiSelectSaysWhenNothingWasPicked(t *testing.T) {
	keys := &fakeKeys{}
	s, _, _ := openMultiKeys(t, keys, " ", "\r")
	if got, err := s.MultiSelect(repoQuestion(0, 0, 0)); err != nil || len(got) != 0 {
		t.Fatalf("MultiSelect = %v, %v", got, err)
	}
	wantLines(t, snapshot(s), "Repositories to protect: none")
}

// The caller's bounds are enforced by the list, and the reason is on the page, under the list, until the next key.
func TestMultiSelectExplainsAnEnterOrAPickItRefuses(t *testing.T) {
	keys := &fakeKeys{}
	s, _, frames := openMultiKeys(t, keys, " ", "\r", keyDownSeq, " ", "\r")
	if got, err := s.MultiSelect(repoQuestion(1, 2, 0)); err != nil || !slices.Equal(got, []int{1}) {
		t.Fatalf("MultiSelect = %v, %v; want [1]", got, err)
	}
	wantLines(t, (*frames)[2], // after the space that unpicked the only row, and the Enter that was refused
		"Repositories to protect",
		"> [ ] acme/api",
		"  [ ] acme/web    the website",
		"  [ ] acme/docs",
		"! Pick between 1 and 2.")
	if text := strings.Join(contentLines((*frames)[3]), "\n"); strings.Contains(text, "Pick between") {
		t.Errorf("the note stays after the next key:\n%s", text)
	}

	s, _, frames = openMultiKeys(t, keys, keyDownSeq, " ", "\r")
	if _, err := s.MultiSelect(repoQuestion(1, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if text := strings.Join(contentLines((*frames)[2]), "\n"); !strings.Contains(text, "! Pick exactly 1.") || !strings.Contains(text, "> [ ] acme/web") {
		t.Errorf("a pick beyond the maximum is not explained, or was taken:\n%s", text)
	}
}

// At 80x24 the body has 18 rows. A list longer than the page scrolls with the cursor, so the highlighted row is always on the screen.
func TestMultiSelectKeepsTheCursorOnScreenInALongList(t *testing.T) {
	var choices []Choice
	for i := 1; i <= 40; i++ {
		choices = append(choices, Choice{Name: fmt.Sprintf("acme/repo%02d", i)})
	}
	chunks := slices.Repeat([]string{keyDownSeq}, 25)
	chunks = append(chunks, " ", "\r")
	keys := &fakeKeys{}
	s, _, frames := openMultiKeys(t, keys, chunks...)
	s.Page("Repositories", Para("The App can reach these repositories."))
	got, err := s.MultiSelect(Multi{Label: "Repositories to protect", Choices: choices, Min: 1})
	if err != nil || !slices.Equal(got, []int{25}) {
		t.Fatalf("MultiSelect = %v, %v; want [25]", got, err)
	}
	for i, f := range *frames {
		if len(f.rows) != 24 {
			t.Fatalf("frame %d has %d rows, want 24", i, len(f.rows))
		}
		want := fmt.Sprintf("> [ ] acme/repo%02d", i+1)
		if i == 26 {
			want = "> [x] acme/repo26"
		}
		if text := content(f); !strings.Contains(text, want) {
			t.Fatalf("frame %d does not show the cursor row %q:\n%s", i, want, text)
		}
	}
	last := content((*frames)[25])
	for _, want := range []string{"↑", "more above", "↓", "more below", "Repositories to protect"} {
		if !strings.Contains(last, want) {
			t.Errorf("the scrolled list lacks %q:\n%s", want, last)
		}
	}
	if strings.Contains(last, "acme/repo01") {
		t.Errorf("the first row is still on screen after the cursor moved far below it:\n%s", last)
	}
}

// Where keys cannot be read the question is the typed one: the rows are numbered and the answer is numbers or names, comma-separated.
func TestMultiSelectAsksByTypingWhenKeyInputIsNotAvailable(t *testing.T) {
	q := repoQuestion(1, 2, 0)
	for name, open := range map[string]func(input string) (*Screen, *bytes.Buffer, *fakeKeys){
		"there is no toggle": func(input string) (*Screen, *bytes.Buffer, *fakeKeys) {
			s, out := openScreen(t, fullOptions(ColorNone), input)
			return s, out, &fakeKeys{}
		},
		"the toggle fails": func(input string) (*Screen, *bytes.Buffer, *fakeKeys) {
			keys := &fakeKeys{enableErr: errors.New("not a console")}
			o := fullOptions(ColorNone)
			o.KeyInput = keys.toggle
			s, out := openScreen(t, o, input)
			return s, out, keys
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, out, keys := open("2,ACME/docs\n")
			got, err := s.MultiSelect(q)
			if err != nil || !slices.Equal(got, []int{1, 2}) {
				t.Fatalf("MultiSelect = %v, %v; want [1 2] (a number and a name, whatever the case)", got, err)
			}
			rows := strings.Join(snapshot(s).rows, "\n")
			for _, want := range []string{
				"Repositories to protect",
				"1) acme/api     (default)",
				"2) acme/web    the website",
				"3) acme/docs",
			} {
				if !strings.Contains(rows, want) {
					t.Errorf("the page lacks %q:\n%s", want, rows)
				}
			}
			// The answer sits on the line of the question: it wraps with it, and only the frame's border and padding come between them.
			if want := squash("Pick (numbers or names, comma-separated, at most 2) [1]: 2,ACME/docs"); !strings.Contains(squash(content(snapshot(s))), want) {
				t.Errorf("the answer is not on the line of the question:\n%s", content(snapshot(s)))
			}
			keys.balanced(t, 0)
			mustClose(t, s)
			if strings.Contains(afterLastLeave(out.String()), esc) {
				t.Error("the normal screen received an escape sequence")
			}
		})
	}
}

func TestTypedMultiSelectTakesTheDefaultsOnEnterAndAsksAgainOnABadAnswer(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "9\n1,2,3\n\n")
	got, err := s.MultiSelect(repoQuestion(1, 2, 0))
	if err != nil || !slices.Equal(got, []int{0}) {
		t.Fatalf("MultiSelect = %v, %v; want the default after two bad answers", got, err)
	}
	rows := strings.Join(snapshot(s).rows, "\n")
	for _, want := range []string{`"9" is not one of the choices.`, "3 chosen: pick between 1 and 2."} {
		if !strings.Contains(rows, want) {
			t.Errorf("the page does not explain the answer that was refused (%q):\n%s", want, rows)
		}
	}
}

func TestTypedMultiSelectAllowsNoAnswerWhenNoneIsNeeded(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "\n")
	if got, err := s.MultiSelect(repoQuestion(0, 0)); err != nil || len(got) != 0 {
		t.Fatalf("MultiSelect = %v, %v; want nothing picked", got, err)
	}
}

func TestTypedMultiSelectEndsWithTheEndOfTheInput(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	if _, err := s.MultiSelect(repoQuestion(1, 0)); !errors.Is(err, io.EOF) {
		t.Fatalf("MultiSelect = %v, want the end of the input", err)
	}
}

// Plain mode has no list to draw and no keys: the typed question, with no escape sequence.
func TestMultiSelectInPlainModeIsTheTypedQuestion(t *testing.T) {
	keys := &fakeKeys{}
	o := plainOptions()
	o.KeyInput = keys.toggle
	s, out := openScreen(t, o, "1,acme/web\n")
	got, err := s.MultiSelect(repoQuestion(1, 2, 0))
	if err != nil || !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("MultiSelect = %v, %v", got, err)
	}
	want := "Repositories to protect\n" +
		"1) acme/api     (default)\n" +
		"2) acme/web    the website\n" +
		"3) acme/docs   \n" +
		"Pick (numbers or names, comma-separated, at most 2) [1]: "
	if out.String() != want {
		t.Errorf("plain output differs.\ngot:\n%q\nwant:\n%q", out.String(), want)
	}
	keys.balanced(t, 0)
}

func TestMultiSelectEndsWithTheContextAndReadsNothingWhenItIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	keys := &fakeKeys{}
	in := &readCounter{}
	o := fullOptions(ColorNone)
	o.Out, o.In, o.Context, o.KeyInput = io.Discard, in, ctx, keys.toggle
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.MultiSelect(repoQuestion(1, 0)); !errors.Is(err, context.Canceled) {
		t.Fatalf("MultiSelect = %v, want the context's error", err)
	}
	if in.reads != 0 {
		t.Errorf("the input was read %d times after the context was done", in.reads)
	}
	keys.balanced(t, 0)
}

func TestMultiSelectThatLosesItsInputPutsTheConsoleBack(t *testing.T) {
	keys := &fakeKeys{}
	s, out, _ := openMultiKeys(t, keys, keyDownSeq, " ") // the input ends before Enter
	if _, err := s.MultiSelect(repoQuestion(1, 0)); !errors.Is(err, io.EOF) {
		t.Fatalf("MultiSelect = %v, want the end of the input", err)
	}
	keys.balanced(t, 1)
	mustClose(t, s)
	if got, want := afterLastLeave(out.String()), "Repositories to protect\n[ ] acme/api\n[x] acme/web    the website\n[ ] acme/docs\n"; got != want {
		t.Errorf("the normal screen received %q, want the question as it stood: %q", got, want)
	}
}

func TestMultiSelectReportsAConsoleThatCannotBePutBack(t *testing.T) {
	broken := errors.New("the console went away")
	keys := &fakeKeys{restoreErr: broken}
	s, _, _ := openMultiKeys(t, keys, "\r")
	if _, err := s.MultiSelect(repoQuestion(0, 0)); !errors.Is(err, broken) {
		t.Fatalf("MultiSelect = %v, want the failure to restore the console", err)
	}
}

// With ISIG kept, Ctrl-C is a signal; a console that delivers it as a character and raises no signal is reported, as Select reports it.
func TestMultiSelectReportsACtrlCThatArrivesAsACharacter(t *testing.T) {
	keys := &fakeKeys{}
	s, _, _ := openMultiKeys(t, keys, "\x03")
	_, err := s.MultiSelect(repoQuestion(1, 0))
	if err == nil || !strings.Contains(err.Error(), "Ctrl-C") || errors.Is(err, context.Canceled) {
		t.Fatalf("MultiSelect = %v, want an error that says Ctrl-C ended it", err)
	}
	keys.balanced(t, 1)
}

func TestMultiSelectRefusesAQuestionThatCannotBeAnswered(t *testing.T) {
	keys := &fakeKeys{}
	s, _, _ := openMultiKeys(t, keys, "\r")
	for name, q := range map[string]Multi{
		"no choices":                  {Label: "L"},
		"a default off the list":      repoQuestion(0, 0, 3),
		"a negative default":          repoQuestion(0, 0, -1),
		"a default twice":             repoQuestion(0, 0, 1, 1),
		"a negative minimum":          repoQuestion(-1, 0),
		"a minimum above the max":     repoQuestion(3, 2),
		"a minimum above the rows":    repoQuestion(4, 0),
		"a negative maximum":          repoQuestion(0, -1),
		"more defaults than the max":  repoQuestion(0, 2, 0, 1, 2),
		"fewer defaults than the min": repoQuestion(2, 0, 0),
	} {
		t.Run(name, func(t *testing.T) {
			if err := q.Validate(); err == nil {
				t.Error("Validate accepted it")
			}
			if _, err := s.MultiSelect(q); err == nil {
				t.Error("MultiSelect accepted it")
			}
		})
	}
	for name, q := range map[string]Multi{
		"defaults within the bounds":     repoQuestion(1, 2, 0, 1),
		"no defaults, so the user picks": repoQuestion(1, 2),
		"no upper bound":                 repoQuestion(1, 0, 0, 1, 2),
	} {
		if err := q.Validate(); err != nil {
			t.Errorf("Validate refused a question that can be answered (%s): %v", name, err)
		}
	}
	keys.balanced(t, 0)
}

func TestMultiParse(t *testing.T) {
	tests := []struct {
		name   string
		q      Multi
		answer string
		want   []int
		err    string
	}{
		{"numbers", repoQuestion(1, 0), "1,3", []int{0, 2}, ""},
		{"names, whatever the case", repoQuestion(1, 0), "ACME/docs, acme/API", []int{2, 0}, ""},
		{"a number and a name", repoQuestion(1, 0), "2, acme/docs", []int{1, 2}, ""},
		{"the typed order is kept", repoQuestion(1, 0), "3,1,2", []int{2, 0, 1}, ""},
		{"a row twice is one row", repoQuestion(1, 0), "1,acme/api,1", []int{0}, ""},
		{"empty items are skipped", repoQuestion(1, 0), " , 2 ,,", []int{1}, ""},
		{"nothing is fine when none is needed", repoQuestion(0, 0), "", nil, ""},
		{"a number off the list", repoQuestion(1, 0), "4", nil, `"4" is not one of the choices`},
		{"zero is not a row", repoQuestion(1, 0), "0", nil, `"0" is not one of the choices`},
		{"a name off the list", repoQuestion(1, 0), "acme/other", nil, `"acme/other" is not one of the choices`},
		{"below the minimum", repoQuestion(1, 0), " , ", nil, "0 chosen: pick at least 1"},
		{"above the maximum", repoQuestion(1, 2), "1,2,3", nil, "3 chosen: pick between 1 and 2"},
		{"exactly one", repoQuestion(1, 1), "1,2", nil, "2 chosen: pick exactly 1"},
		{"at most", repoQuestion(0, 2), "1,2,3", nil, "3 chosen: pick at most 2"},
		{"no upper bound", repoQuestion(1, 0), "1,2,3", []int{0, 1, 2}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.q.Parse(tt.answer)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("Parse(%q) = %v, %v; want an error with %q", tt.answer, got, err, tt.err)
				}
				return
			}
			if err != nil || !slices.Equal(got, tt.want) {
				t.Fatalf("Parse(%q) = %v, %v; want %v", tt.answer, got, err, tt.want)
			}
		})
	}
}

// The text of the typed question is the caller's own, so that a question that was typed before can be a Multi without changing a word.
func TestMultiTypedLabel(t *testing.T) {
	q := repoQuestion(1, 20)
	if got, want := q.TypedLabel(), "Repositories to protect (numbers or names, comma-separated, at most 20)"; got != want {
		t.Errorf("TypedLabel = %q, want %q", got, want)
	}
	q.Hint = "numbers or owner/name, comma-separated, at most 20"
	if got, want := q.TypedLabel(), "Repositories to protect (numbers or owner/name, comma-separated, at most 20)"; got != want {
		t.Errorf("TypedLabel with a hint = %q, want %q", got, want)
	}
	if got, want := repoQuestion(1, 0).TypedLabel(), "Repositories to protect (numbers or names, comma-separated)"; got != want {
		t.Errorf("TypedLabel with no maximum = %q, want %q", got, want)
	}
}
