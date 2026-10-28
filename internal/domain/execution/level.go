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

var canonicalLevelNames = map[CanonicalLevel]string{
	CanonicalLevelOff:      "off",
	CanonicalLevelAdvisory: "advisory",
	CanonicalLevelRequired: "required",
}

// ParseCanonicalLevel reads off, advisory or required, exactly as spelled.
func ParseCanonicalLevel(s string) (CanonicalLevel, error) {
	return parseEnum(canonicalLevelNames, s, ErrInvalidLevel)
}

// String returns the level as .suiteward.yml spells it, or empty for a value that is not a level.
func (l CanonicalLevel) String() string { return canonicalLevelNames[l] }

// ReceiptLevel is the policy for the receipt, from the weakest to the strongest: off, advisory, before-approval, required.
type ReceiptLevel uint8

// The levels of the receipt.
const (
	ReceiptLevelOff ReceiptLevel = iota + 1
	ReceiptLevelAdvisory
	ReceiptLevelBeforeApproval
	ReceiptLevelRequired
)

var receiptLevelNames = map[ReceiptLevel]string{
	ReceiptLevelOff:            "off",
	ReceiptLevelAdvisory:       "advisory",
	ReceiptLevelBeforeApproval: "before-approval",
	ReceiptLevelRequired:       "required",
}

// ParseReceiptLevel reads off, advisory, before-approval or required, exactly as spelled.
func ParseReceiptLevel(s string) (ReceiptLevel, error) {
	return parseEnum(receiptLevelNames, s, ErrInvalidLevel)
}

// String returns the level as .suiteward.yml spells it, or empty for a value that is not a level.
func (l ReceiptLevel) String() string { return receiptLevelNames[l] }
