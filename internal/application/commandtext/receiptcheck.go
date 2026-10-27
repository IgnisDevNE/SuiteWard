package commandtext

import "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"

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

// ReceiptCheckSummary renders the output title and summary of the Receipt check run. A value that does not fit is an impossible state and panics.
func ReceiptCheckSummary(c ReceiptCheck) (title, summary string) {
	return "", ""
}
