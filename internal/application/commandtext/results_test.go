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
	linkNote = "This link opens on the machine that runs this SuiteWard instance; on the isolated tier, through a port forward of the HTTP port. Ctrl+click (Cmd+click on a Mac, or middle-click) the link to open it in a new tab."

	resultsEvidence = "These results are what the test runner saw. They are evidence, not a verdict."
	unblockOffer    = "To go on without these tests, the owner can comment `/suiteward unblock <code>`. That lets this commit pass its checks without them. A new push runs them."
)

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
		"Tests this change adds or edits: 3.\n" +
			"- Fail before the change and pass now, so they show the change works: 2\n" +
			"- Pass before and after, so they show nothing: 1\n" +
			"- Use code that the old code lacks, so they could not run before the change: 0\n" +
			"- Fail with this change: 0\n" +
			"- Give no result: 0\n" +
			"- Give different results when run again: 0\n" +
			"- Skipped: 0",
		"Does the receipt meet each kind of change?\n" +
			"- `fix`: met.\n" +
			"- `feature`: met.\n" +
			"- `refactor`: not met. A test behaves differently with the change, so this does not look like a refactor.",
		"To approve, name a kind of change that is met.",
		"Nothing to do for now.",
		resultsEvidence,
		"Details: [pull request page](" + resultsLink + ").",
		linkNote,
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
	list := func(heading string, counts ...int) string {
		labels := []string{"Fail before the change and pass now, so they show the change works", "Pass before and after, so they show nothing",
			"Use code that the old code lacks, so they could not run before the change", "Fail with this change", "Give no result", "Give different results when run again", "Skipped"}
		lines := []string{heading}
		for i, n := range counts {
			lines = append(lines, "- "+labels[i]+": "+strconv.Itoa(n))
		}
		return strings.Join(lines, "\n")
	}
	for name, tc := range map[string]struct {
		run  commandtext.ReceiptRun
		want []string
		not  []string
	}{
		"every state": {commandtext.ReceiptRun{Counts: execution.ReceiptCounts{Detects: 1, Inert: 2, NewAPI: 3, Fails: 4, Missing: 5, Flaky: 6, Skipped: 7, Excluded: 8}},
			[]string{list("Tests this change adds or edits: 28.", 1, 2, 3, 4, 5, 6, 7)}, []string{"Changed test files that cannot be read", "Lines to look at", "no test that SuiteWard can run"}},
		"an empty receipt": {commandtext.ReceiptRun{}, []string{"This change adds or edits no test that SuiteWard can run."}, []string{"Tests this change adds or edits:", "- Skipped"}},
		"a receipt of excluded tests only": {commandtext.ReceiptRun{Counts: execution.ReceiptCounts{Excluded: 2}},
			[]string{"This change adds or edits no test that SuiteWard can run."}, []string{"Tests this change adds or edits:"}},
		"files that cannot be read": {commandtext.ReceiptRun{Counts: execution.ReceiptCounts{Detects: 1, Unparsed: 2}},
			[]string{list("Tests this change adds or edits: 1.", 1, 0, 0, 0, 0, 0, 0), "Changed test files that cannot be read: 2."}, nil},
		"files that cannot be read and no test": {commandtext.ReceiptRun{Counts: execution.ReceiptCounts{Unparsed: 1}},
			[]string{"This change adds or edits no test that SuiteWard can run.", "Changed test files that cannot be read: 1."}, []string{"Tests this change adds or edits:"}},
		"lines to look at": {commandtext.ReceiptRun{Counts: execution.ReceiptCounts{Inert: 1}, Markers: 4},
			[]string{list("Tests this change adds or edits: 1.", 0, 1, 0, 0, 0, 0, 0), "Lines to look at: 4. They are listed on the page."}, nil},
		"a receipt that did not run": {commandtext.ReceiptRun{Unavailable: "timeout"}, []string{"Tests this change adds or edits: not run. The run took longer than its time limit."},
			[]string{"This change adds or edits no test", "Lines to look at"}},
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

func TestResultsCommentSaysWhichKindsOfChangeTheReceiptMeets(t *testing.T) {
	const fixNotMet = "- `fix`: not met. "
	for name, tc := range map[string]struct {
		counts execution.ReceiptCounts
		want   []string
	}{
		"a test that fails without the change": {execution.ReceiptCounts{Detects: 1},
			[]string{"- `fix`: met.", "- `feature`: met.", "- `refactor`: not met. A test behaves differently with the change, so this does not look like a refactor."}},
		"a test that needs new code": {execution.ReceiptCounts{NewAPI: 1},
			[]string{fixNotMet + "No test fails without the change, so none shows that it fixes something.", "- `feature`: met.", "- `refactor`: not met. A test behaves differently with the change, so this does not look like a refactor."}},
		"only inert tests": {execution.ReceiptCounts{Inert: 2},
			[]string{fixNotMet + "No test fails without the change, so none shows that it fixes something.",
				"- `feature`: not met. No test fails without the change or needs new code, so none shows that it adds something.", "- `refactor`: met."}},
		"a test that fails": {execution.ReceiptCounts{Detects: 1, Fails: 1},
			[]string{fixNotMet + "A test fails, is missing or is flaky, or a changed test file cannot be read.", "- `feature`: not met. A test fails, is missing or is flaky, or a changed test file cannot be read.",
				"- `refactor`: not met. A test fails, is missing or is flaky, or a changed test file cannot be read."}},
		"only skipped tests": {execution.ReceiptCounts{Skipped: 1},
			[]string{fixNotMet + "No test fails without the change, so none shows that it fixes something."}},
	} {
		t.Run(name, func(t *testing.T) {
			body := resultsText(t, results(func(r *commandtext.Results) { r.Receipt.Counts = tc.counts }))
			if !strings.Contains(body, "Does the receipt meet each kind of change?\n") {
				t.Errorf("the comment does not ask which kinds of change are met:\n%s", body)
			}
			for _, w := range tc.want {
				if !strings.Contains(body, w+"\n") && !strings.HasSuffix(strings.TrimRight(body, "\n"), w) && !strings.Contains(body, w) {
					t.Errorf("the comment lacks the line %q:\n%s", w, body)
				}
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

func TestResultsCommentEndsWithTheEvidenceSentenceTheTierAndTheLink(t *testing.T) {
	isolated := resultsText(t, results())
	if !hasParagraph(isolated, resultsEvidence) || strings.Contains(isolated, commandtext.CoLocatedNotice) {
		t.Errorf("the isolated comment lacks the evidence sentence, or has the notice of the co-located tier:\n%s", isolated)
	}
	co := resultsText(t, results(func(r *commandtext.Results) { r.Tier = commandtext.CoLocated }))
	if !hasParagraph(co, commandtext.CoLocatedNotice) || strings.Index(co, resultsEvidence) > strings.Index(co, commandtext.CoLocatedNotice) {
		t.Errorf("the co-located comment lacks the notice, or has it before the evidence sentence:\n%s", co)
	}
	if !strings.HasSuffix(isolated, "Details: [pull request page]("+resultsLink+").\n\n"+linkNote+"\n") {
		t.Errorf("the comment does not end with its link and the link note:\n%s", isolated)
	}
	none := resultsText(t, results(func(r *commandtext.Results) { r.PullURL = "" }))
	const noLink = "SuiteWard has no web address set, so this comment has no link. The checks of this commit carry the counts."
	if !strings.HasSuffix(none, noLink+"\n") || strings.Contains(none, "Details:") || strings.Contains(none, linkNote) {
		t.Errorf("a comment with no base address does not say so:\n%s", none)
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
	)
	notResponding := func(unblock bool, link string) string {
		return render(t, func() string {
			return commandtext.NotRespondingComment(commandtext.NotResponding{Head: resultsHead, Pull: 7, Unblock: unblock, PullURL: link})
		})
	}
	want := strings.Join([]string{bold, waiting, problem, unblockOffer, "Details: [pull request page](" + resultsLink + ").", linkNote}, "\n\n") + "\n"
	if got := notResponding(true, resultsLink); got != want {
		t.Errorf("the comment with the offer is\n%s\nwant\n%s", got, want)
	}
	wantPlain := strings.Join([]string{bold, waiting, problem, "Details: [pull request page](" + resultsLink + ").", linkNote}, "\n\n") + "\n"
	if got := notResponding(false, resultsLink); got != wantPlain {
		t.Errorf("the comment without the offer is\n%s\nwant\n%s", got, wantPlain)
	}
	const noLink = "SuiteWard has no web address set, so this comment has no link. The checks of this commit carry the counts."
	if got := notResponding(true, ""); !strings.HasSuffix(got, unblockOffer+"\n\n"+noLink+"\n") || strings.Contains(got, "Details:") {
		t.Errorf("a comment with no base address does not say so:\n%s", got)
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
