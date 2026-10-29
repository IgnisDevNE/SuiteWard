package app

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The change report of the current revision on the page, and the size rule (docs/contracts/runtime.md, "The pull request page"): the page has no hunk, every part of it has
// a budget in escaped bytes, and the cap of 12 MiB is a guard that the budgets keep unreachable, with 503 and never 413 as its answer.

func TestPullPageShowsTheChangeReportOfTheCurrentRevisionWithoutAnyHunk(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects"))
	page := w.text()
	section := w.section(page, "Change report")
	w.wantText(section, "P2-R1", "Files: 0 added, 1 modified, 0 removed", "Lines: 1 added, 1 removed (counted in 1 of 1 files)", "Marker lines seen: none",
		pageHeader("Status", "File", "Lines"), pageRow("modified", "tests/a_test.go", "+1/−1"), `<a href="/reports/acme/widgets/P2-R1">`)
	w.wantNoText(page, "@@", "<pre", "-v1", "+v2", "line cut")
}

// The hunks of a large change stay on the report page: the pull request page is not made larger by them.
func TestPullPageHoldsNoHunkWhateverTheChangeIs(t *testing.T) {
	w := newPageWorld(t)
	files := execHeadFiles(receiptDecl, "v2")
	files["tests/b_test.go"] = strings.Repeat("a line of a test that changes\n", 30000) // 870 KB of added lines
	w.execPullFive(files)
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass"))
	w.finish(w.onlyJob(5, cjHead2, execution.PlanReceipt), receiptTests("detects"))
	response := w.get("127.0.0.1:8080", pullPath)
	if response.Code != http.StatusOK || response.Body.Len() > 64<<10 {
		t.Fatalf("status %d and %d bytes: the hunks of the report are on the page", response.Code, response.Body.Len())
	}
	w.wantNoText(response.Body.String(), "a line of a test that changes", "@@")
	w.wantText(w.section(html.UnescapeString(response.Body.String()), "Change report"), pageRow("added", "tests/b_test.go", "+30000/−0"))
}

func TestPullPageLinksTheFullReportAndTheReportOfEachEarlierRevisionNewestFirst(t *testing.T) {
	w := newPageWorld(t)
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	for _, push := range []struct{ head, content string }{{cjHead3, "v3"}, {cjHead4, "v4"}} { // two protected pushes: three revisions
		w.tree(push.head, execHeadFiles(receiptDecl, push.content))
		w.sync(map[int]string{5: push.head})
		w.execRun()
	}
	page := w.text()
	section := w.section(page, "Change report")
	w.wantText(section, "P2-R3", `<a href="/reports/acme/widgets/P2-R3">`)
	w.wantInOrder(section, "<h3>Earlier revisions of this proposal: 2</h3>", `<a href="/reports/acme/widgets/P2-R2">P2-R2</a>`, `<a href="/reports/acme/widgets/P2-R1">P2-R1</a>`)
	if got := strings.Count(section, "/reports/acme/widgets/P2-R3"); got != 1 {
		t.Errorf("the current revision is linked %d times, want once, as the full report", got)
	}
}

func TestPullPageOfAFirstRevisionLinksNoEarlierOne(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects"))
	w.wantNoText(w.section(w.text(), "Change report"), "Earlier revisions")
}

func TestPullPageOfAPullRequestWithNoRevisionHasNoChangeReport(t *testing.T) {
	w := newPageWorld(t)
	w.tree(cjHead2, execHeadFiles(receiptDecl, "v1")) // the head changes nothing that is protected
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.execRun()
	w.wantNoText(w.text(), "<h2>Change report</h2>")
}

