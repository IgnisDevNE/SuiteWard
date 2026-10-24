package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The frames of the toolkit that the flow pages are built with, one per element, for a reviewer to read:
//
//	testdata/page.*.golden              a page: the title, a paragraph, fields, a warning, a critical box, and the question set apart (80x32)
//	testdata/page-80x24.*.golden        the same page in the default window: the explanation is trimmed from the end, behind one row
//	testdata/page-60x20.*.golden        and in the smallest window: the title, the critical box and the question stay
//	testdata/multiselect.*.golden       a multiple choice with the cursor on the third row and the note of a pick that was refused
//	testdata/multiselect-long.*.golden  a list longer than the page, scrolled to its cursor
//	testdata/select-long-default.*.golden  a Select of 25 rows (80x24) with the first highlighted: the title, the label and the first rows
//	testdata/select-long-none.*.golden     the same with no default and one arrow down: the first row is highlighted and still in sight
//
// The "off" frames show the layout without color; the "truecolor" ones show which element is which tone.

// toolkitOptions is a taller terminal than the default 80x24: a page of the toolkit holds more than one question's text.
func toolkitOptions(color ColorLevel) Options {
	o := fullOptions(color)
	o.Height = 32
	return o
}

// pageDemo is the restore preview page in a terminal of width x height.
func pageDemo(width, height int) func(*testing.T, ColorLevel) frame {
	return func(t *testing.T, color ColorLevel) frame {
		t.Helper()
		o := fullOptions(color)
		o.Width, o.Height = width, height
		s, _, frames := openChunks(t, o, "abc123\n")
		s.Steps("Welcome", "Backup", "Restore", "Verify", "Finish")
		s.Step(3)
		s.Page("Restore preview",
			Para("Restoring replaces the database and the artifacts of this instance with the backup's."),
			Fields(KV{"Instance", "da0aff25"}, KV{"Backup", "suiteward-backup-20261022.tar.age"}, KV{"Tier", "co-located"}))
		s.Warn("The default branch on GitHub may be ahead of the restored canonical.")
		s.Critical("The recovery kit " + `C:\Users\magal\Documents\suiteward-recovery-kit-da0aff25.txt` + " opens every backup. Copy it off this machine now and delete this copy.")
		if _, err := s.Ask("Type the instance id to confirm", ""); err != nil {
			t.Fatalf("Ask: %v", err)
		}
		return (*frames)[0]
	}
}

func multiDemo(t *testing.T, color ColorLevel) frame {
	t.Helper()
	keys := &fakeKeys{}
	o := toolkitOptions(color)
	o.KeyInput = keys.toggle
	s, _, frames := openChunks(t, o, keyDownSeq, keyDownSeq, " ", "\r")
	s.Steps("Welcome", "GitHub", "Protection", "Finish")
	s.Step(3)
	s.Page("Repositories", Para("The App can reach these repositories. SuiteWard protects the ones you pick."))
	got, err := s.MultiSelect(Multi{
		Label: "Repositories to protect",
		Choices: []Choice{
			{"acme/api", "private"}, {"acme/web", "public, the website"}, {"acme/docs", "public"}, {"acme/infra", "private"},
		},
		Defaults: []int{0, 1},
		Min:      1,
		Max:      2,
	})
	if err != nil || len(got) != 2 {
		t.Fatalf("MultiSelect = %v, %v", got, err)
	}
	return (*frames)[3] // after the two downs and the space that was refused: two rows are picked already, and the most is two
}

func multiLongDemo(t *testing.T, color ColorLevel) frame {
	t.Helper()
	var choices []Choice
	for i := 1; i <= 40; i++ {
		choices = append(choices, Choice{Name: fmt.Sprintf("acme/repo%02d", i)})
	}
	keys := &fakeKeys{}
	o := fullOptions(color)
	o.KeyInput = keys.toggle
	chunks := append(slices.Repeat([]string{keyDownSeq}, 12), " ", "\r")
	s, _, frames := openChunks(t, o, chunks...)
	s.Steps("Welcome", "GitHub", "Protection", "Finish")
	s.Step(3)
	s.Page("Repositories", Para("The App can reach 40 repositories."))
	if _, err := s.MultiSelect(Multi{Label: "Repositories to protect", Choices: choices, Defaults: []int{0}, Min: 1, Max: 20}); err != nil {
		t.Fatalf("MultiSelect: %v", err)
	}
	return (*frames)[13] // twelve rows down, and the space that picked the row under the cursor
}

func TestToolkitGoldens(t *testing.T) {
	demos := map[string]func(*testing.T, ColorLevel) frame{
		"page": pageDemo(80, 32), "page-80x24": pageDemo(80, 24), "page-60x20": pageDemo(60, 20),
		"multiselect": multiDemo, "multiselect-long": multiLongDemo,
		"select-long-default": selectLongDemo(0, 0), "select-long-none": selectLongDemo(-1, 1),
	}
	for name, color := range map[string]ColorLevel{"off": ColorNone, "truecolor": ColorTrue} {
		for demo, build := range demos {
			t.Run(demo+"/"+name, func(t *testing.T) { checkGolden(t, demo+"."+name, goldenText(build(t, color))) })
		}
	}
}

// A critical box stands apart from the text above it and from the warnings around it; a run of Warn lines stays together.
func TestACriticalBoxStandsApartFromTheWarningsAroundIt(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	s.Page("T", Para("Text."))
	s.Warn("First.\n")
	s.Warn("Second.\n")
	s.Critical("Box.\n")
	s.Warn("After.\n")
	box := strings.Repeat("─", 55)
	wantLines(t, snapshot(s),
		"T", "", "Text.", "", "! First.", "! Second.", "",
		"╭"+box+"╮", boxRow("Box."), "╰"+box+"╯", "",
		"! After.")
}
