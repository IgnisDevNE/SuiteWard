package app

import (
	"context"
	"fmt"
	"html"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// What the pull request page shows (docs/contracts/runtime.md, "The pull request page"): the situation of the head and of each run, the tests by state with the meaning
// of each state, the tests of each run with the reason of each flagged test, the weakened tests, the kinds of change, the evidence of each run, and the lists. Rows are
// written as the page writes them: <tr><td>…</td></tr> with no space between the cells.

func pageRow(cells ...string) string {
	return "<tr><td>" + strings.Join(cells, "</td><td>") + "</td></tr>"
}

func pageHeader(cells ...string) string {
	return "<tr><th>" + strings.Join(cells, "</th><th>") + "</th></tr>"
}

// section is the part of the page under <h2>heading</h2>, up to the next <h2>.
func (w *pageWorld) section(page, heading string) string {
	w.t.Helper()
	open := "<h2>" + heading + "</h2>"
	start := strings.Index(page, open)
	if start < 0 {
		w.t.Fatalf("the page has no section %q:\n%s", heading, page)
	}
	rest := page[start+len(open):]
	if end := strings.Index(rest, "<h2>"); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// wantInOrder requires every fragment to be in text, each after the one before it.
func (w *pageWorld) wantInOrder(text string, fragments ...string) {
	w.t.Helper()
	at := 0
	for _, fragment := range fragments {
		i := strings.Index(text[at:], fragment)
		if i < 0 {
			w.t.Errorf("%q is not in the text after offset %d (or comes before the one that precedes it):\n%s", fragment, at, text)
			return
		}
		at += i + len(fragment)
	}
}

func (w *pageWorld) wantNoText(page string, phrases ...string) {
	w.t.Helper()
	for _, phrase := range phrases {
		if strings.Contains(page, phrase) {
			w.t.Errorf("the page has %q:\n%s", phrase, page)
		}
	}
}

func TestPullPageTopStatesThePullRequestTheHeadTheBaseAndTheTier(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects"))
	page := w.text()
	w.wantText(page, "Pull request: 5", "Repository: acme/widgets", "Commit: "+cjHead2[:7]+" (full SHA: "+cjHead2+")", "Base branch commit: "+bootHead, "Installation tier: isolated",
		"evidence reported by a test runner", "not a verdict")
	w.wantNoText(page, "assurance is reduced", "This pull request is not open")
	w.serve("co-located") // the enrollment recorded isolated: the effective tier is co-located
	w.wantText(w.text(), "Installation tier: co-located", commandtext.CoLocatedNotice)
}

func TestPullPageOfAClosedPullRequestShowsTheLastHeadRecorded(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects"))
	w.sync(map[int]string{4: bootHead}) // pull 5 is no longer open
	w.wantText(w.text(), "Commit: "+cjHead2[:7], "This pull request is not open: the results are those of the last commit recorded for it.")
}

func TestPullPageNeverCallsATestRunnerAnExecutor(t *testing.T) {
	for name, setup := range map[string]func(w *pageWorld){
		"both runs are complete": func(w *pageWorld) { w.completed(canonicalTests("pass", "fail"), receiptTests("detects", "fails")) },
		"no test runner is set up": func(w *pageWorld) {
			w.check.executorConfigured = false
			w.execPullFive(execHeadFiles(receiptDecl, "v2"))
		},
		"an attempt failed": func(w *pageWorld) {
			w.execPullFive(execHeadFiles(receiptDecl, "v2"))
			w.failAttempt(w.onlyJob(5, cjHead2, execution.PlanCanonical))
		},
		"the runner does not respond": func(w *pageWorld) {
			w.execPullFive(execHeadFiles(receiptDecl, "v2"))
			w.runnerSilentFor(45 * time.Second)
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newPageWorld(t)
			setup(w)
			if page := strings.ToLower(w.text()); strings.Contains(page, "executor") {
				t.Errorf("the page says executor, and a person reads test runner: %s", page)
			}
		})
	}
}

func TestPullPageSaysOneSentenceOfTheSituationOfTheHead(t *testing.T) {
	const (
		running      = "The tests of this commit have not finished yet."
		waiting      = "The tests of this commit are waiting for the test runner, which is not responding."
		notRun       = "The tests of this commit did not run."
		unblocked    = "The owner unblocked this commit, so its checks do not wait for its tests."
		someDidNot   = "SuiteWard ran some of the tests for this commit. The others did not run."
		ranAllTheirs = "SuiteWard ran the tests for this commit."
	)
	cases := map[string]struct {
		setup func(w *pageWorld)
		want  string
		not   []string
	}{
		"the runs are running": {func(w *pageWorld) { w.execPullFive(execHeadFiles(receiptDecl, "v2")) }, running, []string{waiting, notRun, unblocked, someDidNot, ranAllTheirs}},
		"the runner is silent": {func(w *pageWorld) {
			w.execPullFive(execHeadFiles(receiptDecl, "v2"))
			w.runnerSilentFor(45 * time.Second)
		}, waiting, []string{running, notRun}},
		"the runs did not run": {func(w *pageWorld) {
			w.check.executorConfigured = false
			w.execPullFive(execHeadFiles(receiptDecl, "v2"))
		}, notRun, []string{running, waiting, unblocked, someDidNot, ranAllTheirs}},
		"the owner unblocked the head": {func(w *pageWorld) {
			w.check.executorConfigured = false
			w.execPullFive(execHeadFiles(receiptDecl, "v2"))
			w.unblock(5, 600, cjHead2, w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt))
		}, unblocked, []string{running, waiting, someDidNot, ranAllTheirs}},
		"one run did not run": {func(w *pageWorld) {
			w.execPullFive(execHeadFiles(receiptDecl, "v2"))
			w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass"))
			w.silentFor(6 * time.Minute) // past the lease wait: the sweep ends the receipt job, which no runner leased
			w.mustCheck()
		}, someDidNot, []string{running, waiting, ranAllTheirs}},
		"both runs are complete": {func(w *pageWorld) { w.completed(canonicalTests("pass"), receiptTests("detects")) }, ranAllTheirs, []string{running, waiting, notRun, unblocked, someDidNot}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := newPageWorld(t)
			tc.setup(w)
			page := w.text()
			w.wantText(page, tc.want)
			w.wantNoText(page, tc.not...)
		})
	}
}

