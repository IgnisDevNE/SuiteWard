package commandtext_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The results comment of a head (docs/contracts/runtime.md, "The comments of a head", "The text of the results comment"): counts and the general situation, in nine blocks
// and a link, each a paragraph. The sentences below are the contract's, written out so that the tests pin them.

const (
	resultsHead = "0123456789abcdef0123456789abcdef01234567" // its short form is 0123456
	resultsLink = "http://127.0.0.1:8080/pulls/acme/widgets/7"

	// The link note of the revision comment, which the comments repeat after their link.
	linkNote = "This link opens on the computer that runs SuiteWard. If SuiteWard runs on a separate server, forward that server's HTTP port to your computer first. To open the link in a new tab, Ctrl+click it (Cmd+click on a Mac, or middle-click)."

	resultsEvidence = "These results are what the test runner saw. They are evidence, not a verdict."
	// The offer of the unblock (M2.1c, SW-1; docs/plan/phases/M2.1c.md, text 4), shared by the results comment and the not-responding comment.
	unblockOffer = "To go on without these tests, the owner can comment `/suiteward unblock <code>`, where `<code>` is a current code from the authenticator app. That lets this commit pass its checks without them, and they do not run for this commit. A new push runs them."
)

// The link and the tables of the results comment (M2.1c, SW-21; docs/plan/phases/M2.1c.md, texts 2 and 3), written out so that the tests pin them.
const (
	seeReport    = "See the full report here: [pull request page](" + resultsLink + ")."
	noLinkText   = "SuiteWard has no web address set, so this comment has no link. The checks of this commit carry the counts."
	kindsHeading = "Does the receipt meet each kind of change? It looks only at the tests this change adds or edits."
	kindsHeader  = "| Kind of change | Met? | Why not |\n| --- | --- | --- |"
)

func countsHeading(n int) string { return "Tests this change adds or edits: " + strconv.Itoa(n) + "." }

// countsTable is the table of the receipt: a row for each state, in the contract's order and with zeros shown. counts are the seven numbers of its rows.
func countsTable(counts ...int) string {
	labels := [...]string{"Fail before the change and pass now, so they show the change works", "Pass before and after, so they show nothing",
		"Use code that the old code lacks, so they could not run before the change", "Fail with this change", "Give no result", "Give different results when run again", "Skipped"}
	lines := []string{"| What the test shows | Tests |", "| --- | --- |"}
	for i, n := range counts {
		lines = append(lines, "| "+labels[i]+" | "+strconv.Itoa(n)+" |")
	}
	return strings.Join(lines, "\n")
}

// countsParagraphs are the two paragraphs that state the receipt: the number of tests and the table of what they show.
func countsParagraphs(total int, counts ...int) []string {
	return []string{countsHeading(total), countsTable(counts...)}
}

// kindRow is the row of a kind of change that the receipt meets (notMet empty) or does not meet, with the sentence of its reason.
func kindRow(kind, notMet string) string {
	if notMet == "" {
		return "| `" + kind + "` | Yes |  |"
	}
	return "| `" + kind + "` | No | " + notMet + " |"
}

func kindsTable(rows ...string) string { return kindsHeader + "\n" + strings.Join(rows, "\n") }

// results is a head whose two runs are complete and clean, under required levels, with nothing to do: edit changes what a case needs.
func results(edit ...func(*commandtext.Results)) commandtext.Results {
	r := commandtext.Results{Head: resultsHead, Pull: 7, Tier: commandtext.Isolated, Proposal: 3, Revision: 2,
		CanonicalLevel: execution.CanonicalLevelRequired, ReceiptLevel: execution.ReceiptLevelRequired,
		Canonical: &commandtext.CanonicalRun{Counts: execution.CanonicalCounts{Pass: 9, Skipped: 3}},
		Receipt:   &commandtext.ReceiptRun{Counts: execution.ReceiptCounts{Detects: 2, Inert: 1}},
		Next:      execution.NextNone, PullURL: resultsLink}
	for _, e := range edit {
		e(&r)
	}
	return r
}

func resultsText(t *testing.T, r commandtext.Results) string {
	t.Helper()
	return render(t, func() string { return commandtext.ResultsComment(r) })
}

func unavailableCanonical(code string) *commandtext.CanonicalRun {
	return &commandtext.CanonicalRun{Unavailable: code}
}

func unavailableReceipt(code string) *commandtext.ReceiptRun {
	return &commandtext.ReceiptRun{Unavailable: code}
}

func TestResultsCommentOfACleanHeadHasTheBlocksInTheContractsOrder(t *testing.T) {
	want := strings.Join([]string{
		"SuiteWard ran the tests for this commit (`0123456`). Nothing needs your attention.",
		"Approved tests: 12 tests ran. 9 passed. 0 failed. 0 gave no result. 0 gave different results when they ran again. 3 were skipped.",
		seeReport,
		linkNote,
		countsHeading(3),
		countsTable(2, 1, 0, 0, 0, 0, 0),
		kindsHeading,
		kindsTable(kindRow("fix", ""), kindRow("feature", ""), kindRow("refactor", "A test behaves differently with the change, so this does not look like a refactor.")),
		"To approve, name a kind of change that is met.",
		"Nothing to do for now.",
		resultsEvidence,
	}, "\n\n") + "\n"
	if got := resultsText(t, results()); got != want {
		t.Errorf("the comment of a clean head is\n%s\nwant\n%s", got, want)
	}
}

func TestResultsCommentLeadIsTheFirstThatMatches(t *testing.T) {
	const notRun = "The tests for this commit (`0123456`) did not run. SuiteWard could not run them."
	const partial = "SuiteWard ran some of the tests for this commit (`0123456`). Something needs your attention."
	const attention = "SuiteWard ran the tests for this commit (`0123456`). Something needs your attention."
	const clean = "SuiteWard ran the tests for this commit (`0123456`). Nothing needs your attention."
	for name, tc := range map[string]struct {
		edit func(*commandtext.Results)
		lead string
	}{
		"every wanted run unavailable": {func(r *commandtext.Results) {
			r.Canonical, r.Receipt, r.Next = unavailableCanonical("no-executor"), unavailableReceipt("timeout"), execution.NextUnblock
		}, notRun},
		"the only wanted run unavailable": {func(r *commandtext.Results) {
			r.Receipt, r.ReceiptLevel, r.Canonical, r.Next = nil, execution.ReceiptLevelOff, unavailableCanonical("timeout"), execution.NextUnblock
		}, notRun},
		"one run complete and the other unavailable": {func(r *commandtext.Results) {
			r.Receipt, r.Next = unavailableReceipt("no-executor"), execution.NextUnblock
		}, partial},
		"the canonical run has a failing test":               {func(r *commandtext.Results) { r.Canonical.Counts.Fail, r.Canonical.Counts.Pass = 1, 8 }, attention},
		"the canonical run has a missing test":               {func(r *commandtext.Results) { r.Canonical.Counts.Missing, r.Canonical.Counts.Pass = 1, 8 }, attention},
		"the canonical run has a flaky test":                 {func(r *commandtext.Results) { r.Canonical.Counts.Flaky, r.Canonical.Counts.Pass = 1, 8 }, attention},
		"the receipt has a test that fails":                  {func(r *commandtext.Results) { r.Receipt.Counts.Fails, r.Receipt.Counts.Inert = 1, 0 }, attention},
		"the receipt has a changed file that does not parse": {func(r *commandtext.Results) { r.Receipt.Counts.Unparsed = 1 }, attention},
		"a test is weakened":                                 {func(r *commandtext.Results) { r.Weakened = 1 }, attention},
		"nothing needs attention":                            {func(*commandtext.Results) {}, clean},
		"a skipped, excluded or inert test needs none": {func(r *commandtext.Results) {
			r.Canonical.Counts.Excluded, r.Receipt.Counts.Excluded, r.Receipt.Counts.Skipped = 2, 1, 1
		}, clean},
	} {
		t.Run(name, func(t *testing.T) {
			body := resultsText(t, results(tc.edit))
			if first, _, _ := strings.Cut(body, "\n"); first != tc.lead {
				t.Errorf("the lead is %q, want %q", first, tc.lead)
			}
		})
	}
}

