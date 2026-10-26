// Package execution holds the rules of canonical execution and the receipt (M2.1): the policy levels, how a plan's
// tree is composed, how the observations of a test become a state, the mode summaries, the two gates, the conclusion of
// the Receipt check, the source-integrity check and the evidence binding. It is pure: no I/O, no clock.
package execution

import "errors"

// ErrInvalidLevel reports a policy level that is not one of the levels of its kind.
var ErrInvalidLevel = errors.New("invalid execution policy level")

// CanonicalLevel is the policy for canonical execution, from the weakest to the strongest: off, advisory, required.
type CanonicalLevel uint8

// The levels of canonical execution.
const (
	CanonicalLevelOff CanonicalLevel = iota + 1
	CanonicalLevelAdvisory
	CanonicalLevelRequired
)

// ParseCanonicalLevel reads off, advisory or required, exactly as spelled.
func ParseCanonicalLevel(string) (CanonicalLevel, error) { return 0, errNotImplemented }

// String returns the level as .suiteward.yml spells it, or empty for a value that is not a level.
func (CanonicalLevel) String() string { return "" }

// Weaker reports whether the level is strictly weaker than than. It is defined for the levels of ParseCanonicalLevel.
func (CanonicalLevel) Weaker(CanonicalLevel) bool { return false }

// ReceiptLevel is the policy for the receipt, from the weakest to the strongest: off, advisory, before-approval, required.
type ReceiptLevel uint8

// The levels of the receipt.
const (
	ReceiptLevelOff ReceiptLevel = iota + 1
	ReceiptLevelAdvisory
	ReceiptLevelBeforeApproval
	ReceiptLevelRequired
)

// ParseReceiptLevel reads off, advisory, before-approval or required, exactly as spelled.
func ParseReceiptLevel(string) (ReceiptLevel, error) { return 0, errNotImplemented }

// String returns the level as .suiteward.yml spells it, or empty for a value that is not a level.
func (ReceiptLevel) String() string { return "" }

// Weaker reports whether the level is strictly weaker than than. It is defined for the levels of ParseReceiptLevel.
func (ReceiptLevel) Weaker(ReceiptLevel) bool { return false }