func TestPullPageSaysThatTheRunnerDoesNotRespondAndHowLongAgoItWasHeard(t *testing.T) {
	for silence, ago := range map[time.Duration]string{
		30 * time.Second: "30 seconds", 45 * time.Second: "45 seconds", 59 * time.Second: "59 seconds", time.Minute: "1 minute", 89 * time.Second: "1 minute", 90 * time.Second: "2 minutes",
		200 * time.Second: "3 minutes", 10 * time.Minute: "10 minutes",
	} {
		t.Run(silence.String(), func(t *testing.T) {
			w := newPageWorld(t)
			w.execPullFive(execHeadFiles(receiptDecl, "v2"))
			w.runnerSilentFor(silence)
			page := w.text()
			sentence := "The test runner is not responding. SuiteWard last heard from it about " + ago + " ago and keeps trying."
			w.wantText(page, "Approved tests: "+sentence, "Tests this change adds or edits: "+sentence)
			if got := strings.Count(w.section(page, "Situation"), "<li>"); got != 2 {
				t.Errorf("the situation has %d sentences, want one for each run:\n%s", got, page)
			}
		})
	}
	w := newPageWorld(t)
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	w.runnerSilentFor(30*time.Second - time.Millisecond)
	w.wantNoText(w.text(), "not responding", "<h2>Situation</h2>")
}

// A job that waits on GitHub waits for GitHub and not for the runner, and has its own bound.
func TestPullPageDoesNotSayThatAJobThatWaitsOnGitHubWaitsForTheRunner(t *testing.T) {
	w := newPageWorld(t)
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	w.waitOnGitHub(w.onlyJob(5, cjHead2, execution.PlanCanonical).ID)
	w.runnerSilentFor(45 * time.Second)
	page := w.text()
	w.wantText(page, "Tests this change adds or edits: The test runner is not responding.")
	w.wantNoText(page, "Approved tests: The test runner is not responding.")
}

func TestPullPageSaysWhyARunDidNotRunWithTheSentenceOfItsCode(t *testing.T) {
	w := newPageWorld(t)
	w.check.executorConfigured = false
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	page := w.text()
	sentence := " did not run. No test runner is set up for this SuiteWard installation."
	w.wantText(page, "Approved tests"+sentence, "Tests this change adds or edits"+sentence)
	w.wantNoText(page, "installation..", "(executor-not-configured)", "did not run: ")
}

func TestPullPageSaysWhichRunsTheOwnerUnblockedAndThatAJobThatTheCommandEndedHadWaited(t *testing.T) {
	const stopped = "SuiteWard had stopped waiting for the test runner, which was not responding."
	t.Run("the command ended two jobs that waited", func(t *testing.T) {
		w := newPageWorld(t)
		w.execPullFive(execHeadFiles(receiptDecl, "v2"))
		w.runnerSilentFor(45 * time.Second)
		w.unblock(5, 600, cjHead2, w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt))
		page := w.text()
		w.wantText(page, "The owner unblocked this commit, so its checks do not wait for the approved tests and the receipt.", stopped,
			"Approved tests did not run. The test runner did not respond.")
		w.wantNoText(page, "The test runner is not responding. SuiteWard last heard")
	})
	t.Run("the owner unblocked a job that was unavailable already", func(t *testing.T) {
		w := newPageWorld(t)
		w.check.executorConfigured = false
		w.execPullFive(execHeadFiles(receiptDecl, "v2"))
		w.unblock(5, 600, cjHead2, w.onlyJob(5, cjHead2, execution.PlanReceipt))
		page := w.text()
		w.wantText(page, "The owner unblocked this commit, so its checks do not wait for the receipt.")
		w.wantNoText(page, stopped, "the approved tests and the receipt", "do not wait for the approved tests")
		w.unblock(5, 601, cjHead2, w.onlyJob(5, cjHead2, execution.PlanCanonical))
		w.wantText(w.text(), "The owner unblocked this commit, so its checks do not wait for the approved tests and the receipt.")
	})
	t.Run("nobody unblocked", func(t *testing.T) {
		w := newPageWorld(t)
		w.check.executorConfigured = false
		w.execPullFive(execHeadFiles(receiptDecl, "v2"))
		w.wantNoText(w.text(), "The owner unblocked", stopped)
	})
}

