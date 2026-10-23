package commandtext_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
)

const reportURL = "http://127.0.0.1:8080/reports/acme/widgets/P7-R3"

func build(t *testing.T, changes ...report.FileChange) report.Report {
	t.Helper()
	r, err := report.Build(changes)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func summaryOf(t *testing.T, changes ...report.FileChange) commandtext.Changes {
	t.Helper()
	return commandtext.ChangesOf(build(t, changes...))
}

// smallChange is one modified and one added test file: 5 lines added and 1 removed, with a removed assertion and an added skip.
func smallChange() []report.FileChange {
	return []report.FileChange{
		{Path: "tests/a_test.go", Status: report.Modified,
			Old: []byte("package a\n\nfunc TestA(t *testing.T) {\n\tassert.Equal(t, 1, f())\n}\n"),
			New: []byte("package a\n\nfunc TestA(t *testing.T) {\n\tassert.Equal(t, 2, f())\n\tt.Skip(\"later\")\n}\n")},
		{Path: "tests/new_test.go", Status: report.Added, New: []byte("package a\n\nvar x = 1\n")},
	}
}

func revisionWith(t *testing.T, tier commandtext.Tier, changes ...report.FileChange) commandtext.Revision {
	t.Helper()
	r := revision(tier)
	c := summaryOf(t, changes...)
	r.Changes, r.ReportURL = &c, reportURL
	return r
}

const smallChangeComment = "**SuiteWard contract change P7-R3**\n\n" +
	"This pull request changes the protected files against the canonical contract V4. SuiteWard proposes the change as P7-R3 for the owner's approval.\n\n" +
	"- Base canonical version: V4\n- Installation tier: `isolated`\n\n" +
	"**What changed in the protected files.** SuiteWard counts the changed lines and points out some of them as marker lines; it does not judge them.\n\n" +
	"- Files: 1 added, 1 modified, 0 removed\n- Lines: +5/−1 (counted in 2 of 2 files)\n- Marker lines seen: 1 removed assertion line, 1 added skip line\n\n" +
	"Changed files, most changed lines first:\n\n```\n+2/−1  modified  tests/a_test.go\n+3/−0  added     tests/new_test.go\n```\n\n" +
	"[Full change report](" + reportURL + ")\n\n" +
	"This link opens on the machine that runs this SuiteWard instance; on the isolated tier, through a port forward of the HTTP port. Ctrl+click (Cmd+click on a Mac, or middle-click) the link to open it in a new tab.\n\n" +
	"This comment is a record, not an approval. SuiteWard compares the protected files only; it does not run tests. To approve this exact revision, comment:\n\n" +
	"```\n/suiteward approve P7-R3 <code>\n```\n\nReplace `<code>` with a current code from your authenticator app.\n\n" +
	"This supersedes `P7-R2`.\n"

func TestRevisionCommentStatesTheNumbersOfASmallChange(t *testing.T) {
	body := revisionText(t, revisionWith(t, commandtext.Isolated, smallChange()...))
	if body != smallChangeComment {
		t.Errorf("body:\n%s\nwant:\n%s", body, smallChangeComment)
	}
	checkBody(t, body)
}

func TestRevisionCommentWithNumbersReplacesTheListingsOfPaths(t *testing.T) {
	body := revisionText(t, revisionWith(t, commandtext.Isolated, smallChange()...))
	for _, w := range []string{"Protected files added", "Protected files modified", "Protected files removed"} {
		if strings.Contains(body, w) {
			t.Errorf("body still lists %q:\n%s", w, body)
		}
	}
}

func TestRevisionCommentNamesTheFilesItCouldNotCompareLineByLine(t *testing.T) {
	big := bytes.Repeat([]byte(strings.Repeat("x", 99)+"\n"), 11_000) // over 1 MiB: too large to compare
	changes := []report.FileChange{
		{Path: "tests/ok_test.go", Status: report.Modified, Old: []byte("a\n"), New: []byte("b\n")},
		{Path: "assets/logo.bin", Status: report.Modified, Old: []byte("\x00a"), New: []byte("\x00b")},
		{Path: "data/big.json", Status: report.Modified, Old: big, New: append([]byte("y\n"), big...)},
		{Path: "data/huge.txt", Status: report.Added, New: big},
	}
	body := revisionText(t, revisionWith(t, commandtext.Isolated, changes...))
	for _, w := range []string{
		"- Files: 1 added, 3 modified, 0 removed\n",
		"- Lines: +11001/−1 (counted in 2 of 4 files)\n",
		"- Not shown line by line: 3 files (listed below)\n",
		"Files without a line-by-line diff:\n\n```\n",
		"binary file           assets/logo.bin\n",
		"too large to compare  data/big.json\n",
		"too large to compare  data/huge.txt\n",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	checkBody(t, body)
}

func TestRevisionCommentCountsTheMarkersItSawAndNeverJudgesThem(t *testing.T) {
	changes := []report.FileChange{
		{Path: "web/a.test.js", Status: report.Modified,
			Old: []byte("describe('a', () => {\n  it('works', () => {\n    expect(f()).toBe(1)\n  })\n})\n"),
			New: []byte("describe('a', () => {\n  it.only('works', () => {\n  })\n})\n")},
		{Path: "web/b.test.js", Status: report.Removed, Old: []byte("expect(1).toBe(1)\n")},
		{Path: "web/c.test.js", Status: report.Added, New: []byte("it.skip('x', () => {})\n")},
	}
	r := revisionWith(t, commandtext.Isolated, changes...)
	body := revisionText(t, r)
	for _, w := range []string{
		"- Marker lines seen: 2 removed assertion lines, 1 added skip line, 1 added focus line, 1 removed test declaration, 1 removed test file\n",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	for _, w := range []string{"wrong", "unsafe", "weaken", "suspicious", "fail"} {
		if strings.Contains(strings.ToLower(body), w) {
			t.Errorf("body judges the change (%q):\n%s", w, body)
		}
	}
	clean := revisionWith(t, commandtext.Isolated, report.FileChange{Path: "tests/a.go", Status: report.Added, New: []byte("x\n")})
	if body := revisionText(t, clean); !strings.Contains(body, "- Marker lines seen: none\n") {
		t.Errorf("body does not say that no marker line was seen:\n%s", body)
	}
}

func TestRevisionCommentWithoutAReportURLHasNoLink(t *testing.T) {
	r := revisionWith(t, commandtext.Isolated, smallChange()...)
	r.ReportURL = ""
	body := revisionText(t, r)
	if strings.Contains(body, "http") || strings.Contains(body, "port forward") || strings.Contains(body, "new tab") || strings.Contains(body, "Full change report") {
		t.Errorf("body has a link without a URL:\n%s", body)
	}
	if !strings.Contains(body, "- Lines: +5/−1 (counted in 2 of 2 files)\n") {
		t.Errorf("body lacks the numbers:\n%s", body)
	}
}

func TestRevisionCommentThatCouldNotReadTheFilesListsThePathsAndSaysSo(t *testing.T) {
	r := revision(commandtext.Isolated)
	r.ChangesUnavailable = true
	body := revisionText(t, r)
	for _, w := range []string{"tests/new_test.go", "tests/a.go\ntests/b.go", "SuiteWard could not read the files of this revision to count the lines that changed, so the numbers are unknown.", "/suiteward approve P7-R3 <code>"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if strings.Contains(body, "Full change report") {
		t.Errorf("body links a report that cannot be built:\n%s", body)
	}
	checkBody(t, body)
}

func TestRevisionCommentKeepsTheReductionWarningAndTheCoLocatedNotice(t *testing.T) {
	r := revisionWith(t, commandtext.CoLocated, smallChange()...)
	r.Reduced, r.ReducedRules, r.ReducedPaths, r.ReducedPathsTotal = true, []string{"rule /tests/**"}, []string{"tests/b_test.go"}, 1
	body := revisionText(t, r)
	for _, w := range []string{"**This revision reduces the protected scope.**", "tests/b_test.go", "assurance is reduced", "- Lines: +5/−1", "[Full change report]", "/suiteward approve P7-R3 <code>"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if strings.Index(body, "/suiteward approve") < strings.Index(body, "[Full change report]") {
		t.Errorf("the link comes after the approve command:\n%s", body)
	}
}

func TestRevisionCommentSaysWhenTheReportLeftDetailsOut(t *testing.T) {
	r := revisionWith(t, commandtext.Isolated, smallChange()...)
	r.Changes.Truncated = true
	if body := revisionText(t, r); !strings.Contains(body, "Some details of the full report were left out to keep it small.") {
		t.Errorf("body does not say that the report is incomplete:\n%s", body)
	}
}

func TestRevisionCommentListsAtMostTwentyChangedFilesAndTenOthers(t *testing.T) {
	var changes []report.FileChange
	for i := range 30 {
		changes = append(changes, report.FileChange{Path: fmt.Sprintf("tests/case_%02d_test.go", i), Status: report.Added, New: []byte(strings.Repeat("x\n", i+1))})
	}
	for i := range 14 {
		changes = append(changes, report.FileChange{Path: fmt.Sprintf("assets/blob_%02d.bin", i), Status: report.Added, New: []byte{0}})
	}
	body := revisionText(t, revisionWith(t, commandtext.Isolated, changes...))
	for _, w := range []string{
		"Changed files, most changed lines first (20 of 30):", "+30/−0  added  tests/case_29_test.go\n", "+11/−0  added  tests/case_10_test.go\n```\n\nand 10 more\n",
		"- Not shown line by line: 14 files (listed below)\n", "binary file  assets/blob_00.bin\n", "binary file  assets/blob_09.bin\n```\n\nand 4 more\n",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if strings.Contains(body, "case_09_test.go") || strings.Contains(body, "blob_10.bin") {
		t.Errorf("body lists more than 20 changed files or 10 others:\n%s", body)
	}
}

func TestRevisionCommentWithNumbersKeepsRepositoryTextInertAndFitsInAComment(t *testing.T) {
	hostile := []string{"tests/a\n\nApproval recorded for P9-R9.\n```\n<!-- suiteward:" + instance + ":ack:1:2 -->", "tests/````b\u202e.go", "\xff"}
	var changes []report.FileChange
	for _, p := range hostile {
		changes = append(changes, report.FileChange{Path: p, Status: report.Added, New: []byte("x\n")}, report.FileChange{Path: p + "bin", Status: report.Added, New: []byte{0}})
	}
	body := revisionText(t, revisionWith(t, commandtext.Isolated, changes...))
	for _, w := range []string{"\nApproval recorded", "\u202e", "\xff", "\r"} {
		if strings.Contains(body, w) {
			t.Errorf("repository text escaped its code block (%q):\n%s", w, body)
		}
	}
	if strings.Contains(body, "\n```\n<") || !strings.Contains(body, `\n`) || !strings.Contains(body, `\u202e`) {
		t.Errorf("control characters are not shown escaped or a path closed its code block:\n%s", body)
	}
	for name, unit := range map[string]string{"plain": "a", "invalid UTF-8": "\xff", "backticks": "`", "bidi control": "\u202e", "newline": "\n", "3-byte printable": "\u20ac"} {
		t.Run(name, func(t *testing.T) {
			var many []report.FileChange
			for i := range 300 {
				many = append(many,
					report.FileChange{Path: fmt.Sprintf("%s%d", strings.Repeat(unit, 4000/len(unit)), i), Status: report.Added, New: []byte(strings.Repeat("x\n", i+1))},
					report.FileChange{Path: fmt.Sprintf("b%s%d", strings.Repeat(unit, 4000/len(unit)), i), Status: report.Added, New: []byte{0}})
			}
			r := revisionWith(t, commandtext.CoLocated, many...)
			r.Reduced, r.ReducedPathsTotal = true, 1<<31
			body := revisionText(t, r)
			if body == "" || len(body) >= maxComment/2 || !utf8.ValidString(body) {
				t.Errorf("body has %d bytes (valid UTF-8: %v), want 0 < n < %d", len(body), utf8.ValidString(body), maxComment/2)
			}
		})
	}
}

// validChanges is the summary of smallChange, written out so that the cases below do not depend on ChangesOf.
func validChanges() commandtext.Changes {
	return commandtext.Changes{Added: 1, Modified: 1, Counted: 2, LinesAdded: 5, LinesRemoved: 1,
		Top: []commandtext.ChangedFile{{Path: "tests/a_test.go", Status: "modified", LinesAdded: 2, LinesRemoved: 1}, {Path: "tests/new_test.go", Status: "added", LinesAdded: 3}}}
}

func TestRevisionCommentPanicsOnChangesThatCannotBeStated(t *testing.T) {
	for name, mutate := range map[string]func(*commandtext.Revision){
		"a URL that is not a loopback report": func(r *commandtext.Revision) { r.ReportURL = "https://evil.example/reports/acme/widgets/P7-R3" },
		"a URL of another host": func(r *commandtext.Revision) {
			r.ReportURL = "http://127.0.0.1.evil.example:8080/reports/acme/widgets/P7-R3"
		},
		"a URL that closes the link": func(r *commandtext.Revision) {
			r.ReportURL = "http://127.0.0.1:8080/reports/acme/widgets/P7-R3) [x](http://evil"
		},
		"a URL of another revision":             func(r *commandtext.Revision) { r.ReportURL = "http://127.0.0.1:8080/reports/acme/widgets/P7-R2" },
		"a URL without the revision":            func(r *commandtext.Revision) { r.ReportURL = "http://127.0.0.1:8080/reports/acme/widgets/" },
		"a link without numbers":                func(r *commandtext.Revision) { r.Changes = nil },
		"numbers that could not be read":        func(r *commandtext.Revision) { r.ChangesUnavailable = true },
		"more than 20 changed files":            func(r *commandtext.Revision) { r.Changes.Top = make([]commandtext.ChangedFile, 21) },
		"more than 10 files not shown":          func(r *commandtext.Revision) { r.Changes.NotShown = make([]commandtext.NotShown, 11) },
		"a negative count":                      func(r *commandtext.Revision) { r.Changes.LinesRemoved = -1 },
		"more counted files than changed files": func(r *commandtext.Revision) { r.Changes.Counted = 3 },
		"a listed file that was not counted":    func(r *commandtext.Revision) { r.Changes.Counted = 1 },
		"a changed file of an unknown status":   func(r *commandtext.Revision) { r.Changes.Top[0].Status = "renamed" },
		"a file not shown without a reason": func(r *commandtext.Revision) {
			r.Changes.NotShown = []commandtext.NotShown{{Path: "a"}}
			r.Changes.NotShownTotal = 1
		},
		"a total of files not shown below the listing": func(r *commandtext.Revision) {
			r.Changes.NotShown = []commandtext.NotShown{{Path: "a", Why: "binary file"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, c := revision(commandtext.Isolated), validChanges()
			r.Changes, r.ReportURL = &c, reportURL
			mutate(&r)
			mustPanic(t, func() { commandtext.RevisionComment(r) })
		})
	}
}

func TestChangesOfCountsTheFilesTheLinesAndTheMarkers(t *testing.T) {
	got := summaryOf(t, smallChange()...)
	want := commandtext.Changes{Added: 1, Modified: 1, Counted: 2, LinesAdded: 5, LinesRemoved: 1,
		Top:     []commandtext.ChangedFile{{Path: "tests/a_test.go", Status: "modified", LinesAdded: 2, LinesRemoved: 1}, {Path: "tests/new_test.go", Status: "added", LinesAdded: 3}},
		Markers: commandtext.Markers{RemovedAssertions: 1, AddedSkips: 1}}
	if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
		t.Errorf("ChangesOf = %+v\nwant %+v", got, want)
	}
}

func TestChangesOfNamesEveryFileThatHasNoLineByLineDiff(t *testing.T) {
	r := report.Report{
		Files: []report.File{
			{Path: "a.bin", Status: report.Modified, Limit: report.LimitBinary},
			{Path: "b.txt", Status: report.Modified, Limit: report.LimitEditDistance},
			{Path: "c.txt", Status: report.Modified, Limit: report.LimitBudget},
			{Path: "d.txt", Status: report.Modified, CountsKnown: true, LinesAdded: 4, LinesRemoved: 4, Truncated: true},
			{Path: "e.txt", Status: report.Added, CountsKnown: true, LinesAdded: 9, Limit: report.LimitSize},
			{Path: "f.txt", Status: report.Modified, CountsKnown: true, LinesAdded: 1, LinesRemoved: 1},
		},
		Totals:    report.Totals{Files: map[report.Status]int{report.Modified: 5, report.Added: 1}, LinesAdded: 14, LinesRemoved: 5},
		Truncated: true,
	}
	got := commandtext.ChangesOf(r)
	want := []commandtext.NotShown{
		{Path: "a.bin", Why: "binary file"}, {Path: "b.txt", Why: "too different to compare"}, {Path: "c.txt", Why: "not compared (work budget ran out)"},
		{Path: "d.txt", Why: "diff left out (report size budget)"}, {Path: "e.txt", Why: "too large to compare"},
	}
	if fmt.Sprint(got.NotShown) != fmt.Sprint(want) || got.NotShownTotal != 5 || got.Counted != 3 || !got.Truncated || got.LinesAdded != 14 || got.LinesRemoved != 5 {
		t.Errorf("ChangesOf = %+v\nwant not shown %v (5 files), 3 counted, +14/−5 and truncated", got, want)
	}
}

func TestChangesOfOrdersTheTopByChangedLinesThenPath(t *testing.T) {
	var changes []report.FileChange
	for i, n := range []int{3, 9, 3, 1, 9} {
		changes = append(changes, report.FileChange{Path: fmt.Sprintf("f%d.txt", i), Status: report.Added, New: []byte(strings.Repeat("x\n", n))})
	}
	var paths []string
	for _, f := range summaryOf(t, changes...).Top {
		paths = append(paths, f.Path)
	}
	if got := strings.Join(paths, " "); got != "f1.txt f4.txt f0.txt f2.txt f3.txt" {
		t.Errorf("top = %s, want f1.txt f4.txt f0.txt f2.txt f3.txt", got)
	}
}

func TestCheckSummaryStatesTheNumbersWithoutALink(t *testing.T) {
	c := check(commandtext.CheckInProgress, "approval-missing")
	changes := summaryOf(t, smallChange()...)
	c.Changes = &changes
	_, summary := checkText(t, c)
	for _, w := range []string{"- Files: 1 added, 1 modified, 0 removed\n", "- Lines: +5/−1 (counted in 2 of 2 files)\n", "- Marker lines seen: 1 removed assertion line, 1 added skip line\n"} {
		if !strings.Contains(summary, w) {
			t.Errorf("summary lacks %q:\n%s", w, summary)
		}
	}
	if strings.Contains(summary, "http") || strings.Contains(summary, "tests/a_test.go") {
		t.Errorf("a public check summary links the report or names a path:\n%s", summary)
	}
	checkBody(t, summary)
	without := check(commandtext.CheckInProgress, "approval-missing")
	if _, plain := checkText(t, without); strings.Contains(plain, "Lines:") {
		t.Errorf("a summary without changes states lines:\n%s", plain)
	}
}

func TestCheckSummaryPanicsOnChangesWithoutARevision(t *testing.T) {
	c := check(commandtext.CheckSuccess, "unchanged")
	c.Proposal, c.Revision = 0, 0
	changes := summaryOf(t, smallChange()...)
	c.Changes = &changes
	mustPanic(t, func() { commandtext.CheckSummary(c) })
}

func TestVisibleShowsWhatHidesItself(t *testing.T) {
	for in, want := range map[string]string{
		"plain text\twith a tab": "plain text\twith a tab",
		"a\u202eb":               `a\u202eb`,
		"a\u200bb":               `a\u200bb`,
		"a\rb":                   `a\rb`,
		"a\x00b":                 `a\x00b`,
		"a\xffb":                 `a\xffb`,
		"naïve € 日本語":            "naïve € 日本語",
		"a\U000e0100b":           `a\U000e0100b`,
	} {
		if got := commandtext.Visible(in); got != want {
			t.Errorf("Visible(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckSummaryPanicsOnChangesThatCannotBeStated(t *testing.T) {
	c := check(commandtext.CheckSuccess, "ready")
	changes := validChanges()
	changes.Counted = changes.Added + changes.Modified + changes.Removed + 1
	c.Changes = &changes
	mustPanic(t, func() { commandtext.CheckSummary(c) })
}
