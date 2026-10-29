package app

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// The layout of the pull request page for a pull request with thousands of tests (docs/contracts/runtime.md, "The pull request page"): the short sections above the long
// tables, and each table of tests folded into one closed group for each state, the worst state first.

// pageGroup is one group of a table of tests as the page writes it: its summary, and the state that the first cell of each of its rows says.
type pageGroup struct {
	summary string
	states  []string
}

var (
	headingPattern = regexp.MustCompile(`<h2>([^<]*)</h2>`)
	groupPattern   = regexp.MustCompile(`(?s)<details([^>]*)>\n<summary>([^<]*)</summary>(.*?)</details>`)
	rowStatePart   = regexp.MustCompile(`<tr><td>([^<]*)</td>`)
)

// readGroups are the groups of a section of the page in order. A group that is not a plain <details>, which is closed, is an error of the page.
func (w *pageWorld) readGroups(section string) []pageGroup {
	w.t.Helper()
	var groups []pageGroup
	for _, m := range groupPattern.FindAllStringSubmatch(section, -1) {
		if strings.TrimSpace(m[1]) != "" && strings.TrimSpace(m[1]) != `class="tests"` {
			w.t.Errorf("the group %q is <details%s>: a group is closed and has no other attribute than its class", m[2], m[1])
		}
		g := pageGroup{summary: m[2]}
		for _, row := range rowStatePart.FindAllStringSubmatch(m[3], -1) {
			g.states = append(g.states, row[1])
		}
		groups = append(groups, g)
	}
	return groups
}

func summaries(groups []pageGroup) []string {
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = g.summary
	}
	return out
}

// The short sections come first: the kinds of change, how the results were made and the change report are above the long tables, and the closing note ends the page.
func TestPullPageShowsTheShortSectionsAboveTheLongTables(t *testing.T) {
	w := weakenedWorld(t)
	page := w.text()
	var got []string
	for _, m := range headingPattern.FindAllStringSubmatch(page, -1) {
		got = append(got, m[1])
	}
	want := []string{"Tests by state", "The kinds of change", "How these results were made", "Change report", "Approved tests", "Tests this change adds or edits"}
	if !slices.Equal(got, want) {
		t.Errorf("the sections are %q, want %q", got, want)
	}
	w.wantInOrder(page, "Pull request: 5", weakenedTwo, "<h2>Tests by state</h2>") // the top: the facts and the weakened sentence
	w.wantInOrder(page, "<h2>Tests this change adds or edits</h2>", "The results on this page are evidence reported by a test runner, not a verdict.")
}

// A table of tests is one group for each state that has a test, each closed, with no group for a state that has none. A group holds the rows of its state, and its summary names
// the state and counts them.
func TestPullPageFoldsEachTableOfTestsIntoOneClosedGroupForEachStateThatHasTests(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass", "pass", "fail", "skipped", "excluded"), receiptTests("detects", "detects", "detects", "inert", "fails"))
	page := w.text()
	for heading, want := range map[string][]pageGroup{
		"Approved tests": {
			{"FAIL (1)", []string{"FAIL"}}, {"SKIPPED (1)", []string{"SKIPPED"}}, {"EXCLUDED (1)", []string{"EXCLUDED"}}, {"PASS (2)", []string{"PASS", "PASS"}}},
		"Tests this change adds or edits": {
			{"FAILS (1)", []string{"FAILS"}}, {"INERT (1)", []string{"INERT"}}, {"DETECTS (3)", []string{"DETECTS", "DETECTS", "DETECTS"}}},
	} {
		section := w.section(page, heading)
		got := w.readGroups(section)
		if len(got) != len(want) {
			t.Errorf("%s: groups %v, want %v", heading, got, want)
			continue
		}
		for i := range want {
			if got[i].summary != want[i].summary || !slices.Equal(got[i].states, want[i].states) {
				t.Errorf("%s: group %d is %+v, want %+v", heading, i, got[i], want[i])
			}
		}
		if open, all := strings.Count(section, "<details open"), strings.Count(section, "<details"); open != 0 || all != len(want) {
			t.Errorf("%s: %d groups of which %d are open, want %d and none: every group starts closed", heading, all, open, len(want))
		}
	}
	w.wantNoText(page, "<script", "MISSING (", "FLAKY (", "NEW API (")
}