// The runner is back (docs/contracts/runtime.md, "The early unblock and its hold", "The runner is back"): a job with an unblock row, the reason no-executor and an end
// earlier than the last real request of the runner.
func TestPullPageSaysThatTheRunnerRespondsAgainOnlyAfterAnUnblockedRunEndedForNoExecutor(t *testing.T) {
	const back = "The test runner is responding again. A new push runs the tests."
	ended := func(w *pageWorld) { // both jobs wait, and the command ends them at the clock + 31 s
		w.execPullFive(execHeadFiles(receiptDecl, "v2"))
		w.unblock(5, 600, cjHead2, w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt))
	}
	heardAt := func(w *pageWorld, offset time.Duration) {
		w.server.executorStatus.liveness = execution.NewLiveness(w.clock)
		w.server.executorStatus.liveness.Saw(w.clock.Add(offset))
		w.at = w.clock.Add(time.Hour)
	}
	t.Run("the runner was heard after the end", func(t *testing.T) {
		w := newPageWorld(t)
		ended(w)
		heardAt(w, 32*time.Second)
		w.wantText(w.text(), back)
	})
	t.Run("a start of the service is not a request of the runner", func(t *testing.T) {
		w := newPageWorld(t)
		ended(w)
		w.server.executorStatus.liveness = execution.NewLiveness(w.clock.Add(time.Hour))
		w.at = w.clock.Add(time.Hour)
		w.wantNoText(w.text(), back)
	})
	t.Run("the runner was heard before the end", func(t *testing.T) {
		w := newPageWorld(t)
		ended(w)
		heardAt(w, 10*time.Second)
		w.wantNoText(w.text(), back)
	})
	t.Run("a job that nobody unblocked", func(t *testing.T) {
		w := newPageWorld(t)
		w.execPullFive(execHeadFiles(receiptDecl, "v2"))
		w.silentFor(6 * time.Minute) // the sweep ends the jobs for no-executor
		w.mustCheck()
		heardAt(w, 10*time.Minute)
		w.wantNoText(w.text(), back)
	})
	t.Run("a job that the owner unblocked after the lease wait", func(t *testing.T) {
		w := newPageWorld(t)
		w.execPullFive(execHeadFiles(receiptDecl, "v2"))
		w.silentFor(6 * time.Minute)
		w.mustCheck()
		w.unblock(5, 600, cjHead2, w.onlyJob(5, cjHead2, execution.PlanCanonical))
		heardAt(w, 10*time.Minute)
		w.wantText(w.text(), back)
	})
	t.Run("a pull request that is closed", func(t *testing.T) {
		w := newPageWorld(t)
		ended(w)
		heardAt(w, 32*time.Second)
		w.sync(map[int]string{4: bootHead})
		w.wantNoText(w.text(), back)
	})
}

// stateCounts are the counts of the tests by state of the page's table, by the name of the state: the canonical run, then the receipt.
func stateRows(canonical, receipt map[string]string) []string {
	var rows []string
	for _, m := range commandtext.StateMeanings() {
		cells := []string{"-", "-"}
		if n, ok := canonical[m.State]; ok && m.Canonical {
			cells[0] = n
		}
		if n, ok := receipt[m.State]; ok && m.Receipt {
			cells[1] = n
		}
		rows = append(rows, pageRow(m.State, m.Meaning, cells[0], cells[1]))
	}
	return rows
}

func TestPullPageStatesTheMeaningOfEachStateAndTheCountsOfEachRun(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass", "pass", "fail", "skipped", "missing", "flaky", "excluded"), receiptTests("detects", "inert", "new_api", "fails", "missing", "flaky", "skipped", "excluded", "detects"))
	table := w.section(w.text(), "Tests by state")
	want := stateRows(
		map[string]string{"PASS": "2", "FAIL": "1", "SKIPPED": "1", "MISSING": "1", "FLAKY": "1", "EXCLUDED": "1"},
		map[string]string{"DETECTS": "2", "INERT": "1", "NEW API": "1", "FAILS": "1", "MISSING": "1", "FLAKY": "1", "SKIPPED": "1", "EXCLUDED": "1"})
	w.wantInOrder(table, append([]string{pageHeader("State", "What it means", "Approved tests", "This change's tests")}, want...)...)
	if got := strings.Count(table, "<tr><td>"); got != 10 {
		t.Errorf("the table has %d rows of states, want the ten of the contract", got)
	}
}

// A run that did not complete has no count: a dash, whatever the state.
func TestPullPageHasNoCountForARunThatDidNotComplete(t *testing.T) {
	w := newPageWorld(t)
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass", "fail")) // the receipt run is pending
	table := w.section(w.text(), "Tests by state")
	w.wantInOrder(table, stateRows(map[string]string{"PASS": "1", "FAIL": "1", "SKIPPED": "0", "MISSING": "0", "FLAKY": "0", "EXCLUDED": "0"}, nil)...)
}

func TestPullPageStatesHowManyTestsNoRunCanCoverTheLinesToLookAtAndTheFilesThatCannotBeRead(t *testing.T) {
	w := newPageWorld(t)
	head := execDeclaration("advisory", "advisory") + "# e2e\n"
	head = strings.Replace(head, "include: [/tests/**]", "include: [/tests/**, /e2e/**]", 1)
	files := execHeadFiles(head, "v2")
	files["e2e/x_test.go"] = "package e2e\n"
	w.reader.files = map[int][]github.PullFile{5: {{Path: "e2e/x_test.go", Status: "added"}, {Path: "tests/a_test.go", Status: "modified"}}}
	w.execPullFive(files)
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass", "excluded"))
	w.finish(w.onlyJob(5, cjHead2, execution.PlanReceipt), receiptTests("detects", "excluded"), func(r *execution.Result) {
		r.Markers = []execution.Marker{{Kind: "env-read", Path: "tests/a_test.go", Line: 12}, {Kind: "testing-import", Path: "tests/b_test.go", Line: 3}}
		r.Unparsed = []string{"tests/broken_test.go"}
	})
	table := w.section(w.text(), "Tests by state")
	w.wantText(table, "Approved tests that SuiteWard cannot run, so they are not covered: 1.", "Tests of this change that SuiteWard cannot run, so they are not covered: 1.",
		"Lines to look at in the tests of this change: 2. They are listed below, under the tests of this change.",
		"Changed test files that cannot be read: 1. They are listed below, under the tests of this change.",
		"Files not evaluated because their pattern is not approved yet: 1. They are listed below, under the tests of this change.")
	clean := newPageWorld(t)
	clean.completed(canonicalTests("pass"), receiptTests("detects"))
	clean.wantNoText(clean.section(clean.text(), "Tests by state"), "that SuiteWard cannot run", "Lines to look at", "cannot be read", "not evaluated")
}