// A revision whose report cannot be built shows one sentence in its place and never fails the page; the cause is logged, never shown.
func TestPullPageSaysInOneSentenceThatTheChangeReportCannotBeBuiltAndStillServesTheRest(t *testing.T) {
	for name, tc := range map[string]struct {
		err      error
		sentence string
	}{
		"no stored manifest":      {fmt.Errorf("%w: no manifest", governance.ErrNotFound), "There is no change report for this revision: SuiteWard has no stored files of it, or it has no earlier contract to compare with."},
		"a revision over a bound": {errReportTooLarge, "This revision changes more than SuiteWard builds a change report from, so the report is not shown."},
		"a file missing":          {fmt.Errorf("read the file: %w", fs.ErrNotExist), "SuiteWard could not read the files of this revision, so the change report is not shown."},
		"a store that fails":      {errors.New("SECRET-STORE-TEXT: connection reset"), "SuiteWard could not read the files of this revision, so the change report is not shown."},
	} {
		t.Run(name, func(t *testing.T) {
			w := newPageWorld(t)
			w.completed(canonicalTests("pass"), receiptTests("detects"))
			w.server.reports = &fakeReports{err: tc.err}
			page := w.text()
			section := w.section(page, "Change report")
			w.wantText(section, "<p>"+tc.sentence+"</p>")
			w.wantNoText(section, "<a href", "<table>", "<ul>", "Earlier revisions") // the section is the one sentence: a link would answer 404 or 413
			if got := strings.Count(section, "<p>"); got != 1 {
				t.Errorf("the section has %d paragraphs, want the one sentence: %s", got, section)
			}
			w.wantNoText(page, "SECRET", "<tr><td>modified</td>")
			w.wantText(page, "SuiteWard ran the tests for this commit.", pageRow("PASS", "TestC00", ".", "a_test.go", "", ""))
		})
	}
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects"))
	w.server.reports = &fakeReports{err: errors.New("SECRET-STORE-TEXT: connection reset")}
	w.text()
	if !strings.Contains(w.apiLogs.String(), "SECRET-STORE-TEXT") {
		t.Errorf("the cause is not logged: %s", w.apiLogs)
	}
}

// reportsEndContext cancels the request while it builds the report, as a client that left does.
type reportsEndContext struct{ cancel context.CancelFunc }

func (r reportsEndContext) revisionReport(ctx context.Context, _ contract.ProposalReference) (revisionReport, error) {
	r.cancel()
	return revisionReport{}, ctx.Err()
}

func TestPullPageFailsWhenTheRequestEndsWhileItBuildsTheReport(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects"))
	ctx, cancel := context.WithCancel(t.Context())
	w.server.reports = reportsEndContext{cancel: cancel}
	recorder := httptest.NewRecorder()
	newRouter(w.server).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+pullPath, nil).WithContext(ctx))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503: a page that the end of its request cut is not a page without a report", recorder.Code)
	}
}

// ---- the size rule ----

// hugeTests are the 10,000 tests that a result may hold, each with a package, a name and a file at the limits of the store.
func hugeTests(kind, state string, pkg, name, file func(i int) string) []execution.Test {
	tests := make([]execution.Test, execution.MaxTests)
	for i := range tests {
		tests[i] = execution.Test{Package: pkg(i), Name: name(i), File: file(i), Kind: kind, State: state, HeadRuns: []string{"pass"}}
		if kind == execution.KindAdded {
			tests[i].BaseRuns = []string{"pass"}
		}
	}
	return tests
}

func plainLimits() (pkg, name, file func(i int) string) {
	return func(i int) string { return strings.Repeat("p", 500) + fmt.Sprintf("%05d", i) }, func(i int) string { return fmt.Sprintf("Test%0190d", i) },
		func(int) string { return strings.Repeat("f", 1000) + "_test.go" }
}

// tableOfSection is the first table of the raw section, as the page wrote it, and the number of rows of tests in it.
func tableOfSection(t *testing.T, raw, heading string) (table string, rows int) {
	t.Helper()
	open := "<h2>" + heading + "</h2>"
	start := strings.Index(raw, open)
	if start < 0 {
		t.Fatalf("no section %q", heading)
	}
	rest := raw[start+len(open):]
	if end := strings.Index(rest, "<h2>"); end >= 0 {
		rest = rest[:end]
	}
	from := strings.Index(rest, "<table>")
	to := strings.Index(rest, "</table>")
	if from < 0 || to < 0 {
		t.Fatalf("no table in the section %q", heading)
	}
	table = rest[from : to+len("</table>")]
	return table, strings.Count(table, "<tr><td>")
}