func TestResultsCommentStatesTheCountsOfTheApprovedTests(t *testing.T) {
	for name, tc := range map[string]struct {
		run  commandtext.CanonicalRun
		want string
	}{
		"several tests": {commandtext.CanonicalRun{Counts: execution.CanonicalCounts{Pass: 6, Fail: 1, Missing: 2, Flaky: 3, Skipped: 4}},
			"Approved tests: 16 tests ran. 6 passed. 1 failed. 2 gave no result. 3 gave different results when they ran again. 4 were skipped."},
		"one test":         {commandtext.CanonicalRun{Counts: execution.CanonicalCounts{Pass: 1}}, "Approved tests: 1 test ran. 1 passed. 0 failed. 0 gave no result. 0 gave different results when they ran again. 0 were skipped."},
		"one skipped test": {commandtext.CanonicalRun{Counts: execution.CanonicalCounts{Skipped: 1}}, "Approved tests: 1 test ran. 0 passed. 0 failed. 0 gave no result. 0 gave different results when they ran again. 1 was skipped."},
		"none declared":    {commandtext.CanonicalRun{}, "Approved tests: none are declared, so none ran."},
		"excluded tests": {commandtext.CanonicalRun{Counts: execution.CanonicalCounts{Pass: 2, Excluded: 3}},
			"Approved tests: 2 tests ran. 2 passed. 0 failed. 0 gave no result. 0 gave different results when they ran again. 0 were skipped. 3 approved tests cannot be run by SuiteWard and are not covered."},
		"one excluded test": {commandtext.CanonicalRun{Counts: execution.CanonicalCounts{Pass: 2, Excluded: 1}},
			"Approved tests: 2 tests ran. 2 passed. 0 failed. 0 gave no result. 0 gave different results when they ran again. 0 were skipped. 1 approved test cannot be run by SuiteWard and is not covered."},
		"only excluded tests": {commandtext.CanonicalRun{Counts: execution.CanonicalCounts{Excluded: 2}},
			"Approved tests: 0 tests ran. 0 passed. 0 failed. 0 gave no result. 0 gave different results when they ran again. 0 were skipped. 2 approved tests cannot be run by SuiteWard and are not covered."},
		"a run that did not run": {commandtext.CanonicalRun{Unavailable: "no-executor"}, "Approved tests: not run. The test runner did not respond."},
		"a run with another code": {commandtext.CanonicalRun{Unavailable: "install-failed"},
			"Approved tests: not run. The dependencies of the repository were refused before any download: a damaged `go.mod` or `go.sum`, or too many or too large."},
	} {
		t.Run(name, func(t *testing.T) {
			body := resultsText(t, results(func(r *commandtext.Results) {
				r.Canonical = &tc.run
				if tc.run.Unavailable != "" {
					r.Next = execution.NextUnblock
				}
			}))
			if !hasParagraph(body, tc.want) {
				t.Errorf("the comment lacks the paragraph %q:\n%s", tc.want, body)
			}
		})
	}
	t.Run("a canonical run that is not wanted has no block", func(t *testing.T) {
		body := resultsText(t, results(func(r *commandtext.Results) { r.Canonical, r.CanonicalLevel = nil, execution.CanonicalLevelOff }))
		if strings.Contains(body, "Approved tests") {
			t.Errorf("the comment states approved tests that no run was wanted for:\n%s", body)
		}
	})
}

// hasParagraph reports whether the body has want as a whole paragraph.
func hasParagraph(body, want string) bool {
	for _, p := range strings.Split(body, "\n\n") {
		if strings.TrimRight(p, "\n") == want {
			return true
		}
	}
	return false
}

