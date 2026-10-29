package commandtext

import "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"

// The three comments of a head (docs/contracts/runtime.md, "The comments of a head"): the results comment, the not-responding comment and the runner-back comment.
// Each is built from counts and from closed sets of codes, so no test name, no path and no text of the repository or of the test runner can reach it.

// CanonicalRun is the canonical run that a results comment reports: complete, with its counts, or unavailable, with the code of its reason.
type CanonicalRun struct {
	Unavailable string // the reason code of an unavailable run, empty for a complete one
	Counts      execution.CanonicalCounts
}

// ReceiptRun is the receipt run that a results comment reports: complete, with its counts and the marker lines it saw, or unavailable, with the code of its reason.
type ReceiptRun struct {
	Unavailable string // the reason code of an unavailable run, empty for a complete one
	Counts      execution.ReceiptCounts
	Markers     int
}

// Results is what the results comment of a head states. Canonical and Receipt are the wanted runs, nil for one that is not wanted; at least one is set, and each is
// final. Proposal and Revision name the revision of the head (both zero for none), which the approve command needs. Unblocked holds the plans whose job the owner
// unblocked, and Ended those whose job the command ended: a subset of Unblocked. PullURL is the link to the pull request page, empty without a base address.
type Results struct {
	Head               string // the 40 lowercase hexadecimal digits of the head
	Pull               int
	Tier               Tier
	Proposal, Revision int
	CanonicalLevel     execution.CanonicalLevel
	ReceiptLevel       execution.ReceiptLevel
	Canonical          *CanonicalRun
	Receipt            *ReceiptRun
	Weakened           int
	Unblocked, Ended   Plans
	Next               execution.Next
	ApproveMode        bool // the approve command names a mode
	PullURL            string
}

// NotResponding is what the not-responding comment of a head states. Unblock says that the unblock is offered.
type NotResponding struct {
	Head    string
	Pull    int
	Unblock bool
	PullURL string
}

// RunnerBack is what the runner-back comment of a head states.
type RunnerBack struct {
	Head    string
	Pull    int
	PullURL string
}

// ResultsComment renders the results comment of a head. The values come from storage, so one that does not fit is an impossible state and panics.
func ResultsComment(Results) string { return "" }

// NotRespondingComment renders the not-responding comment of a head.
func NotRespondingComment(NotResponding) string { return "" }

// RunnerBackComment renders the runner-back comment of a head.
func RunnerBackComment(RunnerBack) string { return "" }