func TestPullPageCutsEachTableOfTestsAtItsBudgetAndCountsTheRowsItDoesNotShow(t *testing.T) {
	w := newPageWorld(t)
	pkg, name, file := plainLimits()
	w.completed(hugeTests(execution.KindDeclared, "pass", pkg, name, file), hugeTests(execution.KindAdded, "inert", pkg, name, file))
	response := w.get("127.0.0.1:8080", pullPath)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d for two runs of %d tests, want 200: no page is refused for its size\n%s", response.Code, execution.MaxTests, w.apiLogs)
	}
	raw := response.Body.String()
	if response.Body.Len() > 9<<20 {
		t.Errorf("the page has %d bytes, more than the 9 MiB of its budgets", response.Body.Len())
	}
	for _, heading := range []string{"Approved tests", "Tests this change adds or edits"} {
		table, shown := tableOfSection(t, raw, heading)
		if len(table) > 3<<20 {
			t.Errorf("%s: the table has %d bytes, over its budget of 3 MiB", heading, len(table))
		}
		if shown == 0 || shown == execution.MaxTests {
			t.Fatalf("%s: %d rows of tests are shown, want some and not all", heading, shown)
		}
		if want := fmt.Sprintf("<p>%d more tests are not shown here. The counts above include them.</p>", execution.MaxTests-shown); !strings.Contains(raw, want) {
			t.Errorf("%s: the page lacks %q", heading, want)
		}
		if !strings.Contains(table, fmt.Sprintf("Test%0190d", shown-1)) || strings.Contains(table, fmt.Sprintf("Test%0190d", shown)) {
			t.Errorf("%s: the rows shown are not the first %d in order of package and name", heading, shown)
		}
	}
	w.wantText(html.UnescapeString(raw), "<td>The approved test passed.</td><td>10000</td><td>-</td></tr>")
	w.wantText(html.UnescapeString(raw), "does anything.</td><td>-</td><td>10000</td></tr>") // the counts of the table by state are complete
}

// The weakened rows and then the flagged ones come first, so that a budget never drops one of them, however late they sort.
func TestPullPageNeverCutsAWeakenedRowOrAFlaggedRowBeforeAPassingOne(t *testing.T) {
	w := newPageWorld(t)
	pkg, name, file := plainLimits()
	approved := hugeTests(execution.KindDeclared, "pass", pkg, name, file)
	changes := hugeTests(execution.KindAdded, "inert", pkg, name, file)
	for i := 0; i < 3; i++ { // the last three sort last, and are an approved test that fails and this change edits, and a receipt test that fails
		last := execution.MaxTests - 1 - i
		approved[last].State, approved[last].HeadRuns = "fail", []string{"fail"}
		changes[last].Kind, changes[last].State, changes[last].HeadRuns, changes[last].BaseRuns = execution.KindChanged, "detects", []string{"pass"}, []string{"fail"}
	}
	changes[0].State, changes[0].HeadRuns = "fails", []string{"fail"} // sorts first, and is flagged
	changes[execution.MaxTests-4].State, changes[execution.MaxTests-4].HeadRuns = "fails", []string{"fail"}
	w.completed(approved, changes)
	raw := html.UnescapeString(w.get("127.0.0.1:8080", pullPath).Body.String())
	canonical, shown := tableOfSection(t, raw, "Approved tests")
	if shown >= execution.MaxTests {
		t.Fatalf("the table shows all %d rows: the budget did not cut it", shown)
	}
	for i := 0; i < 3; i++ {
		last := fmt.Sprintf("Test%0190d", execution.MaxTests-1-i)
		if !strings.Contains(canonical, last+"</td>") {
			t.Errorf("the weakened test %s sorts last and is not on the page", last)
		}
	}
	if got := strings.Count(canonical, "Weakened: this change edits this test."); got != 3 {
		t.Errorf("%d weakened rows in the approved table, want 3", got)
	}
	receipt, _ := tableOfSection(t, raw, "Tests this change adds or edits")
	w.wantInOrder(receipt, "<td>DETECTS</td>", "<td>FAILS</td>") // the weakened rows come first, then the flagged ones
	if got := strings.Count(receipt, "<td>FAILS</td>"); got != 2 {
		t.Errorf("%d FAILS rows, want both of them whatever the budget cut", got)
	}
}