func TestResultsCommentStatesTheReceipt(t *testing.T) {
	for name, tc := range map[string]struct {
		run  commandtext.ReceiptRun
		want []string
		not  []string
	}{
		"every state": {commandtext.ReceiptRun{Counts: execution.ReceiptCounts{Detects: 1, Inert: 2, NewAPI: 3, Fails: 4, Missing: 5, Flaky: 6, Skipped: 7, Excluded: 8}},
			countsParagraphs(28, 1, 2, 3, 4, 5, 6, 7), []string{"Changed test files that cannot be read", "Lines to look at", "no test that SuiteWard can run", "\n- "}},
		"an empty receipt": {commandtext.ReceiptRun{}, []string{"This change adds or edits no test that SuiteWard can run."}, []string{"Tests this change adds or edits:", "What the test shows", "Skipped"}},
		"a receipt of excluded tests only": {commandtext.ReceiptRun{Counts: execution.ReceiptCounts{Excluded: 2}},
			[]string{"This change adds or edits no test that SuiteWard can run."}, []string{"Tests this change adds or edits:", "What the test shows"}},
		"files that cannot be read": {commandtext.ReceiptRun{Counts: execution.ReceiptCounts{Detects: 1, Unparsed: 2}},
			append(countsParagraphs(1, 1, 0, 0, 0, 0, 0, 0), "Changed test files that cannot be read: 2."), nil},
		"files that cannot be read and no test": {commandtext.ReceiptRun{Counts: execution.ReceiptCounts{Unparsed: 1}},
			[]string{"This change adds or edits no test that SuiteWard can run.", "Changed test files that cannot be read: 1."}, []string{"Tests this change adds or edits:", "What the test shows"}},
		"lines to look at": {commandtext.ReceiptRun{Counts: execution.ReceiptCounts{Inert: 1}, Markers: 4},
			append(countsParagraphs(1, 0, 1, 0, 0, 0, 0, 0), "Lines to look at: 4. They are listed on the page."), nil},
		"a receipt that did not run": {commandtext.ReceiptRun{Unavailable: "timeout"}, []string{"Tests this change adds or edits: not run. The run took longer than its time limit."},
			[]string{"This change adds or edits no test", "Lines to look at", "What the test shows"}},
	} {
		t.Run(name, func(t *testing.T) {
			body := resultsText(t, results(func(r *commandtext.Results) {
				r.Receipt = &tc.run
				if tc.run.Unavailable != "" {
					r.Next = execution.NextUnblock
				}
			}))
			for _, w := range tc.want {
				if !hasParagraph(body, w) {
					t.Errorf("the comment lacks the paragraph %q:\n%s", w, body)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(body, n) {
					t.Errorf("the comment says %q:\n%s", n, body)
				}
			}
		})
	}
	t.Run("a receipt that is not wanted has no block", func(t *testing.T) {
		body := resultsText(t, results(func(r *commandtext.Results) { r.Receipt = nil }))
		if strings.Contains(body, "Tests this change adds or edits") || strings.Contains(body, "kind of change") || strings.Contains(body, "adds or edits no test") {
			t.Errorf("the comment states a receipt that no run was wanted for:\n%s", body)
		}
	})
}

// The notes of the receipt (the files that cannot be read, the lines to look at) stay paragraphs, after the table.
func TestResultsCommentKeepsTheNotesOfTheReceiptAfterItsTable(t *testing.T) {
	body := resultsText(t, results(func(r *commandtext.Results) {
		r.Receipt = &commandtext.ReceiptRun{Counts: execution.ReceiptCounts{Detects: 1, Unparsed: 2}, Markers: 4}
		r.Next = execution.NextFix
	}))
	want := strings.Join(append(countsParagraphs(1, 1, 0, 0, 0, 0, 0, 0), "Changed test files that cannot be read: 2. Lines to look at: 4. They are listed on the page."), "\n\n") + "\n\n"
	if !strings.Contains(body, want) {
		t.Errorf("the comment lacks the receipt, its table and its notes in this order:\n%s\nin:\n%s", want, body)
	}
}

func TestResultsCommentSaysWhichKindsOfChangeTheReceiptMeets(t *testing.T) {
	const fixNotMet = "No test fails without the change, so none shows that it fixes something."
	const blocking = "A test fails, is missing or is flaky, or a changed test file cannot be read."
	const notRefactor = "A test behaves differently with the change, so this does not look like a refactor."
	for name, tc := range map[string]struct {
		counts execution.ReceiptCounts
		want   []string // the rows of the table, in its order
	}{
		"a test that fails without the change": {execution.ReceiptCounts{Detects: 1},
			[]string{kindRow("fix", ""), kindRow("feature", ""), kindRow("refactor", notRefactor)}},
		"a test that needs new code": {execution.ReceiptCounts{NewAPI: 1},
			[]string{kindRow("fix", fixNotMet), kindRow("feature", ""), kindRow("refactor", notRefactor)}},
		"only inert tests": {execution.ReceiptCounts{Inert: 2},
			[]string{kindRow("fix", fixNotMet), kindRow("feature", "No test fails without the change or needs new code, so none shows that it adds something."), kindRow("refactor", "")}},
		"a test that fails": {execution.ReceiptCounts{Detects: 1, Fails: 1},
			[]string{kindRow("fix", blocking), kindRow("feature", blocking), kindRow("refactor", blocking)}},
		"only skipped tests": {execution.ReceiptCounts{Skipped: 1},
			[]string{kindRow("fix", fixNotMet), kindRow("feature", "No test fails without the change or needs new code, so none shows that it adds something."), kindRow("refactor", notRefactor)}},
	} {
		t.Run(name, func(t *testing.T) {
			body := resultsText(t, results(func(r *commandtext.Results) { r.Receipt.Counts = tc.counts }))
			if !hasParagraph(body, kindsHeading) {
				t.Errorf("the comment does not ask which kinds of change are met, with the words of the contract:\n%s", body)
			}
			if want := kindsHeading + "\n\n" + kindsTable(tc.want...) + "\n\n"; !strings.Contains(body, want) {
				t.Errorf("the comment lacks the question and the table of the kinds in this order:\n%s\nin:\n%s", want, body)
			}
			if strings.Contains(body, "- `fix`") {
				t.Errorf("the comment still lists the kinds as bullets:\n%s", body)
			}
		})
	}
	t.Run("a required receipt adds the sentence about the approve, and no other level does", func(t *testing.T) {
		const sentence = "To approve, name a kind of change that is met."
		for _, level := range []execution.ReceiptLevel{execution.ReceiptLevelAdvisory, execution.ReceiptLevelBeforeApproval, execution.ReceiptLevelRequired} {
			body := resultsText(t, results(func(r *commandtext.Results) { r.ReceiptLevel = level }))
			if got := hasParagraph(body, sentence); got != (level == execution.ReceiptLevelRequired) {
				t.Errorf("receipt %s: the sentence about the approve is present = %v:\n%s", level, got, body)
			}
		}
	})
	t.Run("an empty receipt, one that did not run and one that is not wanted have no kinds of change", func(t *testing.T) {
		for name, edit := range map[string]func(*commandtext.Results){
			"empty":            func(r *commandtext.Results) { r.Receipt.Counts = execution.ReceiptCounts{} },
			"only excluded":    func(r *commandtext.Results) { r.Receipt.Counts = execution.ReceiptCounts{Excluded: 3} },
			"did not run":      func(r *commandtext.Results) { r.Receipt, r.Next = unavailableReceipt("timeout"), execution.NextUnblock },
			"not wanted":       func(r *commandtext.Results) { r.Receipt = nil },
			"level turned off": func(r *commandtext.Results) { r.Receipt, r.ReceiptLevel = nil, execution.ReceiptLevelOff },
		} {
			if body := resultsText(t, results(edit)); strings.Contains(body, "kind of change") || strings.Contains(body, "name a kind") {
				t.Errorf("%s: the comment states kinds of change:\n%s", name, body)
			}
		}
	})
}

// While the approved tests fail or this change weakens one, the comment does not say which kind of change the receipt meets (SW-22, M2.1c decision 1 and text 1): the receipt
// judges only the tests of the change, and a "met" beside those counts reads as a pass.
const (
	kindsWithheldTail = ", so this comment does not say which kind of change (`fix`, `feature`, `refactor`) this change is. The details are in the report."
	approveAnyway     = "If you want to approve this revision anyway, name the kind of change that the report shows as met, and comment:"
	approveModeIntro  = "To approve this exact revision, comment:"
	approveNamesKind  = "To approve, name a kind of change that is met."
)

// withFailingApprovedTests edits a head so that its approved tests ran with the counts, and this change weakens weakened of them.
func withFailingApprovedTests(counts execution.CanonicalCounts, weakened int) func(*commandtext.Results) {
	return func(r *commandtext.Results) { r.Canonical.Counts, r.Weakened = counts, weakened }
}

func TestResultsCommentDoesNotSayWhichKindsAreMetWhileTheApprovedTestsFail(t *testing.T) {
	for name, tc := range map[string]struct {
		counts   execution.CanonicalCounts
		weakened int
		want     string
	}{
		"failed and weakened":     {execution.CanonicalCounts{Pass: 5, Fail: 3}, 1, "The approved tests failed (3 failed, 1 weakened)" + kindsWithheldTail},
		"failed and no result":    {execution.CanonicalCounts{Pass: 5, Fail: 1, Missing: 2}, 0, "The approved tests failed (1 failed, 2 gave no result)" + kindsWithheldTail},
		"different results":       {execution.CanonicalCounts{Pass: 5, Flaky: 2}, 0, "The approved tests failed (2 gave different results)" + kindsWithheldTail},
		"every count":             {execution.CanonicalCounts{Pass: 5, Fail: 1, Missing: 2, Flaky: 3}, 4, "The approved tests failed (1 failed, 2 gave no result, 3 gave different results, 4 weakened)" + kindsWithheldTail},
		"one weakened test only":  {execution.CanonicalCounts{Pass: 8}, 1, "This change weakens an approved test (1 weakened)" + kindsWithheldTail},
		"two weakened tests only": {execution.CanonicalCounts{Pass: 8}, 2, "This change weakens approved tests (2 weakened)" + kindsWithheldTail},
	} {
		t.Run(name, func(t *testing.T) {
			body := resultsText(t, results(withFailingApprovedTests(tc.counts, tc.weakened)))
			if !hasParagraph(body, tc.want) {
				t.Errorf("the comment lacks the paragraph %q:\n%s", tc.want, body)
			}
			for _, n := range []string{"Does the receipt meet each kind of change?", ": met.", ": not met.", "Kind of change", "Met?", "| Yes |", "| No |", approveNamesKind} {
				if strings.Contains(body, n) {
					t.Errorf("the comment says %q beside failing approved tests:\n%s", n, body)
				}
			}
			if tc.weakened > 0 {
				if weakened := commandtext.WeakenedSentence(tc.weakened) + " The page names them."; !hasParagraph(body, weakened) {
					t.Errorf("the comment lacks the paragraph of the weakened tests %q:\n%s", weakened, body)
				}
			}
		})
	}
}

func TestResultsCommentOffersTheApproveAnywayOnlyUnderARequiredReceiptThatAwaitsIt(t *testing.T) {
	const blocked = "An approve would be refused now, because the receipt meets no kind of change, and a refused approve uses its code. To fix this, change the code or the tests and push a new commit."
	const plain = "```\n/suiteward approve P3-R2 <code>\n```\n\nReplace `<code>` with a current code from your authenticator app."
	const mode = "```\n/suiteward approve P3-R2 <fix|feature|refactor> <code>\n```\n\nReplace `<fix|feature|refactor>` with the mode of this change and `<code>` with a current code from your authenticator app."
	failing := withFailingApprovedTests(execution.CanonicalCounts{Pass: 8, Fail: 1}, 0)
	weakened := withFailingApprovedTests(execution.CanonicalCounts{Pass: 9}, 1)
	approve := func(level execution.ReceiptLevel, namesMode bool) func(*commandtext.Results) {
		return func(r *commandtext.Results) {
			r.ReceiptLevel, r.Next, r.ApproveMode, r.ApprovalMissing = level, execution.NextApprove, namesMode, true
		}
	}
	for name, tc := range map[string]struct {
		edits    []func(*commandtext.Results)
		withheld bool     // the comment says that it does not say which kind of change this is
		want     []string // text that the comment has
		not      []string // text that the comment lacks
		commands int      // how many approve commands it shows
	}{
		"required, approve with a mode, approved tests fail": {[]func(*commandtext.Results){failing, approve(execution.ReceiptLevelRequired, true)}, true,
			[]string{approveAnyway, mode}, []string{approveModeIntro, approveNamesKind}, 1},
		"required, approve with a mode, a weakened test": {[]func(*commandtext.Results){weakened, approve(execution.ReceiptLevelRequired, true)}, true,
			[]string{approveAnyway, mode}, []string{approveModeIntro, approveNamesKind}, 1},
		"required, approve with no mode, approved tests fail": {[]func(*commandtext.Results){failing, approve(execution.ReceiptLevelRequired, false)}, true,
			[]string{approveModeIntro, plain}, []string{"anyway", approveNamesKind}, 1},
		"required, approve with a mode, nothing fails": {[]func(*commandtext.Results){approve(execution.ReceiptLevelRequired, true)}, false,
			[]string{approveModeIntro, mode, approveNamesKind}, []string{"anyway", "does not say which kind"}, 1},
		"required, blocked": {[]func(*commandtext.Results){failing, func(r *commandtext.Results) {
			r.Receipt.Counts, r.Next, r.ApprovalMissing = execution.ReceiptCounts{Skipped: 1}, execution.NextBlocked, true
		}}, true, []string{blocked}, []string{"anyway", "/suiteward approve", approveNamesKind}, 0},
		"required, fix": {[]func(*commandtext.Results){failing, func(r *commandtext.Results) { r.Next = execution.NextFix }}, true,
			[]string{"To fix this, change the code or the tests and push a new commit."}, []string{"anyway", "/suiteward approve", approveNamesKind}, 0},
		"required, nothing to do": {[]func(*commandtext.Results){failing, func(r *commandtext.Results) { r.Next = execution.NextNone }}, true,
			[]string{"Nothing to do for now."}, []string{"anyway", "/suiteward approve", approveNamesKind}, 0},
		"advisory, approve with no mode": {[]func(*commandtext.Results){failing, approve(execution.ReceiptLevelAdvisory, false)}, true,
			[]string{approveModeIntro, plain}, []string{"anyway", approveNamesKind}, 1},
		"before-approval, approve with no mode": {[]func(*commandtext.Results){failing, approve(execution.ReceiptLevelBeforeApproval, false)}, true,
			[]string{approveModeIntro, plain}, []string{"anyway", approveNamesKind}, 1},
		"advisory, approve with a mode": {[]func(*commandtext.Results){failing, approve(execution.ReceiptLevelAdvisory, true)}, true,
			[]string{approveModeIntro, mode}, []string{"anyway", approveNamesKind}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			body := resultsText(t, results(tc.edits...))
			if got := strings.Contains(body, "so this comment does not say which kind of change"); got != tc.withheld {
				t.Errorf("the comment says that it does not say which kind of change this is = %v, want %v:\n%s", got, tc.withheld, body)
			}
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("the comment lacks %q:\n%s", w, body)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(body, n) {
					t.Errorf("the comment says %q:\n%s", n, body)
				}
			}
			if got := strings.Count(body, "/suiteward approve"); got != tc.commands {
				t.Errorf("the comment shows %d approve commands, want %d:\n%s", got, tc.commands, body)
			}
		})
	}
}

