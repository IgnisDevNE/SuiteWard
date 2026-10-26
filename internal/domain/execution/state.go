package execution

import "errors"

var (
	// ErrInvalidObservation reports an observation that is not one of the six.
	ErrInvalidObservation = errors.New("invalid observation")
	// ErrInvalidState reports a test state that is not one of its kind.
	ErrInvalidState = errors.New("invalid test state")
	// ErrUnclassifiable reports observations that no row of the precedence table decides.
	ErrUnclassifiable = errors.New("observations no state decides")
)

// Observation is what one side of one test showed.
type Observation uint8

// The observations of a test side.
const (
	ObservedPass       Observation = iota + 1 // the test passed
	ObservedFail                              // a failed assertion, a panic or a timeout
	ObservedBuildError                        // the package or file did not build, so the test never ran
	ObservedSkip                              // the test skipped itself
	ObservedAbsent                            // expected, but no result
	ObservedExcluded                          // declared, but not runnable in this profile
)

// ParseObservation reads pass, fail, build-error, skip, absent or excluded, exactly as spelled.
func ParseObservation(string) (Observation, error) { return 0, errNotImplemented }

// String returns the observation as the executor reports it, or empty for a value that is not one.
func (Observation) String() string { return "" }

// Side is the outcome of one test on one side (head or base): the observation after the reruns, and whether the runs of
// the side differed, which makes the test flaky.
type Side struct {
	Result Observation
	Varied bool
}

// ReceiptState is the state of an evaluated test in the receipt.
type ReceiptState uint8

// The receipt states. ReceiptNewAPI is shown as "NEW API".
const (
	ReceiptExcluded ReceiptState = iota + 1
	ReceiptMissing
	ReceiptFlaky
	ReceiptSkipped
	ReceiptFails
	ReceiptNewAPI
	ReceiptDetects
	ReceiptInert
)

// String returns the stored name of the state (NEW_API for ReceiptNewAPI), or empty for a value that is not a state.
func (ReceiptState) String() string { return "" }

// CanonicalState is the state of a declared test in a canonical run.
type CanonicalState uint8

// The canonical states.
const (
	CanonicalPass CanonicalState = iota + 1
	CanonicalFail
	CanonicalSkipped
	CanonicalMissing
	CanonicalFlaky
	CanonicalExcluded
)

// String returns the stored name of the state, or empty for a value that is not a state.
func (CanonicalState) String() string { return "" }

// ClassifyReceipt decides the receipt state of an evaluated test from its head and base sides, first match winning:
// EXCLUDED, MISSING (head absent, or base absent while the base package built), FLAKY, SKIPPED, FAILS, NEW_API, DETECTS,
// INERT. A head pass over a base that is absent although its package did not build is no row of the table, and an error.
func ClassifyReceipt(head, base Side, baseBuilt bool) (ReceiptState, error) {
	return 0, errNotImplemented
}

// ClassifyCanonical decides the canonical state of a declared test from its head side. The design gives no order, so it
// mirrors the receipt's: EXCLUDED, MISSING, FLAKY, SKIPPED, FAIL (a failure or a build error), PASS.
func ClassifyCanonical(head Side) (CanonicalState, error) { return 0, errNotImplemented }

// ReceiptCounts counts the evaluated tests of a receipt by state. EXCLUDED tests are counted apart and are never a pass.
type ReceiptCounts struct {
	Excluded, Missing, Flaky, Skipped, Fails, NewAPI, Detects, Inert int
}

// CountReceipt counts states.
func CountReceipt(states []ReceiptState) (ReceiptCounts, error) {
	return ReceiptCounts{}, errNotImplemented
}

// Evaluated is the number of tests that ran: all but the EXCLUDED ones.
func (ReceiptCounts) Evaluated() int { return 0 }

// Blocking is the number of tests that block a mode summary and the gates: FAILS, MISSING and FLAKY.
func (ReceiptCounts) Blocking() int { return 0 }

// CanonicalCounts counts the declared tests of a canonical run by state. EXCLUDED tests are counted apart and are never a pass.
type CanonicalCounts struct {
	Pass, Fail, Skipped, Missing, Flaky, Excluded int
}

// CountCanonical counts states.
func CountCanonical(states []CanonicalState) (CanonicalCounts, error) {
	return CanonicalCounts{}, errNotImplemented
}

// Blocking is the number of tests that block the canonical gate: FAIL, MISSING and FLAKY.
func (CanonicalCounts) Blocking() int { return 0 }