// hostile is a text of n bytes made of the characters that grow most when they are shown: & and + (html/template writes them as five bytes), and an invalid byte.
func hostile(n int, c string) string { return strings.Repeat(c, n) }

func TestPullPageStaysUnderItsBudgetsWhateverTheRepositoryAndTheTestRunnerSay(t *testing.T) {
	w := newPageWorld(t)
	files := execHeadFiles(receiptDecl, "v2")
	for i := range 150 { // the files that the receipt covers: the page lists 100 of them, each at the longest path that a job keeps
		files["tests/"+hostile(900, "&")+fmt.Sprintf("%03d_test.go", i)] = "package a\n"
	}
	for i := range 400 { // the files of the change report
		files["tests/"+hostile(900, "+")+fmt.Sprintf("%03d_test.go", i)] = "package a\n"
	}
	w.execPullFive(files)
	pkg := func(i int) string { return hostile(400, "&") + hostile(50, "\xff") + fmt.Sprintf("%05d", i) }
	name := func(i int) string { return hostile(100, "+") + hostile(50, "<") + fmt.Sprintf("%05d", i) }
	file := func(int) string {
		return hostile(300, "&") + hostile(150, "\xe2\x80\xae") + hostile(150, "\\") + "_test.go"
	}
	approved := hugeTests(execution.KindDeclared, "pass", pkg, name, file)
	changes := hugeTests(execution.KindAdded, "inert", pkg, name, file)
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), approved)
	w.finish(w.onlyJob(5, cjHead2, execution.PlanReceipt), changes, func(r *execution.Result) {
		for i := range 200 {
			r.Markers = append(r.Markers, execution.Marker{Kind: "env-read", Path: hostile(4000, "&") + fmt.Sprint(i), Line: i + 1})
		}
		for i := range 100 {
			r.Submodules = append(r.Submodules, hostile(4000, "<")+fmt.Sprint(i))
			r.Unparsed = append(r.Unparsed, hostile(900, "&")+fmt.Sprintf("%03d_test.go", i))
		}
	})
	response := w.get("127.0.0.1:8080", pullPath)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: the page of a hostile pull request is refused\n%s", response.Code, w.apiLogs)
	}
	raw := response.Body.String()
	if response.Body.Len() > 9<<20 {
		t.Errorf("the page has %d bytes, more than the 9 MiB that its budgets add up to", response.Body.Len())
	}
	for _, heading := range []string{"Approved tests", "Tests this change adds or edits"} {
		if table, _ := tableOfSection(t, raw, heading); len(table) > 3<<20 {
			t.Errorf("%s: the table has %d bytes, over its budget of 3 MiB", heading, len(table))
		}
	}
	if table, _ := tableOfSection(t, raw, "Change report"); len(table) > 1<<20 {
		t.Errorf("the table of the files of the report has %d bytes, over its budget of 1 MiB", len(table))
	}
	lists := regexp.MustCompile(`(?s)<h3>([^<]*)</h3>\n<ul>\n(.*?)</ul>`).FindAllStringSubmatch(raw, -1)
	if len(lists) < 4 {
		t.Fatalf("%d lists on the page, want the files, the submodules, the lines to look at and the files that cannot be read", len(lists))
	}
	for _, list := range lists {
		if len(list[2]) > 256<<10 {
			t.Errorf("the list %q has %d bytes, over its budget of 256 KiB", list[1], len(list[2]))
		}
	}
	for _, sentence := range []string{"more are not shown here. The count above includes them.", "more files are not shown here. The counts above include them.", "more tests are not shown here. The counts above include them."} {
		if !strings.Contains(raw, sentence) {
			t.Errorf("the page lacks the sentence %q of what a budget left out", sentence)
		}
	}
	for _, banned := range []string{"\xff", "\xe2\x80\xae", "<script"} {
		if strings.Contains(raw, banned) {
			t.Errorf("the page holds %q unescaped", banned)
		}
	}
	text := html.UnescapeString(raw)
	w.wantText(text, "Files: 550 added, 1 modified, 0 removed") // the counts of the report are complete: 150 + 400 added files, with tests/a_test.go modified
}