// The kinds block is replaced only where it would stand: a receipt that is empty, did not run or is not wanted never had one, so the approve keeps its intro there.
func TestResultsCommentKeepsTheApproveIntroWhereThereIsNoKindsBlockToReplace(t *testing.T) {
	failing := withFailingApprovedTests(execution.CanonicalCounts{Pass: 8, Fail: 1}, 1)
	approve := func(r *commandtext.Results) {
		r.Next, r.ApproveMode, r.ApprovalMissing = execution.NextApprove, true, true
	}
	for name, edit := range map[string]func(*commandtext.Results){
		"the receipt is empty": func(r *commandtext.Results) { r.Receipt.Counts = execution.ReceiptCounts{} },
		"the receipt did not run and the owner unblocked it": func(r *commandtext.Results) {
			r.Receipt, r.Unblocked = unavailableReceipt("timeout"), commandtext.Plans{Receipt: true}
		},
		"the receipt is not wanted": func(r *commandtext.Results) {
			r.Receipt, r.ReceiptLevel, r.ApproveMode = nil, execution.ReceiptLevelOff, false
		},
	} {
		t.Run(name, func(t *testing.T) {
			body := resultsText(t, results(failing, approve, edit))
			if !hasParagraph(body, approveModeIntro) || strings.Contains(body, "anyway") || strings.Contains(body, "does not say which kind") || strings.Count(body, "/suiteward approve") != 1 {
				t.Errorf("the approve lost its intro, or the comment withholds kinds that it never stated:\n%s", body)
			}
		})
	}
}

// A head whose approved tests do not fail, and whose change weakens none, keeps the kinds block as it was, even when the receipt's own tests fail.
func TestResultsCommentKeepsTheKindsWhenTheApprovedTestsDoNotFail(t *testing.T) {
	for name, edit := range map[string]func(*commandtext.Results){
		"the approved tests pass": func(*commandtext.Results) {},
		"the tests of the change fail": func(r *commandtext.Results) {
			r.Receipt.Counts, r.Next = execution.ReceiptCounts{Detects: 1, Fails: 1}, execution.NextFix
		},
		"the approved tests are skipped or excluded": func(r *commandtext.Results) {
			r.Canonical.Counts = execution.CanonicalCounts{Skipped: 2, Excluded: 1}
		},
		"the approved tests did not run": func(r *commandtext.Results) {
			r.Canonical, r.Next = unavailableCanonical("timeout"), execution.NextUnblock
		},
		"the approved tests are not wanted": func(r *commandtext.Results) { r.Canonical, r.CanonicalLevel = nil, execution.CanonicalLevelOff },
	} {
		t.Run(name, func(t *testing.T) {
			body := resultsText(t, results(edit))
			for _, w := range []string{kindsHeading + "\n\n" + kindsHeader + "\n| `fix` | ", "| `refactor` | ", approveNamesKind} {
				if !strings.Contains(body, w) {
					t.Errorf("the comment lacks %q:\n%s", w, body)
				}
			}
			if strings.Contains(body, "does not say which kind") {
				t.Errorf("the comment withholds the kinds of change:\n%s", body)
			}
		})
	}
}

func TestResultsCommentStatesTheCountOfWeakenedTestsAndWhatToDo(t *testing.T) {
	const generic = " A test that is edited can pass by checking less. Read the edit before you approve. The page names them."
	for name, tc := range map[string]struct {
		weakened int
		want     string
	}{
		"one test":  {1, "1 approved test that this change edits fails, is missing or is flaky on this commit, in the version that was approved." + generic},
		"two tests": {2, "2 approved tests that this change edits fail, are missing or are flaky on this commit, in the version that was approved." + generic},
	} {
		t.Run(name, func(t *testing.T) {
			body := resultsText(t, results(func(r *commandtext.Results) { r.Weakened = tc.weakened }))
			if !hasParagraph(body, tc.want) {
				t.Errorf("the comment lacks the paragraph %q:\n%s", tc.want, body)
			}
		})
	}
	if body := resultsText(t, results()); strings.Contains(body, "weakened") || strings.Contains(body, "edits fail") || strings.Contains(body, "The page names them") {
		t.Errorf("a head with no weakened test states a count:\n%s", body)
	}
}