const (
	failedText   = "The test failed or did not finish."
	noResultText = "The test gave no result. The run may have stopped before it, or the test is not in the code."
	buildRule    = "A build rule, such as a tag or a file name like `_windows_test.go`, leaves this test out of the test run."
)

func TestPullPageListsTheApprovedTestsWithTheReasonOfEachFlaggedOneWorstFirst(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass", "fail", "skipped", "missing", "flaky", "excluded"), receiptTests("detects"))
	section := w.section(w.text(), "Approved tests")
	w.wantInOrder(section, "5 tests ran.", pageHeader("State", "Test", "Package", "File", "Reason", "Note"),
		pageRow("FAIL", "TestC01", ".", "a_test.go", failedText, ""),
		pageRow("MISSING", "TestC03", ".", "a_test.go", noResultText, ""),
		pageRow("FLAKY", "TestC04", ".", "a_test.go", "The result changed from run to run: passed, failed, passed.", ""),
		pageRow("SKIPPED", "TestC02", ".", "a_test.go", "", ""),
		pageRow("EXCLUDED", "TestC05", ".", "a_test.go", buildRule, ""),
		pageRow("PASS", "TestC00", ".", "a_test.go", "", ""))
}

func TestPullPageSaysHowManyApprovedTestsRan(t *testing.T) {
	for want, tests := range map[string][]execution.Test{"0 tests ran.": nil, "1 test ran.": canonicalTests("pass"), "3 tests ran.": canonicalTests("pass", "fail", "skipped", "excluded")} {
		w := newPageWorld(t)
		w.completed(tests, receiptTests("detects"))
		w.wantText(w.section(w.text(), "Approved tests"), want)
	}
}

func TestPullPageListsTheTestsOfTheReceiptWithTheReasonOfEachFlaggedOneWorstFirst(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects", "inert", "new_api", "fails", "missing", "flaky", "skipped", "excluded"))
	section := w.section(w.text(), "Tests this change adds or edits")
	w.wantInOrder(section, pageHeader("State", "Test", "Package", "File", "Reason", "Note"),
		pageRow("FAILS", "TestR03", ".", "a_test.go", "The test failed or did not finish with this change.", ""),
		pageRow("MISSING", "TestR04", ".", "a_test.go", noResultText, ""),
		pageRow("FLAKY", "TestR05", ".", "a_test.go", "The result changed from run to run with this change: passed, failed, passed.", ""),
		pageRow("NEW API", "TestR02", ".", "a_test.go", "The test passes with this change. The code before the change could not build it, because it uses something new.", ""),
		pageRow("INERT", "TestR01", ".", "a_test.go", "The test passes with this change. On the code before the change it passed or skipped itself, so it does not show that the change does anything.", ""),
		pageRow("SKIPPED", "TestR06", ".", "a_test.go", "", ""),
		pageRow("EXCLUDED", "TestR07", ".", "a_test.go", buildRule, ""),
		pageRow("DETECTS", "TestR00", ".", "a_test.go", "", ""))
}

func TestPullPageGivesTheReasonOfEachSideThatVariedAndOfAMissingBase(t *testing.T) {
	w := newPageWorld(t)
	tests := []execution.Test{
		{Package: ".", Name: "TestBoth", File: "a_test.go", Kind: execution.KindAdded, State: "flaky", HeadRuns: []string{"pass", "fail"}, BaseRuns: []string{"pass", "build-error"}},
		{Package: ".", Name: "TestBase", File: "a_test.go", Kind: execution.KindAdded, State: "flaky", HeadRuns: []string{"pass"}, BaseRuns: []string{"pass", "fail", "skip"}},
		{Package: ".", Name: "TestNoBase", File: "a_test.go", Kind: execution.KindAdded, State: "missing", HeadRuns: []string{"pass"}, BaseRuns: []string{"absent"}},
		{Package: ".", Name: "TestDidNotBuild", File: "a_test.go", Kind: execution.KindAdded, State: "fails", HeadRuns: []string{"build-error"}, BaseRuns: []string{"pass"}},
	}
	w.completed(canonicalTests("pass"), tests)
	section := w.section(w.text(), "Tests this change adds or edits")
	w.wantText(section,
		pageRow("FLAKY", "TestBoth", ".", "a_test.go", "The result changed from run to run with this change: passed, failed. On the code before the change the result changed from run to run: passed, did not build.", ""),
		pageRow("FLAKY", "TestBase", ".", "a_test.go", "On the code before the change the result changed from run to run: passed, failed, skipped.", ""),
		pageRow("MISSING", "TestNoBase", ".", "a_test.go", "The test gave no result on the code before the change.", ""),
		pageRow("FAILS", "TestDidNotBuild", ".", "a_test.go", "The code did not build with this change, so the test could not run.", ""))
}

func TestPullPageSortsEachGroupOfRowsByPackageAndName(t *testing.T) {
	w := newPageWorld(t)
	failing := func(pkg, name string) execution.Test {
		return execution.Test{Package: pkg, Name: name, File: "a_test.go", Kind: execution.KindDeclared, State: "fail", HeadRuns: []string{"fail"}}
	}
	w.completed([]execution.Test{failing("z", "TestA"), failing("a", "TestB"), failing("a", "TestA")}, receiptTests("detects"))
	w.wantInOrder(w.section(w.text(), "Approved tests"), pageRow("FAIL", "TestA", "a", "a_test.go", failedText, ""), pageRow("FAIL", "TestB", "a", "a_test.go", failedText, ""),
		pageRow("FAIL", "TestA", "z", "a_test.go", failedText, ""))
}

func edited(name string, state, receiptState string, headRuns, baseRuns []string) (canonical, receipt execution.Test) {
	canonical = execution.Test{Package: ".", Name: name, File: "a_test.go", Kind: execution.KindDeclared, State: state, HeadRuns: headRuns}
	receipt = execution.Test{Package: ".", Name: name, File: "a_test.go", Kind: execution.KindChanged, State: receiptState, HeadRuns: []string{"pass"}, BaseRuns: baseRuns}
	return canonical, receipt
}

