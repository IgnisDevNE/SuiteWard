package commandtext_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The check summaries state the counts in the words of the results comment (docs/contracts/runtime.md, "Wording"), so a person who reads the comment and the check reads
// one sentence for one fact: ExecutionText and ReceiptFacts say what the comment says.
func TestTheCountsOfTheCheckSummariesAreThoseOfTheResultsComment(t *testing.T) {
	canonical := execution.CanonicalCounts{Pass: 9, Fail: 1, Skipped: 2, Missing: 1, Flaky: 1, Excluded: 3}
	// No excluded tests: the check states how many tests of the receipt SuiteWard cannot run, which the results comment does not (a gap that the owner may close).
	receipt := execution.ReceiptCounts{Detects: 3, Inert: 2, NewAPI: 1, Fails: 2, Missing: 1, Flaky: 6, Skipped: 4, Unparsed: 7}
	comment := resultsText(t, results(func(r *commandtext.Results) {
		r.Canonical = &commandtext.CanonicalRun{Counts: canonical}
		r.Receipt = &commandtext.ReceiptRun{Counts: receipt, Markers: 9}
		r.Next = execution.NextFix
	}))
	approved := commandtext.ExecutionText(commandtext.CheckExecution{Tests: 14, Passed: 9, Skipped: 2, Failed: 1, Missing: 1, Flaky: 1, Excluded: 3})
	if !strings.Contains(comment, approved) {
		t.Errorf("the comment does not say the counts of the approved tests as the check does (%q):\n%s", approved, comment)
	}
	for _, paragraph := range commandtext.ReceiptFacts(receipt, 9) {
		if !strings.Contains(comment, paragraph) {
			t.Errorf("the comment does not say the counts of the receipt as the check does (%q):\n%s", paragraph, comment)
		}
	}
	empty := commandtext.ExecutionText(commandtext.CheckExecution{})
	if want := "Approved tests: none are declared, so none ran."; empty != want {
		t.Errorf("ExecutionText of a run with no declared test = %q, want %q", empty, want)
	}
}
