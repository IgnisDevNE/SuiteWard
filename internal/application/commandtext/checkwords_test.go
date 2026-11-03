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
	facts := commandtext.ReceiptFacts(receipt, 9)
	for _, paragraph := range facts {
		if !strings.Contains(comment, paragraph) {
			t.Errorf("the comment does not say the counts of the receipt as the check does (%q):\n%s", paragraph, comment)
		}
	}
	// The check says the table of the comment, not a list of its own: the number of tests, then the table, as two paragraphs.
	if want := countsParagraphs(receipt.Evaluated(), 3, 2, 1, 2, 1, 6, 4); len(facts) < 2 || facts[0] != want[0] || facts[1] != want[1] {
		t.Errorf("the counts of the receipt in the check are\n%q\nwant them to open with\n%q", facts, want)
	}
	empty := commandtext.ExecutionText(commandtext.CheckExecution{})
	if want := "Approved tests: none are declared, so none ran."; empty != want {
		t.Errorf("ExecutionText of a run with no declared test = %q, want %q", empty, want)
	}
}

// The unblock is offered in the same words by the comment, the Receipt check, the Contract check and the refusal of an approve (M2.1c, SW-47; docs/plan/phases/M2.1c.md, text 4): a
// reader meets one offer, which says what the code is and that the tests do not run for this commit.
func TestTheUnblockOfTheChecksAndTheRefusalIsOfferedInTheWordsOfTheComment(t *testing.T) {
	const codeIs = "`<code>` is a current code from the authenticator app."
	comment := resultsText(t, results(func(r *commandtext.Results) {
		r.Canonical, r.Receipt, r.Next = unavailableCanonical("no-executor"), nil, execution.NextUnblock
	}))
	c := receiptOf("receipt-not-responding")
	c.OffersUnblock = true
	_, receipt := receiptText(t, c)
	for name, body := range map[string]string{"comment": comment, "Receipt check": receipt} {
		if !strings.Contains(body, "`/suiteward unblock <code>`, where "+codeIs) {
			t.Errorf("the %s does not say what the code is as the offer of the comments does:\n%s", name, body)
		}
	}
	// The checks that show the command to copy say it as the approve command does, and say what the unblock costs.
	_, contract := checkText(t, requiredCheck("canonical-unavailable"))
	ack := commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.ReceiptRefused, Source: source, Owner: owner, Proposal: 4, Revision: 2, Gate: execution.ReasonReceiptUnavailable})
	for name, body := range map[string]string{"Contract check": contract, "refusal": ack} {
		if !strings.Contains(body, "Replace `<code>` with a current code from your authenticator app.") {
			t.Errorf("the %s does not say what the code is as the approve command does:\n%s", name, body)
		}
	}
	if !strings.Contains(contract, "they do not run for this commit. A new push runs them.") || !strings.Contains(receipt, "does not run for this commit, and a new push runs it.") {
		t.Errorf("a check does not say that the tests do not run for this commit after an unblock:\n%s\n%s", contract, receipt)
	}
}