// The cap is a guard that the budgets keep unreachable. When it is passed all the same, the answer is 503 and the fixed text, never 413, and the cause is logged.
func TestPullPagePastItsCapIs503AndNeverRefusedForItsSize(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass", "fail"), receiptTests("detects"))
	w.pages.limit = 1 << 10
	response := w.get("127.0.0.1:8080", pullPath)
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "the pull request page is unavailable\n" {
		t.Fatalf("status %d, body %q, want 503 and the fixed text", response.Code, response.Body.String())
	}
	requireHeaders(t, "503", response)
	if !strings.Contains(w.apiLogs.String(), errPageTooLarge.Error()) {
		t.Errorf("the cause is not logged: %s", w.apiLogs)
	}
}

func TestAPageIsBuiltUnderACapOfTwelveMebibytesAboveTheNineOfItsBudgets(t *testing.T) {
	if got := newReceiptPages(nil, nil, nil, nil, nil, "isolated").limit; got != 12<<20 {
		t.Errorf("a page is built under a limit of %d bytes, want 12 MiB", got)
	}
}

func (r reportsEndContext) turn(context.Context) (func(), error) { return func() {}, nil }

func (r reportsEndContext) build(ctx context.Context, ref contract.ProposalReference) (revisionReport, error) {
	return r.revisionReport(ctx, ref)
}

// A revision whose report cannot be built has no link: the full report would answer 404 or 413, and the earlier revisions are links of the same section.
func TestPullPageLinksNoReportWhenTheReportOfTheRevisionCannotBeBuilt(t *testing.T) {
	w := newPageWorld(t)
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	w.tree(cjHead3, execHeadFiles(receiptDecl, "v3"))
	w.sync(map[int]string{5: cjHead3})
	w.execRun()
	w.server.reports = &fakeReports{err: errReportTooLarge}
	section := w.section(w.text(), "Change report")
	w.wantNoText(section, "<a href", "Earlier revisions")
}

// A list that shows no count in its heading cannot say "the count above includes them": the earlier revisions are counted in their heading, with those listed when some are not.
func TestTheHeadingOfTheEarlierRevisionsCountsThemAndSaysHowManyAreListed(t *testing.T) {
	revisions := make([]contract.ProposalReference, 62)
	for i := range revisions {
		revisions[i] = revisionRef("P2", fmt.Sprintf("R%d", i+1))
	}
	deps := pageDeps{reports: &fakeReports{report: revisionReport{Base: "V1", Report: smallReport(t)}}, logger: quietLogger()}
	for name, tc := range map[string]struct {
		current       int // the index of the current revision
		heading, more string
		listed        int
	}{
		"fifty or fewer":  {10, "Earlier revisions of this proposal: 10", "", 10},
		"more than fifty": {61, "Earlier revisions of this proposal: 61 (50 listed)", "11 more are not shown here. The count above includes them.", 50},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := reportOf(t.Context(), deps, "acme/widgets", revisions[tc.current], revisions)
			if err != nil {
				t.Fatal(err)
			}
			if got.EarlierHeading != tc.heading || got.EarlierMore != tc.more || len(got.Earlier) != tc.listed {
				t.Errorf("heading %q, more %q, %d listed; want %q, %q, %d", got.EarlierHeading, got.EarlierMore, len(got.Earlier), tc.heading, tc.more, tc.listed)
			}
		})
	}
}