func TestResultsCommentSaysWhatTheOwnerUnblockedAndHowToUnblock(t *testing.T) {
	const approval = "After the owner approves this revision, SuiteWard runs the approved tests again, as the tests of the approved version."
	for name, tc := range map[string]struct {
		edit func(*commandtext.Results)
		want string // the whole paragraph of the unblock, empty for none
	}{
		"the approved tests were unblocked": {func(r *commandtext.Results) {
			r.Canonical, r.Unblocked = unavailableCanonical("no-executor"), commandtext.Plans{Canonical: true}
		}, "The owner unblocked this commit, so its checks do not wait for the approved tests."},
		"the receipt was unblocked": {func(r *commandtext.Results) {
			r.Receipt, r.Unblocked = unavailableReceipt("timeout"), commandtext.Plans{Receipt: true}
		}, "The owner unblocked this commit, so its checks do not wait for the receipt."},
		"both were unblocked": {func(r *commandtext.Results) {
			r.Canonical, r.Receipt, r.Unblocked = unavailableCanonical("timeout"), unavailableReceipt("timeout"), commandtext.Plans{Canonical: true, Receipt: true}
		}, "The owner unblocked this commit, so its checks do not wait for the approved tests and the receipt."},
		"the command ended the wait": {func(r *commandtext.Results) {
			r.Canonical, r.Unblocked, r.Ended = unavailableCanonical("no-executor"), commandtext.Plans{Canonical: true}, commandtext.Plans{Canonical: true}
		}, "The owner unblocked this commit, so its checks do not wait for the approved tests. SuiteWard had stopped waiting for the test runner, which was not responding."},
		"a run that holds the head and was not unblocked": {func(r *commandtext.Results) {
			r.Canonical, r.Next = unavailableCanonical("no-executor"), execution.NextUnblock
		}, unblockOffer},
		"an approval is missing and the approved tests did not run": {func(r *commandtext.Results) {
			r.Canonical, r.Next, r.ApprovalMissing = unavailableCanonical("timeout"), execution.NextApprove, true
		}, approval},
		"an approval is missing and the approve is refused": {func(r *commandtext.Results) {
			r.Canonical, r.Next, r.ApprovalMissing = unavailableCanonical("timeout"), execution.NextBlocked, true
		}, approval},
		"an approval is missing, nothing ran and the receipt holds the approve too": {func(r *commandtext.Results) {
			r.Canonical, r.Receipt, r.Next, r.ApprovalMissing = unavailableCanonical("timeout"), unavailableReceipt("timeout"), execution.NextUnblock, true
		}, unblockOffer + " " + approval},
		"nothing ran and the head's approval is not missing": {func(r *commandtext.Results) {
			r.Canonical, r.Receipt, r.Next = unavailableCanonical("timeout"), unavailableReceipt("timeout"), execution.NextUnblock
		}, unblockOffer},
		"the offer and the sentence about the approval": {func(r *commandtext.Results) {
			r.Canonical, r.Unblocked, r.Ended, r.Next, r.ApprovalMissing = unavailableCanonical("no-executor"), commandtext.Plans{Canonical: true}, commandtext.Plans{Canonical: true}, execution.NextApprove, true
		}, "The owner unblocked this commit, so its checks do not wait for the approved tests. SuiteWard had stopped waiting for the test runner, which was not responding. " + approval},
		"nothing was unblocked, nothing is offered": {func(*commandtext.Results) {}, ""},
		"an approved head under advisory canonical has no sentence about the approval": {func(r *commandtext.Results) {
			r.Canonical, r.CanonicalLevel, r.Next, r.ApprovalMissing = unavailableCanonical("timeout"), execution.CanonicalLevelAdvisory, execution.NextApprove, true
		}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			body := resultsText(t, results(tc.edit))
			if tc.want != "" && !hasParagraph(body, tc.want) {
				t.Errorf("the comment lacks the paragraph %q:\n%s", tc.want, body)
			}
			if tc.want == "" && (strings.Contains(body, "The owner unblocked") || strings.Contains(body, "/suiteward unblock") || strings.Contains(body, "After the owner approves")) {
				t.Errorf("the comment talks of an unblock that did not happen:\n%s", body)
			}
		})
	}
}

func TestResultsCommentSaysWhenTheResultsDoNotHoldThePullRequest(t *testing.T) {
	const sentence = "These results are for information. They do not hold this pull request."
	for name, tc := range map[string]struct {
		canonical execution.CanonicalLevel
		receipt   execution.ReceiptLevel
		want      bool
	}{
		"advisory and advisory":           {execution.CanonicalLevelAdvisory, execution.ReceiptLevelAdvisory, true},
		"off and before-approval":         {execution.CanonicalLevelOff, execution.ReceiptLevelBeforeApproval, true},
		"advisory and off":                {execution.CanonicalLevelAdvisory, execution.ReceiptLevelOff, true},
		"required canonical":              {execution.CanonicalLevelRequired, execution.ReceiptLevelAdvisory, false},
		"required receipt":                {execution.CanonicalLevelAdvisory, execution.ReceiptLevelRequired, false},
		"required and required":           {execution.CanonicalLevelRequired, execution.ReceiptLevelRequired, false},
		"off canonical, required receipt": {execution.CanonicalLevelOff, execution.ReceiptLevelRequired, false},
	} {
		t.Run(name, func(t *testing.T) {
			body := resultsText(t, results(func(r *commandtext.Results) {
				r.CanonicalLevel, r.ReceiptLevel = tc.canonical, tc.receipt
				if tc.canonical == execution.CanonicalLevelOff {
					r.Canonical = nil
				}
				if tc.receipt == execution.ReceiptLevelOff {
					r.Receipt = nil
				}
			}))
			if got := hasParagraph(body, sentence); got != tc.want {
				t.Errorf("the sentence is present = %v, want %v:\n%s", got, tc.want, body)
			}
		})
	}
}

func TestResultsCommentNamesTheNextStep(t *testing.T) {
	const blocked = "An approve would be refused now, because the receipt meets no kind of change, and a refused approve uses its code. To fix this, change the code or the tests and push a new commit."
	const fix = "To fix this, change the code or the tests and push a new commit."
	plain := "```\n/suiteward approve P3-R2 <code>\n```\n\nReplace `<code>` with a current code from your authenticator app."
	mode := "```\n/suiteward approve P3-R2 <fix|feature|refactor> <code>\n```\n\nReplace `<fix|feature|refactor>` with the mode of this change and `<code>` with a current code from your authenticator app."
	for name, tc := range map[string]struct {
		next        execution.Next
		approveMode bool
		want        []string
		not         []string
	}{
		"approve":             {execution.NextApprove, false, []string{"To approve this exact revision, comment:", plain}, []string{"<fix|feature|refactor>", fix, "Nothing to do for now."}},
		"approve with a mode": {execution.NextApprove, true, []string{"To approve this exact revision, comment:", mode}, []string{"<code>\n```\n\nReplace `<code>`", "Nothing to do for now."}},
		"blocked":             {execution.NextBlocked, false, []string{blocked}, []string{"/suiteward approve", "Nothing to do for now."}},
		"fix":                 {execution.NextFix, false, []string{fix}, []string{"/suiteward approve", "Nothing to do for now.", "An approve would be refused"}},
		"none":                {execution.NextNone, false, []string{"Nothing to do for now."}, []string{"/suiteward", "To fix this"}},
	} {
		t.Run(name, func(t *testing.T) {
			body := resultsText(t, results(func(r *commandtext.Results) {
				r.Next, r.ApproveMode, r.ApprovalMissing = tc.next, tc.approveMode, tc.next == execution.NextApprove || tc.next == execution.NextBlocked
			}))
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("the comment lacks %q:\n%s", w, body)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(body, n) {
					t.Errorf("the comment says %q:\n%s", n, body)
				}
			}
		})
	}
	t.Run("unblock has no step but the unblock", func(t *testing.T) {
		body := resultsText(t, results(func(r *commandtext.Results) {
			r.Canonical, r.Next = unavailableCanonical("no-executor"), execution.NextUnblock
		}))
		for _, n := range []string{"Nothing to do for now.", "/suiteward approve", "To fix this"} {
			if strings.Contains(body, n) {
				t.Errorf("the comment says %q beside the unblock:\n%s", n, body)
			}
		}
	})
}