const (
	editsSecond   = " A test that is edited can pass by checking less. Read the edit before you approve."
	weakenedOne   = "1 approved test that this change edits fails, is missing or is flaky on this commit, in the version that was approved." + editsSecond
	weakenedTwo   = "2 approved tests that this change edits fail, are missing or are flaky on this commit, in the version that was approved." + editsSecond
	canonicalNote = "Weakened: this change edits this test."
	receiptNote   = "Weakened: the approved version of this test fails on this commit."
)

func weakenedWorld(t *testing.T) *pageWorld {
	t.Helper()
	w := newPageWorld(t)
	flaky, flakyR := edited("TestEdited", "flaky", "detects", []string{"pass", "fail", "pass"}, []string{"fail"})
	failing, failingR := edited("TestAlsoEdited", "fail", "detects", []string{"fail"}, []string{"fail"})
	canonical := []execution.Test{flaky, failing,
		{Package: ".", Name: "TestUntouched", File: "a_test.go", Kind: execution.KindDeclared, State: "fail", HeadRuns: []string{"fail"}},
		{Package: ".", Name: "TestAdded", File: "a_test.go", Kind: execution.KindDeclared, State: "fail", HeadRuns: []string{"fail"}},
		{Package: ".", Name: "TestExcluded", File: "a_test.go", Kind: execution.KindDeclared, State: "excluded", ExcludedReason: execution.ExcludedBuildConstraint},
		{Package: ".", Name: "TestPassing", File: "a_test.go", Kind: execution.KindDeclared, State: "pass", HeadRuns: []string{"pass"}}}
	receipt := []execution.Test{flakyR, failingR,
		{Package: ".", Name: "TestAdded", File: "a_test.go", Kind: execution.KindAdded, State: "detects", HeadRuns: []string{"pass"}, BaseRuns: []string{"fail"}}, // an added test has no approved version
		{Package: ".", Name: "TestExcluded", File: "a_test.go", Kind: execution.KindChanged, State: "excluded", ExcludedReason: execution.ExcludedBuildConstraint}}
	w.completed(canonical, receipt)
	return w
}

func TestPullPageCountsTheWeakenedTestsAndMarksThemInBothTablesWithTheWeakenedRowsFirst(t *testing.T) {
	w := weakenedWorld(t)
	page := w.text()
	w.wantText(page, weakenedTwo)
	w.wantInOrder(w.section(page, "Approved tests"),
		pageRow("FAIL", "TestAlsoEdited", ".", "a_test.go", failedText, canonicalNote), // the weakened rows come first, whatever their state
		pageRow("FLAKY", "TestEdited", ".", "a_test.go", "The result changed from run to run: passed, failed, passed.", canonicalNote),
		pageRow("FAIL", "TestAdded", ".", "a_test.go", failedText, ""),
		pageRow("FAIL", "TestUntouched", ".", "a_test.go", failedText, ""),
		pageRow("EXCLUDED", "TestExcluded", ".", "a_test.go", buildRule, ""),
		pageRow("PASS", "TestPassing", ".", "a_test.go", "", ""))
	w.wantInOrder(w.section(page, "Tests this change adds or edits"),
		pageRow("DETECTS", "TestAlsoEdited", ".", "a_test.go", "", receiptNote),
		pageRow("DETECTS", "TestEdited", ".", "a_test.go", "", receiptNote),
		pageRow("EXCLUDED", "TestExcluded", ".", "a_test.go", buildRule, ""),
		pageRow("DETECTS", "TestAdded", ".", "a_test.go", "", ""))
	if got := strings.Count(page, "Weakened:"); got != 4 {
		t.Errorf("%d markers on the page, want 2 in each table", got)
	}
}

func TestPullPageSaysOneWeakenedTestInTheSingular(t *testing.T) {
	w := newPageWorld(t)
	canonical, receipt := edited("TestEdited", "fail", "detects", []string{"fail"}, []string{"fail"})
	w.completed([]execution.Test{canonical}, []execution.Test{receipt})
	w.wantText(w.text(), weakenedOne)
}

// The signal is not stated as an absence: it is known only when both runs are complete, and read as nothing otherwise.
func TestPullPageStatesNoWeakenedTestWhenNoneIsOrWhenTheCountIsUnknown(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("fail"), receiptTests("detects")) // no test is edited
	w.wantNoText(w.text(), "this change edits fail", "this change edits fails", "Weakened:")

	w = newPageWorld(t)
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	canonical, _ := edited("TestEdited", "fail", "detects", []string{"fail"}, []string{"fail"})
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), []execution.Test{canonical})
	w.silentFor(6 * time.Minute) // the receipt job is never run: the sweep ends it
	w.mustCheck()
	w.wantNoText(w.text(), "this change edits fail", "Weakened:")
}

// After the owner approves, the head has another canonical job, under the manifest of the revision, which the weakened set never reads: the signal does not change.
func TestPullPageStillCountsTheWeakenedTestsWhenTheHeadHasAnotherCanonicalJobUnderTheManifestOfTheRevision(t *testing.T) {
	w := newPageWorld(t)
	canonical, receipt := edited("TestEdited", "fail", "detects", []string{"fail"}, []string{"fail"})
	first, _ := w.completed([]execution.Test{canonical}, []execution.Test{receipt})
	spec := first.Spec
	spec.GoverningDigest, spec.GoverningSource = artifactDigest("the manifest of the revision"), "P2-R1"
	approved := w.seedJob(spec)
	w.finish(approved, canonicalTests("pass"))
	page := w.text()
	w.wantText(page, weakenedOne)
	w.wantInOrder(w.section(page, "Tests this change adds or edits"), pageRow("DETECTS", "TestEdited", ".", "a_test.go", "", receiptNote))
	w.wantInOrder(w.section(page, "Approved tests"), pageRow("PASS", "TestC00", ".", "a_test.go", "", "")) // the table is that of the job the gates read
}