// A run that has no test has no group, and says so as before.
func TestPullPageHasNoGroupForARunWithNoTest(t *testing.T) {
	w := newPageWorld(t)
	w.completed(nil, receiptTests("excluded"))
	page := w.text()
	w.wantText(w.section(page, "Approved tests"), "0 tests ran.")
	w.wantNoText(w.section(page, "Approved tests"), "<details")
}

// The groups go from the worst state to the best, and each is named as the table of the tests by state names its state.
func TestPullPageOrdersTheGroupsFromTheWorstStateToTheBestAndNamesThemAsTheTableByState(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass", "excluded", "skipped", "flaky", "missing", "fail"),
		receiptTests("detects", "new_api", "inert", "excluded", "skipped", "flaky", "missing", "fails"))
	page := w.text()
	approved := summaries(w.readGroups(w.section(page, "Approved tests")))
	if want := []string{"FAIL (1)", "MISSING (1)", "FLAKY (1)", "SKIPPED (1)", "EXCLUDED (1)", "PASS (1)"}; !slices.Equal(approved, want) {
		t.Errorf("the groups of the approved tests are %q, want %q", approved, want)
	}
	changed := summaries(w.readGroups(w.section(page, "Tests this change adds or edits")))
	if want := []string{"FAILS (1)", "MISSING (1)", "FLAKY (1)", "SKIPPED (1)", "EXCLUDED (1)", "INERT (1)", "NEW API (1)", "DETECTS (1)"}; !slices.Equal(changed, want) {
		t.Errorf("the groups of the tests of the change are %q, want %q", changed, want)
	}
	for _, c := range []struct {
		groups []string
		plan   func(commandtext.StateMeaning) bool
	}{{approved, func(m commandtext.StateMeaning) bool { return m.Canonical }}, {changed, func(m commandtext.StateMeaning) bool { return m.Receipt }}} {
		for _, summary := range c.groups {
			state, count, found := strings.Cut(summary, " (")
			if _, err := strconv.Atoi(strings.TrimSuffix(count, ")")); !found || err != nil {
				t.Errorf("the summary %q is not a state and a count", summary)
			}
			if !slices.ContainsFunc(commandtext.StateMeanings(), func(m commandtext.StateMeaning) bool { return m.State == state && c.plan(m) }) {
				t.Errorf("the summary %q names %q, which is no state of the table of the tests by state for its run", summary, state)
			}
		}
	}
}

// The style never breaks a word of the state column or of a header: those cells do not wrap at all, and the long names and paths of the other cells may. A table that is wider
// than the page scrolls inside its own box.
func TestPullPageStyleNeverBreaksTheStateColumnOrAHeaderInsideAWord(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects"))
	page := w.text()
	style := page[strings.Index(page, "<style>"):strings.Index(page, "</style>")]
	rules := regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`).FindAllStringSubmatch(style, -1)
	var noWrap = map[string]bool{}
	for _, r := range rules {
		for _, selector := range strings.Split(r[1], ",") {
			selector = strings.TrimSpace(selector)
			if selector != "th" && !strings.HasSuffix(selector, "td:first-child") {
				continue
			}
			if strings.Contains(r[2], "overflow-wrap: anywhere") || strings.Contains(r[2], "word-break: break-all") {
				t.Errorf("the rule %q lets %q break inside a word", strings.TrimSpace(r[1]), selector)
			}
			if strings.Contains(r[2], "white-space: nowrap") {
				noWrap[selector] = true
			}
		}
	}
	if !noWrap["th"] {
		t.Errorf("no rule keeps a header on one line:\n%s", style)
	}
	if !slices.ContainsFunc(rules, func(r []string) bool {
		return strings.Contains(r[2], "white-space: nowrap") && slices.ContainsFunc(strings.Split(r[1], ","), func(s string) bool { return strings.HasSuffix(strings.TrimSpace(s), "td:first-child") })
	}) {
		t.Errorf("no rule keeps the first column, the state of a test, on one line:\n%s", style)
	}
	if !slices.ContainsFunc(rules, func(r []string) bool {
		return strings.TrimSpace(r[1]) == ".scroll" && strings.Contains(r[2], "overflow-x: auto")
	}) {
		t.Errorf("no rule lets a table scroll inside its own box:\n%s", style)
	}
	if tables, boxed := strings.Count(page, "<table>"), strings.Count(page, `<div class="scroll"><table>`); tables == 0 || tables != boxed {
		t.Errorf("%d tables and %d in a box of their own that scrolls, want all of them", tables, boxed)
	}
}