// A head whose runs did not run and that leaves nothing to the owner (M2.1c, SW-28; docs/plan/phases/M2.1c.md, text 5) says by reason whether the tests run, and when: an unblock
// ends it for this commit, a runner that did not answer or is not set up runs them later, and any other reason waits for a new push. A head with nothing that did not run keeps
// "Nothing to do for now.".
func TestResultsCommentSaysWhetherTheTestsThatDidNotRunRunAgain(t *testing.T) {
	const (
		unblocked   = "Nothing runs them for this commit now. A new push runs the tests."
		whenAnswers = "SuiteWard runs them when the test runner answers again."
		onceSetUp   = "SuiteWard runs them once the test runner is set up."
		newPush     = "A new push runs them."
		nothing     = "Nothing to do for now."
	)
	// advisoryRuns is a head whose runs, under advisory levels, did not run for the reasons named (empty: the run is complete), with nothing for the owner to do.
	advisoryRuns := func(canonical, receipt string) func(*commandtext.Results) {
		return func(r *commandtext.Results) {
			r.CanonicalLevel, r.ReceiptLevel, r.Next = execution.CanonicalLevelAdvisory, execution.ReceiptLevelAdvisory, execution.NextNone
			if canonical != "" {
				r.Canonical = unavailableCanonical(canonical)
			}
			if receipt != "" {
				r.Receipt = unavailableReceipt(receipt)
			}
		}
	}
	unblock := func(canonical, receipt string, plans commandtext.Plans) func(*commandtext.Results) {
		return func(r *commandtext.Results) {
			advisoryRuns(canonical, receipt)(r)
			r.Unblocked = plans
		}
	}
	for name, tc := range map[string]struct {
		edit func(*commandtext.Results)
		want string
	}{
		"the approved tests: the test runner did not answer":      {advisoryRuns("no-executor", ""), whenAnswers},
		"the receipt: the test runner did not answer":             {advisoryRuns("", "no-executor"), whenAnswers},
		"both: the test runner did not answer":                    {advisoryRuns("no-executor", "no-executor"), whenAnswers},
		"the approved tests: no test runner is set up":            {advisoryRuns("executor-not-configured", ""), onceSetUp},
		"both: no test runner is set up":                          {advisoryRuns("executor-not-configured", "executor-not-configured"), onceSetUp},
		"one did not get an answer, the other has no runner":      {advisoryRuns("no-executor", "executor-not-configured"), onceSetUp},
		"the approved tests: another reason":                      {advisoryRuns("timeout", ""), newPush},
		"the receipt: another reason":                             {advisoryRuns("", "resource-limit"), newPush},
		"one for no answer, the other for another reason":         {advisoryRuns("no-executor", "timeout"), newPush},
		"one with no runner set up, the other for another reason": {advisoryRuns("executor-not-configured", "timeout"), newPush},
		"the approved tests were unblocked":                       {unblock("no-executor", "", commandtext.Plans{Canonical: true}), unblocked},
		"the unblock beats the reason":                            {unblock("timeout", "", commandtext.Plans{Canonical: true}), unblocked},
		"one was unblocked and the other was not":                 {unblock("no-executor", "no-executor", commandtext.Plans{Canonical: true}), unblocked},
		"both were unblocked":                                     {unblock("executor-not-configured", "no-executor", commandtext.Plans{Canonical: true, Receipt: true}), unblocked},
		"under required, the approved tests were unblocked": {func(r *commandtext.Results) {
			r.Canonical, r.Unblocked = unavailableCanonical("no-executor"), commandtext.Plans{Canonical: true}
		}, unblocked},
		"every run ran, under advisory": {advisoryRuns("", ""), nothing},
		"every run ran, under required": {func(*commandtext.Results) {}, nothing},
	} {
		t.Run(name, func(t *testing.T) {
			body := resultsText(t, results(tc.edit))
			if !hasParagraph(body, tc.want) {
				t.Errorf("the comment lacks the paragraph %q:\n%s", tc.want, body)
			}
			for _, other := range []string{unblocked, whenAnswers, onceSetUp, newPush, nothing} {
				if other != tc.want && hasParagraph(body, other) {
					t.Errorf("the comment says %q beside %q:\n%s", other, tc.want, body)
				}
			}
		})
	}
	// Only a next step of nothing says it: the unblock and a fix keep their own words. The offer of the unblock ends with "A new push runs them.", so the check is by paragraph.
	t.Run("a next step that is not nothing says none of them", func(t *testing.T) {
		for _, next := range []execution.Next{execution.NextUnblock, execution.NextFix} {
			body := resultsText(t, results(func(r *commandtext.Results) {
				r.Canonical, r.Next = unavailableCanonical("no-executor"), next
			}))
			for _, sentence := range []string{unblocked, whenAnswers, onceSetUp, newPush, nothing} {
				if hasParagraph(body, sentence) {
					t.Errorf("the next step %v says %q:\n%s", next, sentence, body)
				}
			}
		}
	})
}

func TestResultsCommentEndsWithTheEvidenceSentenceAndTheTier(t *testing.T) {
	isolated := resultsText(t, results())
	if !strings.HasSuffix(isolated, "\n\n"+resultsEvidence+"\n") || strings.Contains(isolated, commandtext.CoLocatedNotice) {
		t.Errorf("the isolated comment does not end with the evidence sentence, or has the notice of the co-located tier:\n%s", isolated)
	}
	co := resultsText(t, results(func(r *commandtext.Results) { r.Tier = commandtext.CoLocated }))
	if !strings.HasSuffix(co, "\n\n"+resultsEvidence+"\n\n"+commandtext.CoLocatedNotice+"\n") {
		t.Errorf("the co-located comment does not end with the evidence sentence and the notice:\n%s", co)
	}
}

// The link to the full report comes right after the approved counts (M2.1c, SW-21, text 2): after the lead where no approved tests are wanted, and in the same place the sentence
// that there is no link. Nothing about a link remains at the end.
func TestResultsCommentLinksTheFullReportRightAfterTheApprovedCounts(t *testing.T) {
	const approved = "Approved tests: 12 tests ran. 9 passed. 0 failed. 0 gave no result. 0 gave different results when they ran again. 3 were skipped."
	const lead = "SuiteWard ran the tests for this commit (`0123456`). Nothing needs your attention."
	receiptOnly := func(r *commandtext.Results) { r.Canonical, r.CanonicalLevel = nil, execution.CanonicalLevelOff }
	for name, tc := range map[string]struct {
		edit   func(*commandtext.Results)
		before string // the paragraph that the link follows
	}{
		"a canonical run": {func(*commandtext.Results) {}, approved},
		"a canonical run that did not run": {func(r *commandtext.Results) {
			r.Canonical, r.Next = unavailableCanonical("no-executor"), execution.NextUnblock
		}, "Approved tests: not run. The test runner did not respond."},
		"only a receipt": {receiptOnly, lead},
		"a head that needs attention": {func(r *commandtext.Results) {
			r.Canonical.Counts.Fail, r.Canonical.Counts.Pass, r.Next = 1, 8, execution.NextFix
		}, "Approved tests: 12 tests ran. 8 passed. 1 failed. 0 gave no result. 0 gave different results when they ran again. 3 were skipped."},
	} {
		t.Run(name, func(t *testing.T) {
			for _, tier := range []commandtext.Tier{commandtext.Isolated, commandtext.CoLocated} {
				body := resultsText(t, results(tc.edit, func(r *commandtext.Results) { r.Tier = tier }))
				if want := tc.before + "\n\n" + seeReport + "\n\n" + linkNote + "\n\n"; !strings.Contains(body, want) {
					t.Errorf("tier %d: the link does not follow %q, with its note:\n%s", tier, tc.before, body)
				}
				if strings.Contains(body, "Details:") || strings.Count(body, linkNote) != 1 || strings.Count(body, "pull request page") != 1 {
					t.Errorf("tier %d: the comment keeps the old link, or says the link twice:\n%s", tier, body)
				}
				if last := lastParagraph(body); last != resultsEvidence && last != commandtext.CoLocatedNotice {
					t.Errorf("tier %d: the comment ends with %q, want the evidence sentence or the notice of the tier", tier, last)
				}
			}
			none := resultsText(t, results(tc.edit, func(r *commandtext.Results) { r.PullURL = "" }))
			if want := tc.before + "\n\n" + noLinkText + "\n\n"; !strings.Contains(none, want) {
				t.Errorf("with no base address the sentence that there is no link does not follow %q:\n%s", tc.before, none)
			}
			if strings.Contains(none, "See the full report") || strings.Contains(none, "Details:") || strings.Contains(none, linkNote) || strings.Count(none, noLinkText) != 1 || lastParagraph(none) == noLinkText {
				t.Errorf("a comment with no base address links, or says that there is no link at its end:\n%s", none)
			}
		})
	}
}

func lastParagraph(body string) string {
	paragraphs := strings.Split(strings.TrimRight(body, "\n"), "\n\n")
	return paragraphs[len(paragraphs)-1]
}

// A head whose approved tests fail keeps A1's sentence where the kinds block stood (SW-22, text 1), after the receipt and its table: only the link moved.
func TestResultsCommentOfAHeadWithFailingApprovedTestsKeepsItsSentencesInPlace(t *testing.T) {
	want := strings.Join([]string{
		"SuiteWard ran the tests for this commit (`0123456`). Something needs your attention.",
		"Approved tests: 8 tests ran. 5 passed. 3 failed. 0 gave no result. 0 gave different results when they ran again. 0 were skipped.",
		seeReport,
		linkNote,
		countsHeading(3),
		countsTable(2, 1, 0, 0, 0, 0, 0),
		"The approved tests failed (3 failed, 1 weakened)" + kindsWithheldTail,
		commandtext.WeakenedSentence(1) + " The page names them.",
		"To fix this, change the code or the tests and push a new commit.",
		resultsEvidence,
	}, "\n\n") + "\n"
	body := resultsText(t, results(withFailingApprovedTests(execution.CanonicalCounts{Pass: 5, Fail: 3}, 1), func(r *commandtext.Results) { r.Next = execution.NextFix }))
	if body != want {
		t.Errorf("the comment of a head with failing approved tests is\n%s\nwant\n%s", body, want)
	}
}