func TestPullPageSaysWhetherEachKindOfChangeIsMetAndWhyNot(t *testing.T) {
	const (
		blocking     = "A test fails, is missing or is flaky, or a changed test file cannot be read."
		noDetection  = "No test fails without the change, so none shows that it fixes something."
		noDetection2 = "No test fails without the change or needs new code, so none shows that it adds something."
		notInert     = "A test behaves differently with the change, so this does not look like a refactor."
	)
	met, notMet := "Met", "Not met"
	for name, tc := range map[string]struct {
		tests []execution.Test
		want  []string
	}{
		"a test that detects": {receiptTests("detects", "inert"), []string{pageRow("fix", met, "-"), pageRow("feature", met, "-"), pageRow("refactor", notMet, notInert)}},
		"only inert tests":    {receiptTests("inert"), []string{pageRow("fix", notMet, noDetection), pageRow("feature", notMet, noDetection2), pageRow("refactor", met, "-")}},
		"a failing test":      {receiptTests("detects", "fails"), []string{pageRow("fix", notMet, blocking), pageRow("feature", notMet, blocking), pageRow("refactor", notMet, blocking)}},
	} {
		t.Run(name, func(t *testing.T) {
			w := newPageWorld(t)
			w.completed(canonicalTests("pass"), tc.tests)
			section := w.section(w.text(), "The kinds of change")
			w.wantInOrder(section, append([]string{pageHeader("Kind", "Met", "Why")}, tc.want...)...)
			w.wantNoText(section, "No receipt to judge.")
		})
	}
}

func TestPullPageHasNoKindOfChangeToJudgeWithoutAReceiptThatIsCompleteAndNotEmpty(t *testing.T) {
	for name, setup := range map[string]func(w *pageWorld){
		"an empty receipt": func(w *pageWorld) { w.completed(canonicalTests("pass"), receiptTests("excluded")) },
		"a receipt that is not complete": func(w *pageWorld) {
			w.execPullFive(execHeadFiles(receiptDecl, "v2"))
			w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass"))
		},
		"a receipt that does not apply": func(w *pageWorld) { w.execPullFive(execHeadFiles(receiptDecl+"# a comment\n", "v1")) },
		"a pull request with no job": func(w *pageWorld) {
			w.tree(cjHead2, execHeadFiles(receiptDecl, "v1"))
			w.sync(map[int]string{4: bootHead, 5: cjHead2})
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newPageWorld(t)
			setup(w)
			section := w.section(w.text(), "The kinds of change")
			w.wantText(section, "No receipt to judge.")
			w.wantNoText(section, "<table>", "Not met")
		})
	}
}

func TestPullPageStatesThePreviewModeAndSaysThatNothingReadsIt(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects"))
	w.wantText(w.section(w.text(), "The kinds of change"), "No preview mode is recorded for this pull request.")
	w.previews.mode, w.previews.found = "feature", true
	w.previews.asked = nil
	w.wantText(w.section(w.text(), "The kinds of change"), "Preview mode: feature. It is a preview: no check and no approval reads it.")
	if want := fmt.Sprint(bootRepoID, " 5 ", bootTime.UTC().Format(time.RFC3339)); len(w.previews.asked) != 1 || w.previews.asked[0] != want {
		t.Errorf("the preview mode was asked as %v, want once as %q: since the current enrollment", w.previews.asked, want)
	}
}

func TestModeRowSaysWhetherASummaryIsMetAndRefusesOneNoTextCanSay(t *testing.T) {
	for name, summary := range map[string]domain.ModeSummary{
		"a reason that no words exist for": {Verdict: domain.VerdictNotMet, Reason: "a-reason-from-the-future"},
		"no reason at all":                 {Verdict: domain.VerdictNotMet},
		"a verdict that is none":           {},
		"the verdict of no mode":           {Verdict: domain.VerdictNotJudged},
	} {
		if met, why, err := modeRow(domain.ModeFix, summary); err == nil {
			t.Errorf("%s: met %v, why %q, no error", name, met, why)
		}
	}
	if met, why, err := modeRow(domain.ModeFix, domain.ModeSummary{Verdict: domain.VerdictMet}); err != nil || !met || why != "" {
		t.Errorf("a met summary: %v, %q, %v", met, why, err)
	}
	if met, why, err := modeRow(domain.ModeFix, domain.ModeSummary{Verdict: domain.VerdictNotMet, Reason: domain.ReasonNoDetection}); err != nil || met ||
		why != "No test fails without the change, so none shows that it fixes something." {
		t.Errorf("a summary that is not met: %v, %q, %v", met, why, err)
	}
}

func TestPullPageStatesTheEvidenceOfEachRunAsATable(t *testing.T) {
	w := newPageWorld(t)
	canonical, receipt := w.completed(canonicalTests("pass"), receiptTests("detects"))
	section := w.section(w.text(), "How these results were made")
	digest := canonical.GoverningDigest.String()
	w.wantInOrder(section, pageHeader("What", "Approved tests", "Tests this change adds or edits"),
		pageRow("State", "complete", "complete"),
		pageRow("Approved file list (fingerprint)", digest+" (source V1)", digest+" (source V1)"),
		pageRow("Policy levels", "canonical advisory, receipt advisory", "canonical advisory, receipt advisory"),
		pageRow("Profile", "go", "go"),
		pageRow("Test runner version", "0.4.0-test", "0.4.0-test"),
		pageRow("Test runner program fingerprint (SHA-256)", artifactHash("binary"), artifactHash("binary")),
		pageRow("Container program", "podman 5.5.0", "podman 5.5.0"),
		pageRow("Go toolchain", "go1.25.1", "go1.25.1"),
		pageRow("Image fingerprint (digest)", artifactHash("image"), artifactHash("image")))
	w.wantText(section, "<tr><td>Started</td><td>", "<tr><td>Ended</td><td>", pageRow("Common ancestor commit (merge base)", "-", receipt.BaseSHA))
	w.wantNoText(section, "Attempts", "Failures")
}

