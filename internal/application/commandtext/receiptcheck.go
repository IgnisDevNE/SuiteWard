package commandtext

import (
	"errors"
	"fmt"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// ReceiptCheck is what the output of the SuiteWard / Receipt check run states (docs/contracts/runtime.md, "The SuiteWard / Receipt check"). Reason is the stable code
// the checks job stored (receipt-running, receipt-not-responding, receipt-failing, receipt-complete, receipt-not-run or receipt-not-applicable), and State must be the one the code belongs to.
// Proposal and Revision name the revision the check concerns. Counts and Markers are the receipt of the head: the tests by state, the excluded ones, the changed test
// files that do not parse and the marker lines seen. Unavailable is the reason code of a job that was not run (receipt-not-run only), and Unblocked says that the owner
// unblocked that job. A receipt summary is public: no repository text, no path, no test name, no link and no mode.
type ReceiptCheck struct {
	State              CheckState
	Reason             string
	Proposal, Revision int
	Tier               Tier
	Counts             execution.ReceiptCounts
	Markers            int
	Unavailable        string
	Unblocked          bool
	// OffersUnblock is the offer of /suiteward unblock that a receipt-not-responding check makes, where the receipt gate would refuse an approve.
	OffersUnblock bool
}

// receiptReason is the output of one reason of the Receipt check: its state, a title and what happened. The titles of the states are the contract's; the texts are a task's.
type receiptReason struct {
	state CheckState
	title string
	text  string
}

var receiptReasons = map[string]receiptReason{
	"receipt-running": {CheckInProgress, "Receipt pending",
		"The receipt of this commit, the run of the tests that this change adds or edits, has not finished. The tests are waiting for the test runner, or they are running. This check has no result yet."},
	"receipt-not-responding": {CheckInProgress, "Receipt waiting: test runner not responding",
		"The test runner has not answered for about half a minute, so the receipt of this commit has not run. SuiteWard keeps trying. This check has no result yet."},
	"receipt-failing": {CheckFailure, "Receipt: failing or missing tests",
		"At least one test that this change adds or edits fails or gives no result with this change, or a changed test file cannot be read. The counts below are what SuiteWard saw. " +
			"To fix this, change the code or the tests and push a new commit."},
	"receipt-complete": {CheckSuccess, "Receipt ready",
		"SuiteWard ran the tests that this change adds or edits, with the change and on the code before it. The counts below are what it saw."},
	"receipt-not-run": {CheckNeutral, "Receipt unavailable",
		"SuiteWard could not run the receipt of this commit."},
	"receipt-not-applicable": {CheckNeutral, "No receipt for this revision",
		"This commit has no receipt. Its revision changes no file that the approved patterns match, or the receipt is turned off for this repository. Nothing to do."},
}

const (
	// evidenceSentence closes every summary but the one of a receipt that does not apply. The check is written before the runs are final, so it speaks of what the test runner saw, not of results that it may not have.
	evidenceSentence         = "What the test runner saw is evidence, not a verdict."
	receiptUnblockedSentence = "The owner unblocked this commit. Its receipt could not run, so the tests of this change did not run."
	// receiptUnblockOffer ends the text of receipt-not-responding where the receipt gate would refuse an approve.
	receiptUnblockOffer = "To approve without the receipt, the owner can comment `/suiteward unblock <code>`."
	// emptyReceiptText is the text of receipt-complete when no test was evaluated: "SuiteWard ran the tests" would say something the run did not do.
	emptyReceiptText = "The receipt of this commit is empty, so there is nothing to judge."
)

// ReceiptCheckSummary renders the output title and summary of the Receipt check run: the state and what it means, the revision, the tier, the counts of the receipt that completed,
// that the results are evidence (unless nothing was observed: receipt-not-applicable), for a receipt that was not run why, and the owner's unblock. The reason, the state and the
// counts come from storage, so a value that does not fit is an impossible state and panics.
func ReceiptCheckSummary(c ReceiptCheck) (title, summary string) {
	reason, err := validateReceiptCheck(c)
	must(err)
	text := reason.text
	if c.Reason == "receipt-complete" && c.Counts.Empty() {
		text = emptyReceiptText
	}
	if c.OffersUnblock {
		text += " " + receiptUnblockOffer
	}
	parts := []string{text}
	if c.Unavailable != "" {
		parts = append(parts, unavailableSentences[c.Unavailable])
	}
	facts := []string{"- State: `" + checkStateName[c.State] + "`"}
	if c.Proposal != 0 {
		facts = append(facts, "- Proposal revision: "+Reference(c.Proposal, c.Revision))
	}
	parts = append(parts, strings.Join(append(facts, tierLine(c.Tier)), "\n"))
	if c.Reason == "receipt-failing" || c.Reason == "receipt-complete" { // only a receipt that completed has counts
		parts = append(parts, ReceiptFacts(c.Counts, c.Markers)...)
	}
	if c.Reason != "receipt-not-applicable" {
		parts = append(parts, evidenceSentence)
	}
	if c.Unblocked {
		parts = append(parts, receiptUnblockedSentence)
	}
	if c.Tier == CoLocated {
		parts = append(parts, CoLocatedNotice)
	}
	return reason.title, paragraphs(parts...)
}

// validateReceiptCheck reports why ReceiptCheckSummary would panic on c, and returns the output of its reason. The counts are those of a receipt that completed, so only
// receipt-failing (a test that FAILS or is MISSING, or a file that does not parse) and receipt-complete (none of them) have any.
func validateReceiptCheck(c ReceiptCheck) (receiptReason, error) {
	reason, known := receiptReasons[c.Reason]
	if !known || reason.state != c.State {
		return reason, fmt.Errorf("commandtext: receipt check reason %q does not fit state %d", c.Reason, c.State)
	}
	n := c.Counts
	completed := c.Reason == "receipt-failing" || c.Reason == "receipt-complete"
	blocking := n.Fails + n.Missing + n.Unparsed
	switch {
	case c.Tier != Isolated && c.Tier != CoLocated:
		return reason, fmt.Errorf("commandtext: unknown tier %d", c.Tier)
	case (c.Proposal == 0) != (c.Revision == 0):
		return reason, fmt.Errorf("commandtext: receipt check reference P%d-R%d does not fit", c.Proposal, c.Revision)
	case min(n.Detects, n.Inert, n.NewAPI, n.Fails, n.Missing, n.Flaky, n.Skipped, n.Excluded, n.Unparsed, c.Markers) < 0:
		return reason, errors.New("commandtext: a count of the receipt is negative")
	case !completed && (n != execution.ReceiptCounts{} || c.Markers != 0):
		return reason, fmt.Errorf("commandtext: reason %q has no counts of a receipt that completed", c.Reason)
	case c.Reason == "receipt-failing" && blocking == 0:
		return reason, errors.New("commandtext: receipt-failing needs a test that FAILS or is MISSING, or a file that does not parse")
	case c.Reason == "receipt-complete" && blocking > 0:
		return reason, errors.New("commandtext: receipt-complete has a test that FAILS or is MISSING, or a file that does not parse")
	case c.Reason == "receipt-not-run" && unavailableSentences[c.Unavailable] == "":
		return reason, errors.New("commandtext: a receipt that was not run needs a reason of the closed set")
	case c.Reason != "receipt-not-run" && (c.Unavailable != "" || c.Unblocked):
		return reason, fmt.Errorf("commandtext: reason %q has no unavailable reason and no unblock", c.Reason)
	case c.OffersUnblock && c.Reason != "receipt-not-responding":
		return reason, fmt.Errorf("commandtext: reason %q offers no unblock: only a receipt that waits for a runner that does not respond does", c.Reason)
	}
	if c.Proposal != 0 {
		if _, err := reference(c.Proposal, c.Revision); err != nil {
			return reason, err
		}
	}
	return reason, nil
}

// ReceiptFacts are the paragraphs that state the counts of a receipt that completed, in the words of the results comment: the tests that the change adds or edits by what they
// show, then one paragraph of notes (the tests that SuiteWard cannot run, the changed test files that cannot be read and the lines to look at, each only when there are some).
// The results comment says the same sentences, with a link to the page where the lines are listed.
func ReceiptFacts(n execution.ReceiptCounts, markers int) []string {
	var parts []string
	if n.Evaluated() == 0 {
		parts = append(parts, "This change adds or edits no test that SuiteWard can run.")
	} else {
		parts = append(parts, fmt.Sprintf("Tests this change adds or edits: %d.\n"+
			"- Fail before the change and pass now, so they show the change works: %d\n"+
			"- Pass before and after, so they show nothing: %d\n"+
			"- Use code that the old code lacks, so they could not run before the change: %d\n"+
			"- Fail with this change: %d\n"+
			"- Give no result: %d\n"+
			"- Give different results when run again: %d\n"+
			"- Skipped: %d", n.Evaluated(), n.Detects, n.Inert, n.NewAPI, n.Fails, n.Missing, n.Flaky, n.Skipped))
	}
	var notes []string
	if n.Excluded > 0 {
		notes = append(notes, counted(n.Excluded, "test that this change adds or edits cannot be run by SuiteWard and is not covered", "tests that this change adds or edits cannot be run by SuiteWard and are not covered")+".")
	}
	if n.Unparsed > 0 {
		notes = append(notes, fmt.Sprintf("Changed test files that cannot be read: %d.", n.Unparsed))
	}
	if markers > 0 {
		notes = append(notes, fmt.Sprintf("Lines to look at: %d.", markers))
	}
	if len(notes) > 0 {
		parts = append(parts, strings.Join(notes, " "))
	}
	return parts
}