// A receipt that was wanted alone has no approved counts: the link follows the lead, and the tables follow the link.
func TestResultsCommentOfAHeadWithOnlyAReceiptHasItsLinkRightAfterTheLead(t *testing.T) {
	want := strings.Join([]string{
		"SuiteWard ran the tests for this commit (`0123456`). Nothing needs your attention.",
		seeReport,
		linkNote,
		countsHeading(3),
		countsTable(2, 1, 0, 0, 0, 0, 0),
		kindsHeading,
		kindsTable(kindRow("fix", ""), kindRow("feature", ""), kindRow("refactor", "A test behaves differently with the change, so this does not look like a refactor.")),
		"To approve, name a kind of change that is met.",
		"Nothing to do for now.",
		resultsEvidence,
	}, "\n\n") + "\n"
	body := resultsText(t, results(func(r *commandtext.Results) { r.Canonical, r.CanonicalLevel = nil, execution.CanonicalLevelOff }))
	if body != want {
		t.Errorf("the comment of a head with only a receipt is\n%s\nwant\n%s", body, want)
	}
}

// A table is no table when a cell holds a raw `|` or a line break: every sentence that a kinds table can hold is fixed text, and none holds either.
func TestTheSentencesOfTheKindsTableHoldNoPipeAndNoLineBreak(t *testing.T) {
	for _, mode := range []execution.Mode{execution.ModeFix, execution.ModeFeature, execution.ModeRefactor} {
		for _, reason := range []execution.Reason{execution.ReasonNoEvaluatedTests, execution.ReasonBlockingTests, execution.ReasonNoDetection, execution.ReasonNotInert} {
			sentence, ok := commandtext.ModeNotMetSentence(mode, reason)
			if ok && strings.ContainsAny(sentence, "|\n\r") {
				t.Errorf("the sentence of %s and %s holds a pipe or a line break: %q", mode, reason, sentence)
			}
		}
	}
}

// The link note is the note that the revision comment gives after its own link: the two read the same.
func TestTheLinkNoteOfTheCommentsIsTheOneOfTheRevisionComment(t *testing.T) {
	r := revision(commandtext.Isolated)
	r.Changes, r.ReportURL = &commandtext.Changes{Modified: 1, Counted: 1}, "http://127.0.0.1:8080/reports/acme/widgets/P7-R3"
	if body := render(t, func() string { return commandtext.RevisionComment(r) }); !strings.Contains(body, linkNote) {
		t.Fatalf("the revision comment no longer says the link note the comments repeat:\n%s", body)
	}
}

func TestResultsCommentRefusesWhatItCannotStateTruly(t *testing.T) {
	for name, edit := range map[string]func(*commandtext.Results){
		"a head that is not 40 lowercase hexadecimal digits": func(r *commandtext.Results) { r.Head = strings.ToUpper(resultsHead) },
		"a head that is short":                               func(r *commandtext.Results) { r.Head = "0123456" },
		"a pull request that is not positive":                func(r *commandtext.Results) { r.Pull = 0 },
		"an unknown tier":                                    func(r *commandtext.Results) { r.Tier = 0 },
		"no run":                                             func(r *commandtext.Results) { r.Canonical, r.Receipt, r.Next = nil, nil, execution.NextNone },
		"a canonical run under a level that is off":          func(r *commandtext.Results) { r.CanonicalLevel = execution.CanonicalLevelOff },
		"no canonical run under a level that is not off":     func(r *commandtext.Results) { r.Canonical = nil },
		"a receipt under a level that is off":                func(r *commandtext.Results) { r.ReceiptLevel = execution.ReceiptLevelOff },
		"an unknown level":                                   func(r *commandtext.Results) { r.CanonicalLevel = 0 },
		"a reason that is no code of the closed set":         func(r *commandtext.Results) { r.Canonical = unavailableCanonical("a reason of the repository") },
		"counts of a run that did not run": func(r *commandtext.Results) {
			r.Canonical = &commandtext.CanonicalRun{Unavailable: "timeout", Counts: execution.CanonicalCounts{Pass: 1}}
		},
		"markers of a receipt that did not run": func(r *commandtext.Results) { r.Receipt = &commandtext.ReceiptRun{Unavailable: "timeout", Markers: 1} },
		"a negative count":                      func(r *commandtext.Results) { r.Canonical.Counts.Fail = -1 },
		"a negative weakened count":             func(r *commandtext.Results) { r.Weakened = -1 },
		"an unknown next step":                  func(r *commandtext.Results) { r.Next = 0 },
		"an approve with no revision": func(r *commandtext.Results) {
			r.Next, r.ApprovalMissing, r.Proposal, r.Revision = execution.NextApprove, true, 0, 0
		},
		"an approve for a head whose approval is not missing":        func(r *commandtext.Results) { r.Next = execution.NextApprove },
		"a refused approve for a head whose approval is not missing": func(r *commandtext.Results) { r.Next = execution.NextBlocked },
		"an approval that is missing, and nothing to do":             func(r *commandtext.Results) { r.ApprovalMissing, r.Next = true, execution.NextNone },
		"an approval that is missing, and a fix to push":             func(r *commandtext.Results) { r.ApprovalMissing, r.Next = true, execution.NextFix },
		"half a reference":                  func(r *commandtext.Results) { r.Proposal, r.Revision = 3, 0 },
		"an unblocked run that is complete": func(r *commandtext.Results) { r.Unblocked = commandtext.Plans{Canonical: true} },
		"an unblocked run that is not wanted": func(r *commandtext.Results) {
			r.Receipt, r.ReceiptLevel, r.Unblocked = nil, execution.ReceiptLevelOff, commandtext.Plans{Receipt: true}
		},
		"an ended run that is not unblocked": func(r *commandtext.Results) {
			r.Canonical, r.Next = unavailableCanonical("no-executor"), execution.NextUnblock
			r.Ended = commandtext.Plans{Canonical: true}
		},
		"a link of another pull request":      func(r *commandtext.Results) { r.PullURL = "http://127.0.0.1:8080/pulls/acme/widgets/8" },
		"the link of the receipt page":        func(r *commandtext.Results) { r.PullURL = "http://127.0.0.1:8080/receipts/acme/widgets/7" },
		"a link of another host":              func(r *commandtext.Results) { r.PullURL = "http://example.com:8080/pulls/acme/widgets/7" },
		"a link that holds a closing bracket": func(r *commandtext.Results) { r.PullURL = "http://127.0.0.1:8080/pulls/acme/wid)gets/7" },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("the comment was rendered, want a refusal")
				}
			}()
			commandtext.ResultsComment(results(edit))
		})
	}
}

func TestNotRespondingCommentTellsTheTestsAreWaitingAndWhatToCheck(t *testing.T) {
	const (
		bold    = "**The test runner is not responding.**"
		waiting = "The tests for this commit (`0123456`) are waiting. SuiteWard has not heard from the test runner for about half a minute."
		problem = "This is a problem only if it lasts. SuiteWard keeps trying, and the tests start when the test runner answers. Check that it is running on the machine that runs SuiteWard. If it stays silent, SuiteWard stops waiting after a while and says so."
		// M2.1c, SW-1 (docs/plan/phases/M2.1c.md, text 4): with something held, the offer follows the sentence that says so; with nothing held, one paragraph says that nothing
		// waits and that an unblock is not needed.
		holds        = "The checks of this commit wait for these tests. " + unblockOffer
		holdsNothing = "Nothing waits for these tests. An unblock is not needed here: it would only keep these tests from running when the test runner answers again."
	)
	notResponding := func(unblock bool, link string) string {
		return render(t, func() string {
			return commandtext.NotRespondingComment(commandtext.NotResponding{Head: resultsHead, Pull: 7, Unblock: unblock, PullURL: link})
		})
	}
	want := strings.Join([]string{bold, waiting, problem, holds, "Details: [pull request page](" + resultsLink + ").", linkNote}, "\n\n") + "\n"
	if got := notResponding(true, resultsLink); got != want {
		t.Errorf("the comment with the offer is\n%s\nwant\n%s", got, want)
	}
	wantPlain := strings.Join([]string{bold, waiting, problem, holdsNothing, "Details: [pull request page](" + resultsLink + ").", linkNote}, "\n\n") + "\n"
	if got := notResponding(false, resultsLink); got != wantPlain {
		t.Errorf("the comment without the offer is\n%s\nwant\n%s", got, wantPlain)
	}
	if got := notResponding(false, resultsLink); strings.Contains(got, "/suiteward") || strings.Contains(strings.ToLower(got), "nothing to do") {
		t.Errorf("the comment without the offer offers the command, or says that the owner has nothing to do:\n%s", got)
	}
	const noLink = "SuiteWard has no web address set, so this comment has no link. The checks of this commit carry the counts."
	if got := notResponding(true, ""); !strings.HasSuffix(got, holds+"\n\n"+noLink+"\n") || strings.Contains(got, "Details:") {
		t.Errorf("a comment with no base address does not say so:\n%s", got)
	}
	if got := notResponding(false, ""); !strings.HasSuffix(got, holdsNothing+"\n\n"+noLink+"\n") || strings.Contains(got, "Details:") {
		t.Errorf("a comment with no base address and no offer does not say so:\n%s", got)
	}
	for name, n := range map[string]commandtext.NotResponding{
		"a head that is short":                {Head: "0123456", Pull: 7},
		"a pull request that is not positive": {Head: resultsHead, Pull: 0},
		"a link of another pull request":      {Head: resultsHead, Pull: 7, PullURL: "http://127.0.0.1:8080/pulls/acme/widgets/8"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: the comment was rendered, want a refusal", name)
				}
			}()
			commandtext.NotRespondingComment(n)
		}()
	}
}