func TestPullPageStatesAJobThatWasNotRunWithItsReasonAndNoEvidence(t *testing.T) {
	w := newPageWorld(t)
	w.check.executorConfigured = false // the jobs are created already unavailable, and no results row exists
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	section := w.section(w.text(), "How these results were made")
	const notConfigured = "No test runner is set up for this SuiteWard installation."
	w.wantText(section, pageRow("State", "not run", "not run"), pageRow("Last reason", notConfigured, notConfigured))
	w.wantNoText(section, "executor-not-configured")
	w.wantNoText(section, "Approved file list", "Test runner version", "Image fingerprint", "Started")
}

func TestPullPageStatesWhatARunThatWasTriedAgainSaw(t *testing.T) {
	w := newPageWorld(t)
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	canonical := w.onlyJob(5, cjHead2, execution.PlanCanonical) // the older job: it is leased first
	w.failAttempt(canonical)
	section := w.section(w.text(), "How these results were made")
	w.wantText(section, pageRow("State", "pending", "pending"), pageRow("Attempts", "1", "0"), pageRow("Failures", "1", "0"),
		pageRow("Last reason", "The test runner failed in a way that SuiteWard cannot name.", "-"))
	w.wantNoText(section, "executor-error")
}

func TestPullPageListsTheChangedFilesAtMostOneHundredAndHowManyThereAre(t *testing.T) {
	w := newPageWorld(t)
	files := execHeadFiles(receiptDecl, "v2")
	for i := range 150 {
		files[fmt.Sprintf("tests/n%03d_test.go", i)] = "package a\n"
	}
	w.execPullFive(files)
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass"))
	w.finish(w.onlyJob(5, cjHead2, execution.PlanReceipt), receiptTests("detects"))
	section := w.section(w.text(), "Tests this change adds or edits")
	w.wantText(section, "<h3>Files that this revision adds or changes under the approved patterns: 151 (100 listed)</h3>", "<li>tests/a_test.go</li>", "<li>tests/n098_test.go</li>")
	w.wantNoText(section, "tests/n099_test.go")
}

func TestPullPageListsWhatIsNotEvaluatedTheSubmodulesTheMarkersAndTheFilesThatDoNotParse(t *testing.T) {
	w := newPageWorld(t)
	head := execDeclaration("advisory", "advisory") + "# e2e\n"
	head = strings.Replace(head, "include: [/tests/**]", "include: [/tests/**, /e2e/**]", 1)
	files := execHeadFiles(head, "v2")
	files["e2e/x_test.go"] = "package e2e\n"
	w.reader.files = map[int][]github.PullFile{5: {{Path: "e2e/x_test.go", Status: "added"}, {Path: "tests/a_test.go", Status: "modified"}}}
	w.execPullFive(files)
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass"))
	w.finish(w.onlyJob(5, cjHead2, execution.PlanReceipt), receiptTests("detects"), func(r *execution.Result) {
		r.Submodules = []string{"vendor/sub"}
		r.Markers = []execution.Marker{{Kind: "env-read", Path: "tests/a_test.go", Line: 12}, {Kind: "testing-import", Path: "tests/b_test.go", Line: 3}}
		r.Unparsed = []string{"tests/broken_test.go"}
	})
	section := w.section(w.text(), "Tests this change adds or edits")
	w.wantText(section, "<h3>Files not evaluated because their pattern is not approved yet: 1</h3>", "<li>e2e/x_test.go</li>", "<h3>Skipped submodules: 1</h3>", "<li>vendor/sub</li>",
		"<h3>Lines to look at: 2</h3>", "<li>env-read: tests/a_test.go line 12</li>", "<li>testing-import: tests/b_test.go line 3</li>",
		"<h3>Changed test files that cannot be read: 1</h3>", "<li>tests/broken_test.go</li>")
}

// Everything that comes from the repository or from the test runner is shown escaped: the names of tests, packages and files, the paths of markers, submodules and files
// that do not parse, and the text of the evidence. A character that hides itself is shown as an escape.
func TestPullPageEscapesWhatTheRepositoryAndTheTestRunnerSay(t *testing.T) {
	w := newPageWorld(t)
	hostile := []execution.Test{
		{Package: "pkg\n<b>pk</b>", Name: "Test<img src=x onerror=alert(1)>&+", File: "a\u202e_test.go", Kind: execution.KindAdded, State: "detects", HeadRuns: []string{"pass"}, BaseRuns: []string{"fail"}},
		{Package: ".", Name: "Test\\n\u200bZero\xff", File: "b_test.go", Kind: execution.KindAdded, State: "inert", HeadRuns: []string{"pass"}, BaseRuns: []string{"pass"}},
	}
	w.completed(canonicalTests("pass"), hostile, func(r *execution.Result) {
		r.Submodules = []string{"sub<script>"}
		r.Markers = []execution.Marker{{Kind: "env-read", Path: "x\ny<i>.go", Line: 1}}
		r.Unparsed = []string{"<u>_test.go"}
		r.Runtime = "podman <5>"
	})
	raw := w.get("127.0.0.1:8080", pullPath).Body.String()
	for _, banned := range []string{"<img", "<b>", "<script>", "<i>", "<u>", "\u202e", "\u200b", "pkg\n", "x\ny", "<5>", "\xff"} {
		if strings.Contains(raw, banned) {
			t.Errorf("the page holds %q unescaped", banned)
		}
	}
	page := html.UnescapeString(raw)
	w.wantText(page, `pkg\n<b>pk</b>`, `Test<img src=x onerror=alert(1)>&+`, `a\u202e_test.go`, `Test\\n\u200bZero\xff`, `sub<script>`, `x\ny<i>.go`, `<u>_test.go`, `podman <5>`)
}

