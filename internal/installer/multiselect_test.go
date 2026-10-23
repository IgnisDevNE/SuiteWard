package installer

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// A multiple choice is resolved as Select is: the flag, then the answers file, then the defaults when there is no terminal to ask. A
// plain screen asks the typed question word for word, and a full screen draws the list to be picked with the keys.

// repoMulti is the repositories question of the GitHub flow, as a Multi: its typed text is the one that exists today.
func repoMulti() ui.Multi {
	return ui.Multi{
		Label:    "Repositories to protect",
		Hint:     fmt.Sprintf("numbers or owner/name, comma-separated, at most %d", MaxRepositories),
		Choices:  []ui.Choice{{Name: "acme/api"}, {Name: "acme/web"}, {Name: "acme/docs"}},
		Defaults: []int{0, 1, 2},
		Min:      1,
		Max:      MaxRepositories,
	}
}

func TestAMultipleChoiceIsAnsweredByTheFlagTheAnswersFileOrTheDefaults(t *testing.T) {
	for _, tc := range []struct {
		name            string
		flags, answers  map[string]string
		terminal        bool
		mutate          func(*ui.Multi)
		want            []string
		wantErrContains string
	}{
		{"a flag, numbers and names, in the order typed", map[string]string{"repositories": "acme/web, 1"}, map[string]string{"repositories": "acme/docs"}, true, nil, []string{"acme/web", "acme/api"}, ""},
		{"the answers file", nil, map[string]string{"repositories": "3"}, true, nil, []string{"acme/docs"}, ""},
		{"no terminal takes the defaults", nil, nil, false, nil, []string{"acme/api", "acme/web", "acme/docs"}, ""},
		{"no terminal and no defaults asks for the flag", nil, nil, false, func(m *ui.Multi) { m.Defaults = nil }, nil,
			"Repositories to protect (numbers or owner/name, comma-separated, at most 20) is required: pass --repositories"},
		{"a flag that names no choice", map[string]string{"repositories": "acme/elsewhere"}, nil, true, nil, nil, `"acme/elsewhere" is not one of the choices`},
		{"a flag with too few", map[string]string{"repositories": " , "}, nil, true, nil, nil, "0 chosen"},
		{"a flag with too many", map[string]string{"repositories": "1,2,3"}, nil, true, func(m *ui.Multi) { m.Max, m.Defaults = 2, nil }, nil, "3 chosen: pick between 1 and 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &keyConsole{out: &bytes.Buffer{}}
			br := bufio.NewReader(strings.NewReader("\x1b[B \r"))
			scr, err := c.open(context.Background(), br, "v0", false)
			if err != nil {
				t.Fatal(err)
			}
			r := newTestResolver(t, tc.flags, tc.answers, Terminal{In: br, Out: scr, IsTerminal: tc.terminal, ReadPassword: func() ([]byte, error) { return nil, nil }})
			m := repoMulti()
			if tc.mutate != nil {
				tc.mutate(&m)
			}
			got, err := r.MultiSelect("repositories", m)
			if tc.wantErrContains != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Fatalf("MultiSelect = %v, %v; want an error with %q", got, err, tc.wantErrContains)
				}
			} else if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("MultiSelect = %v, %v; want %v", got, err, tc.want)
			}
			if c.enabled != 0 {
				t.Error("the console was put in key mode for a question nobody asked")
			}
			if strings.Contains(c.out.String(), "Repositories to protect") {
				t.Errorf("a question was drawn:\n%s", c.out.String())
			}
		})
	}
}

// A plain screen on a terminal (--plain, NO_COLOR, a small window) asks the typed question of today, word for word, and prints no list of
// its own: the flow prints the numbered list it always printed.
func TestThePlainMultipleChoiceIsTheTypedQuestionItAlwaysWas(t *testing.T) {
	r, c := keyResolver(t, true, nil, nil, "1,acme/web\n")
	got, err := r.MultiSelect("repositories", repoMulti())
	if err != nil || !slices.Equal(got, []string{"acme/api", "acme/web"}) {
		t.Fatalf("MultiSelect = %v, %v; want the typed answer", got, err)
	}
	if want := "Repositories to protect (numbers or owner/name, comma-separated, at most 20) [acme/api,acme/web,acme/docs]: "; c.out.String() != want {
		t.Errorf("the plain output is %q, want %q", c.out.String(), want)
	}
	if c.enabled != 0 {
		t.Error("the console was put in key mode on a plain screen")
	}
}

func TestThePlainMultipleChoiceTakesTheDefaultsOnEnter(t *testing.T) {
	r, _ := keyResolver(t, true, nil, nil, "\n")
	if got, err := r.MultiSelect("repositories", repoMulti()); err != nil || len(got) != 3 {
		t.Fatalf("MultiSelect = %v, %v; want the three defaults", got, err)
	}
}

func TestAMultipleChoiceIsPickedWithTheKeysOnAFullScreen(t *testing.T) {
	r, c := keyResolver(t, false, nil, nil, "\x1b[B \r") // down, space (unpicks the second row), Enter
	got, err := r.MultiSelect("repositories", repoMulti())
	if err != nil || !slices.Equal(got, []string{"acme/api", "acme/docs"}) {
		t.Fatalf("MultiSelect = %v, %v; want the rows that were left picked", got, err)
	}
	if c.enabled != 1 || c.restored != 1 {
		t.Errorf("the console was put in key mode %d times and back %d times, want once each", c.enabled, c.restored)
	}
	if want := "Repositories to protect: acme/api, acme/docs"; !strings.Contains(c.out.String(), want) {
		t.Errorf("the page lacks the answered question %q:\n%s", want, c.out.String())
	}
}

// A question that cannot be answered is a bug of the caller, and nothing is asked.
func TestAnImpossibleMultipleChoiceIsAnErrorAndAsksNothing(t *testing.T) {
	for name, mutate := range map[string]func(*ui.Multi){
		"a default off the list":      func(m *ui.Multi) { m.Defaults = []int{3} },
		"no choices":                  func(m *ui.Multi) { m.Choices = nil; m.Defaults = nil },
		"a minimum above max":         func(m *ui.Multi) { m.Min, m.Max = 3, 2 },
		"more defaults than the max":  func(m *ui.Multi) { m.Max = 2 },
		"fewer defaults than the min": func(m *ui.Multi) { m.Min, m.Defaults = 3, []int{0} },
	} {
		t.Run(name, func(t *testing.T) {
			for _, plain := range []bool{false, true} {
				r, c := keyResolver(t, plain, nil, nil, "")
				opened := c.out.Len() // a full screen has drawn its frame by now
				m := repoMulti()
				mutate(&m)
				if _, err := r.MultiSelect("repositories", m); err == nil || !strings.Contains(err.Error(), "repositories") {
					t.Fatalf("plain=%v: MultiSelect = %v, want an error that names the question", plain, err)
				}
				if c.enabled != 0 || c.out.Len() != opened {
					t.Errorf("plain=%v: something was asked: enabled %d, output %q", plain, c.enabled, c.out.String())
				}
			}
		})
	}
}
