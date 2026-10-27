package commandtext

import (
	"errors"
	"fmt"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// ReceiptCheck is what the output of the SuiteWard / Receipt check run states (docs/contracts/runtime.md, "The SuiteWard / Receipt check"). Reason is the stable code
// the checks job stored (receipt-running, receipt-failing, receipt-complete, receipt-not-run or receipt-not-applicable), and State must be the one the code belongs to.
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
}

// receiptReason is the output of one reason of the Receipt check: its state, a title and what happened. The titles of the states are the contract's; the texts are a task's.
type receiptReason struct {
	state CheckState
	title string
	text  string
}

var receiptReasons = map[string]receiptReason{
	"receipt-running": {CheckInProgress, "Receipt pending",
		"SuiteWard is running the tests that this revision adds or changes, on the head and on the code before the change. The check has no result yet."},
	"receipt-failing": {CheckFailure, "Receipt: failing or missing tests",
		"At least one test that this revision adds or changes FAILS or is MISSING on the head, or a changed test file does not parse. The counts below are what SuiteWard saw."},
	"receipt-complete": {CheckSuccess, "Receipt ready",
		"SuiteWard ran the tests that this revision adds or changes, on the head and on the code before the change. The counts below are what it saw."},
	"receipt-not-run": {CheckNeutral, "Receipt unavailable",
		"SuiteWard could not run the receipt of this head."},
	"receipt-not-applicable": {CheckNeutral, "No receipt for this revision",
		"This head has no receipt now: its revision changes no file that the approved patterns match, or the receipt is turned off for this repository."},
}

// unavailableWords say why a receipt job was unavailable, in words, for each code of the closed set (docs/contracts/runtime.md, "Unavailable reasons").
var unavailableWords = map[string]string{
	"executor-error":          "the executor failed in a way that no other reason names",
	"sandbox-unavailable":     "the sandbox could not be built, or the runtime cannot honor a property that the run requires",
	"archive-unavailable":     "an archive or a manifest file could not be read",
	"lease-expired":           "the executor was lost before it reported a result",
	"timeout":                 "the run went past its time limit",
	"github-unavailable":      "GitHub could not be read",
	"no-executor":             "no executor was seen in time",
	"fetch-failed":            "a module could not be fetched from the Go module proxy",
	"install-failed":          "the dependency prefetch was refused before any module was fetched",
	"resource-limit":          "the run was killed by a resource limit",
	"toolchain-mismatch":      "the repository needs a Go toolchain that this release does not have",
	"source-mismatch":         "the source of the head could not be verified",
	"too-large":               "an archive, a tree, a declaration or a result passed its size limit",
	"profile-unsupported":     "the executor does not support the profile of the job",
	"executor-not-configured": "no executor is configured for this instance",
	"no-merge-base":           "GitHub has no merge base for this head and the base branch",
}

const (
	evidenceSentence         = "Observed by this instance's executor. Evidence, not a verdict."
	receiptUnblockedSentence = "The owner unblocked this head: its receipt was unavailable, so the tests did not run."
)

// ReceiptCheckSummary renders the output title and summary of the Receipt check run: the state and what it means, the revision, the tier, the count of each state, the
// excluded tests, the marker lines and the changed test files that do not parse, that the results are evidence, for a receipt that was not run why, and the owner's
// unblock. The reason, the state and the counts come from storage, so a value that does not fit is an impossible state and panics.
func ReceiptCheckSummary(c ReceiptCheck) (title, summary string) {
	reason, err := validateReceiptCheck(c)
	must(err)
	parts := []string{reason.text}
	if c.Unavailable != "" {
		parts = append(parts, "Why: "+unavailableWords[c.Unavailable]+".")
	}
	facts := []string{"- State: `" + checkStateName[c.State] + "`"}
	if c.Proposal != 0 {
		facts = append(facts, "- Proposal revision: "+Reference(c.Proposal, c.Revision))
	}
	facts = append(facts, tierLine(c.Tier))
	for _, fact := range ReceiptFacts(c.Counts, c.Markers) {
		facts = append(facts, "- "+fact)
	}
	parts = append(parts, strings.Join(facts, "\n"), evidenceSentence)
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
	case c.Reason == "receipt-not-run" && unavailableWords[c.Unavailable] == "":
		return reason, errors.New("commandtext: a receipt that was not run needs a reason of the closed set")
	case c.Reason != "receipt-not-run" && (c.Unavailable != "" || c.Unblocked):
		return reason, fmt.Errorf("commandtext: reason %q has no unavailable reason and no unblock", c.Reason)
	}
	if c.Proposal != 0 {
		if _, err := reference(c.Proposal, c.Revision); err != nil {
			return reason, err
		}
	}
	return reason, nil
}

// UnavailableReason says in words why a job is unavailable, for a reason code of the closed set; ok is false for any other.
func UnavailableReason(code string) (words string, ok bool) {
	words, ok = unavailableWords[code]
	return words, ok
}

// ReceiptFacts are the counts of a receipt as the Receipt check summary states them, one line each: the tests by state, the excluded tests, the marker lines seen and
// the changed test files that do not parse. The receipt page states the same.
func ReceiptFacts(n execution.ReceiptCounts, markers int) []string {
	return []string{
		fmt.Sprintf("Tests by state: %d DETECTS, %d INERT, %d NEW API, %d FAILS, %d MISSING, %d FLAKY, %d SKIPPED", n.Detects, n.Inert, n.NewAPI, n.Fails, n.Missing, n.Flaky, n.Skipped),
		fmt.Sprintf("Excluded tests: %d", n.Excluded),
		fmt.Sprintf("Marker lines seen: %d", markers),
		fmt.Sprintf("Changed test files that do not parse: %d", n.Unparsed),
	}
}

// ModeReason says in words why the summary of a mode is not met, for a reason of the domain; ok is false for any other.
func ModeReason(reason execution.Reason) (words string, ok bool) {
	words, ok = modeReasons[reason]
	return words, ok
}

var modeReasons = map[execution.Reason]string{
	execution.ReasonNoEvaluatedTests: "no test ran, so nothing detects the change",
	execution.ReasonBlockingTests:    "a test fails, is missing or is flaky, or a changed test file does not parse",
	execution.ReasonNoDetection:      "no test detects anything",
	execution.ReasonNotInert:         "a test is not inert",
}