func TestRunnerBackCommentWarnsThatTheUnblockedTestsDidNotRun(t *testing.T) {
	runnerBack := func(link string) string {
		return render(t, func() string {
			return commandtext.RunnerBackComment(commandtext.RunnerBack{Head: resultsHead, Pull: 7, PullURL: link})
		})
	}
	want := strings.Join([]string{
		"**The test runner is back.**",
		"The owner unblocked this commit (`0123456`) while the test runner was not responding, so its tests did not run and its checks stopped waiting for them.",
		"Nothing will run them for this commit now. A new push runs the tests as usual.",
		"Details: [pull request page](" + resultsLink + ").",
		linkNote,
	}, "\n\n") + "\n"
	if got := runnerBack(resultsLink); got != want {
		t.Errorf("the comment is\n%s\nwant\n%s", got, want)
	}
	if got := runnerBack(""); !strings.HasSuffix(got, "SuiteWard has no web address set, so this comment has no link. The checks of this commit carry the counts.\n") {
		t.Errorf("a comment with no base address does not say so:\n%s", got)
	}
	for name, b := range map[string]commandtext.RunnerBack{
		"a head that is short":                {Head: "0123456", Pull: 7},
		"a pull request that is not positive": {Head: resultsHead, Pull: 0},
		"a link of the receipt page":          {Head: resultsHead, Pull: 7, PullURL: "http://127.0.0.1:8080/receipts/acme/widgets/7"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: the comment was rendered, want a refusal", name)
				}
			}()
			commandtext.RunnerBackComment(b)
		}()
	}
}

// What a text says about running tests: the three comments are computed from jobs and unblocks, so they may say what ran and what did not, and none says that SuiteWard
// does not run tests as a property of itself.
func TestTheThreeCommentsNeverSayThatSuiteWardDoesNotRunTests(t *testing.T) {
	bodies := map[string]string{
		"not responding": render(t, func() string {
			return commandtext.NotRespondingComment(commandtext.NotResponding{Head: resultsHead, Pull: 7, Unblock: true, PullURL: resultsLink})
		}),
		"runner back": render(t, func() string {
			return commandtext.RunnerBackComment(commandtext.RunnerBack{Head: resultsHead, Pull: 7, PullURL: resultsLink})
		}),
		"none ran": resultsText(t, results(func(r *commandtext.Results) {
			r.Canonical, r.Receipt, r.Next = unavailableCanonical("no-executor"), unavailableReceipt("no-executor"), execution.NextUnblock
		})),
	}
	for name, body := range bodies {
		for _, phrase := range notRunPhrases {
			if strings.Contains(strings.ToLower(body), phrase) {
				t.Errorf("%s: the body says %q:\n%s", name, phrase, body)
			}
		}
	}
}

// resultsSituations are the results comments that reach every lead and every block, for the guard test of the texts.
var resultsSituations = map[string]func(*commandtext.Results){
	"complete and clean": func(*commandtext.Results) {},
	"complete with attention": func(r *commandtext.Results) {
		r.Canonical.Counts.Fail, r.Canonical.Counts.Pass, r.Next = 1, 8, execution.NextFix
	},
	"mixed": func(r *commandtext.Results) {
		r.Receipt, r.Next = unavailableReceipt("no-executor"), execution.NextUnblock
	},
	"none ran": func(r *commandtext.Results) {
		r.Canonical, r.Receipt, r.Next = unavailableCanonical("timeout"), unavailableReceipt("timeout"), execution.NextUnblock
	},
	"unblocked": func(r *commandtext.Results) {
		r.Canonical, r.Unblocked = unavailableCanonical("no-executor"), commandtext.Plans{Canonical: true}
	},
	"did not run, the test runner did not answer": func(r *commandtext.Results) {
		r.CanonicalLevel, r.ReceiptLevel, r.Canonical, r.Receipt = execution.CanonicalLevelAdvisory, execution.ReceiptLevelAdvisory, unavailableCanonical("no-executor"), unavailableReceipt("no-executor")
	},
	"did not run, no test runner is set up": func(r *commandtext.Results) {
		r.CanonicalLevel, r.ReceiptLevel, r.Canonical = execution.CanonicalLevelAdvisory, execution.ReceiptLevelAdvisory, unavailableCanonical("executor-not-configured")
	},
	"did not run, another reason": func(r *commandtext.Results) {
		r.CanonicalLevel, r.ReceiptLevel, r.Receipt = execution.CanonicalLevelAdvisory, execution.ReceiptLevelAdvisory, unavailableReceipt("timeout")
	},
	"ended": func(r *commandtext.Results) {
		r.Canonical, r.Receipt, r.Unblocked, r.Ended = unavailableCanonical("no-executor"), unavailableReceipt("no-executor"), commandtext.Plans{Canonical: true, Receipt: true}, commandtext.Plans{Canonical: true, Receipt: true}
	},
	"approve": func(r *commandtext.Results) { r.Next, r.ApprovalMissing = execution.NextApprove, true },
	"approve with mode": func(r *commandtext.Results) {
		r.Next, r.ApproveMode, r.ApprovalMissing = execution.NextApprove, true, true
	},
	"approve blocked": func(r *commandtext.Results) {
		r.Receipt.Counts, r.Next, r.ApprovalMissing = execution.ReceiptCounts{Skipped: 1}, execution.NextBlocked, true
	},
	"approve, approved tests not run": func(r *commandtext.Results) {
		r.Canonical, r.Next, r.ApprovalMissing = unavailableCanonical("timeout"), execution.NextApprove, true
	},
	"approve, nothing ran and the receipt holds it too": func(r *commandtext.Results) {
		r.Canonical, r.Receipt, r.Next, r.ApprovalMissing = unavailableCanonical("no-executor"), unavailableReceipt("no-executor"), execution.NextUnblock, true
	},
	"weakened": func(r *commandtext.Results) { r.Weakened, r.Next, r.ApprovalMissing = 2, execution.NextApprove, true },
	"approved tests fail, the approve names a mode": func(r *commandtext.Results) {
		r.Canonical.Counts, r.Weakened, r.Next, r.ApproveMode, r.ApprovalMissing = execution.CanonicalCounts{Pass: 8, Fail: 1, Missing: 1}, 1, execution.NextApprove, true, true
	},
	"files that cannot be read and lines to look at": func(r *commandtext.Results) {
		r.Receipt.Counts.Unparsed, r.Receipt.Markers, r.Next = 1, 3, execution.NextFix
	},
	"empty receipt and no approved test": func(r *commandtext.Results) {
		r.Canonical.Counts, r.Receipt.Counts = execution.CanonicalCounts{}, execution.ReceiptCounts{}
	},
	"advisory": func(r *commandtext.Results) {
		r.CanonicalLevel, r.ReceiptLevel = execution.CanonicalLevelAdvisory, execution.ReceiptLevelAdvisory
	},
	"the approved tests only": func(r *commandtext.Results) { r.Receipt, r.ReceiptLevel = nil, execution.ReceiptLevelOff },
	"the receipt only":        func(r *commandtext.Results) { r.Canonical, r.CanonicalLevel = nil, execution.CanonicalLevelOff },
	"no base address":         func(r *commandtext.Results) { r.PullURL = "" },
}