func artifactDigest(text string) artifact.Digest { return artifact.Hash([]byte(text)) }

// failAttempt leases job, which must be the oldest ready one, and fails the attempt with a reason that is tried again: the job is pending again with one failure.
func (w *pageWorld) failAttempt(job execution.Job) {
	w.t.Helper()
	leased, found, err := w.jobs.Lease(w.t.Context(), execution.LeaseRequest{Now: w.clock, LeaseID: fmt.Sprintf("%032x", 7), JobTimeout: time.Hour, GitHubWait: time.Hour},
		func(context.Context, execution.Job) error { return nil })
	if err != nil || !found || leased.ID != job.ID {
		w.t.Fatalf("lease: job %d, found %v, %v", leased.ID, found, err)
	}
	failed := execution.Result{LeaseID: leased.LeaseID, Report: execution.Report{Status: execution.StateUnavailable, Reason: execution.ReasonExecutorError, ExecutorVersion: "0.4.0-test",
		ExecutorSHA256: artifact.Hash([]byte("binary")), StartedAt: w.clock.Add(-time.Minute), EndedAt: w.clock, BodyDigest: artifact.Hash([]byte("failed"))}}
	if outcome, err := w.jobs.Complete(w.t.Context(), job.ID, failed, w.clock, time.Hour); err != nil || outcome.State != execution.StatePending {
		w.t.Fatalf("the failed attempt: %+v, %v", outcome, err)
	}
}

// A job that waits on GitHub has not ended: the page says it waits, and never that SuiteWard could not read the commit for too long, which is what a job says that ended on
// the bound.
func TestPullPageSaysThatAJobThatWaitsOnGitHubWaitsAndOnlyAJobThatEndedOnTheBoundCouldNotReadForTooLong(t *testing.T) {
	const waiting, ended = "SuiteWard is waiting for GitHub to answer.", "SuiteWard could not read this commit from GitHub for too long."
	w := newPageWorld(t)
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	canonical := w.onlyJob(5, cjHead2, execution.PlanCanonical)
	w.waitOnGitHub(canonical.ID)
	page := w.text()
	w.wantText(w.section(page, "How these results were made"), pageRow("State", "pending", "pending"), pageRow("Last reason", waiting, "-"))
	w.wantNoText(page, "for too long", "did not run")

	if _, err := w.jobs.GitHubFailure(t.Context(), canonical.ID, w.clock.Add(time.Hour), w.check.executor.GitHubWait); err != nil { // the first failure is older than the bound
		t.Fatal(err)
	}
	page = w.text()
	w.wantText(page, pageRow("Last reason", ended, "-"), "Approved tests did not run. "+ended)
	w.wantNoText(page, waiting)
}

// unapprovedFiles cuts its list at 100 and stores no total, so a list of exactly 100 says that there are at least 100.
func TestPullPageSaysThatAtLeastOneHundredFilesAreNotEvaluatedWhenTheListHoldsOneHundred(t *testing.T) {
	const heading = "Files not evaluated because their pattern is not approved yet"
	w := newPageWorld(t)
	head := strings.Replace(execDeclaration("advisory", "advisory")+"# e2e\n", "include: [/tests/**]", "include: [/tests/**, /e2e/**]", 1)
	files := execHeadFiles(head, "v2")
	changed := []github.PullFile{{Path: "tests/a_test.go", Status: "modified"}}
	for i := range 120 {
		path := fmt.Sprintf("e2e/x%03d_test.go", i)
		files[path] = "package e2e\n"
		changed = append(changed, github.PullFile{Path: path, Status: "added"})
	}
	w.reader.files = map[int][]github.PullFile{5: changed}
	w.execPullFive(files)
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass"))
	w.finish(w.onlyJob(5, cjHead2, execution.PlanReceipt), receiptTests("detects"))
	page := w.text()
	w.wantText(page, "<h3>"+heading+": at least 100 (100 listed)</h3>", heading+": at least 100.")
	w.wantNoText(page, heading+": 100")
}

// pageStaleLevels is an execution store whose canonical job was created under another declaration: it says that the receipt is off.
type pageStaleLevels struct{ execution.Store }

func (s pageStaleLevels) HeadJobs(ctx context.Context, repositoryID int64, pull int, head string, plan execution.Plan) ([]execution.Job, error) {
	jobs, err := s.Store.HeadJobs(ctx, repositoryID, pull, head, plan)
	if plan == execution.PlanCanonical {
		for i := range jobs {
			jobs[i].CanonicalLevel, jobs[i].ReceiptLevel = domain.CanonicalLevelRequired, domain.ReceiptLevelOff
		}
	}
	return jobs, err
}

// The level of each plan is that of its own job: an old canonical job that outlived a change of the declaration does not hide a receipt job that waits for the runner.
func TestPullPageTakesTheLevelOfEachPlanFromItsOwnJob(t *testing.T) {
	w := newPageWorld(t)
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass")) // complete, and created under a declaration with the receipt off
	w.pages.executions = pageStaleLevels{Store: w.jobs}
	w.runnerSilentFor(45 * time.Second)
	page := w.text()
	w.wantText(page, "Tests this change adds or edits: The test runner is not responding. SuiteWard last heard from it about 45 seconds ago and keeps trying.",
		"The tests of this commit are waiting for the test runner, which is not responding.")
	w.wantNoText(page, "Approved tests: The test runner is not responding")
}
