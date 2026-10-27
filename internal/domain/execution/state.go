package execution

import (
	"errors"
	"fmt"
)

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

var observationNames = map[Observation]string{
	ObservedPass:       "pass",
	ObservedFail:       "fail",
	ObservedBuildError: "build-error",
	ObservedSkip:       "skip",
	ObservedAbsent:     "absent",
	ObservedExcluded:   "excluded",
}

// ParseObservation reads pass, fail, build-error, skip, absent or excluded, exactly as spelled.
func ParseObservation(s string) (Observation, error) {
	return parseEnum(observationNames, s, ErrInvalidObservation)
}

// String returns the observation as the executor reports it, or empty for a value that is not one.
func (o Observation) String() string { return observationNames[o] }

// Side is the outcome of one test on one side (head or base): the observation after the reruns, and whether the runs of
// the side differed, which makes the test flaky.
type Side struct {
	Result Observation
	Varied bool
}

func (s Side) validate() error {
	if _, ok := observationNames[s.Result]; !ok {
		return fmt.Errorf("%w: %d", ErrInvalidObservation, s.Result)
	}
	return nil
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

var receiptStateNames = map[ReceiptState]string{
	ReceiptExcluded: "EXCLUDED",
	ReceiptMissing:  "MISSING",
	ReceiptFlaky:    "FLAKY",
	ReceiptSkipped:  "SKIPPED",
	ReceiptFails:    "FAILS",
	ReceiptNewAPI:   "NEW_API",
	ReceiptDetects:  "DETECTS",
	ReceiptInert:    "INERT",
}

// String returns the stored name of the state (NEW_API for ReceiptNewAPI), or empty for a value that is not a state.
func (s ReceiptState) String() string { return receiptStateNames[s] }

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

var canonicalStateNames = map[CanonicalState]string{
	CanonicalPass:     "PASS",
	CanonicalFail:     "FAIL",
	CanonicalSkipped:  "SKIPPED",
	CanonicalMissing:  "MISSING",
	CanonicalFlaky:    "FLAKY",
	CanonicalExcluded: "EXCLUDED",
}

// String returns the stored name of the state, or empty for a value that is not a state.
func (s CanonicalState) String() string { return canonicalStateNames[s] }

// ClassifyReceipt decides the receipt state of an evaluated test from its head and base sides, first match winning:
// EXCLUDED, MISSING (head absent, or base absent while the base package built), FLAKY, SKIPPED, FAILS, NEW_API, DETECTS,
// INERT. A head pass over a base that is absent although its package did not build is no row of the table, and an error.
func ClassifyReceipt(head, base Side, baseBuilt bool) (ReceiptState, error) {
	if err := errors.Join(head.validate(), base.validate()); err != nil {
		return 0, err
	}
	switch {
	case head.Result == ObservedExcluded || base.Result == ObservedExcluded:
		return ReceiptExcluded, nil
	case head.Result == ObservedAbsent || (base.Result == ObservedAbsent && baseBuilt):
		return ReceiptMissing, nil
	case head.Varied || base.Varied:
		return ReceiptFlaky, nil
	case head.Result == ObservedSkip:
		return ReceiptSkipped, nil
	case head.Result == ObservedFail || head.Result == ObservedBuildError:
		return ReceiptFails, nil
	}
	// The head passed.
	switch base.Result {
	case ObservedBuildError:
		return ReceiptNewAPI, nil
	case ObservedFail:
		return ReceiptDetects, nil
	case ObservedPass, ObservedSkip:
		return ReceiptInert, nil
	}
	return 0, fmt.Errorf("%w: head pass over an absent base whose package did not build", ErrUnclassifiable)
}

// ClassifyCanonical decides the canonical state of a declared test from its head side. The design gives no order, so it
// mirrors the receipt's: EXCLUDED, MISSING, FLAKY, SKIPPED, FAIL (a failure or a build error), PASS.
func ClassifyCanonical(head Side) (CanonicalState, error) {
	if err := head.validate(); err != nil {
		return 0, err
	}
	switch {
	case head.Result == ObservedExcluded:
		return CanonicalExcluded, nil
	case head.Result == ObservedAbsent:
		return CanonicalMissing, nil
	case head.Varied:
		return CanonicalFlaky, nil
	case head.Result == ObservedSkip:
		return CanonicalSkipped, nil
	case head.Result == ObservedFail || head.Result == ObservedBuildError:
		return CanonicalFail, nil
	}
	return CanonicalPass, nil
}

// ReceiptCounts counts the evaluated tests of a receipt by state. EXCLUDED tests are counted apart and are never a pass. Unparsed is the
// number of changed test files that go cannot parse, which list no test: the caller sets it, and each counts as a blocking failure.
type ReceiptCounts struct {
	Excluded, Missing, Flaky, Skipped, Fails, NewAPI, Detects, Inert int
	Unparsed                                                         int
}

// CountReceipt counts states.
func CountReceipt(states []ReceiptState) (ReceiptCounts, error) {
	var c ReceiptCounts
	for _, state := range states {
		switch state {
		case ReceiptExcluded:
			c.Excluded++
		case ReceiptMissing:
			c.Missing++
		case ReceiptFlaky:
			c.Flaky++
		case ReceiptSkipped:
			c.Skipped++
		case ReceiptFails:
			c.Fails++
		case ReceiptNewAPI:
			c.NewAPI++
		case ReceiptDetects:
			c.Detects++
		case ReceiptInert:
			c.Inert++
		default:
			return ReceiptCounts{}, fmt.Errorf("%w: %d", ErrInvalidState, state)
		}
	}
	return c, nil
}

// Evaluated is the number of tests the receipt judges: all but the EXCLUDED ones. A MISSING test is evaluated, though it did not run.
func (c ReceiptCounts) Evaluated() int {
	return c.Missing + c.Flaky + c.Skipped + c.Fails + c.NewAPI + c.Detects + c.Inert
}

// Blocking is the number of tests that block a mode summary and the gates: FAILS, MISSING and FLAKY.
func (c ReceiptCounts) Blocking() int { return c.Fails + c.Missing + c.Flaky }

// CanonicalCounts counts the declared tests of a canonical run by state. EXCLUDED tests are counted apart and are never a pass.
type CanonicalCounts struct {
	Pass, Fail, Skipped, Missing, Flaky, Excluded int
}

// CountCanonical counts states.
func CountCanonical(states []CanonicalState) (CanonicalCounts, error) {
	var c CanonicalCounts
	for _, state := range states {
		switch state {
		case CanonicalPass:
			c.Pass++
		case CanonicalFail:
			c.Fail++
		case CanonicalSkipped:
			c.Skipped++
		case CanonicalMissing:
			c.Missing++
		case CanonicalFlaky:
			c.Flaky++
		case CanonicalExcluded:
			c.Excluded++
		default:
			return CanonicalCounts{}, fmt.Errorf("%w: %d", ErrInvalidState, state)
		}
	}
	return c, nil
}

// Blocking is the number of tests that block the canonical gate: FAIL, MISSING and FLAKY.
func (c CanonicalCounts) Blocking() int { return c.Fail + c.Missing + c.Flaky }

// Empty reports whether the receipt has nothing to judge.
func (c ReceiptCounts) Empty() bool { return c.Evaluated() == 0 }
