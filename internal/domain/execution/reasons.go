package execution

import (
	"errors"
	"fmt"
	"slices"
)

// ErrNoTestReason reports a test whose state and runs no row of the table of reasons decides: they contradict each other, or the reason of an excluded test is not
// one of the two.
var ErrNoTestReason = errors.New("no reason decides this test")

// TestReasonCode names one sentence of the table of the reasons of a test (docs/contracts/runtime.md, "The reason in words"). The sentence is the text's: the code
// says which row decided, and the words belong to commandtext.
type TestReasonCode string

// The reason codes. The last two are the stored reasons that exclude a test.
const (
	TestReasonFailed          TestReasonCode = "failed"           // the head side failed, or did not finish
	TestReasonBuildError      TestReasonCode = "build-error"      // the head side did not build
	TestReasonNoResult        TestReasonCode = "no-result"        // the head side gave no result
	TestReasonNoBaseResult    TestReasonCode = "no-base-result"   // the head side has a result and the base side gave none
	TestReasonVaried          TestReasonCode = "varied"           // the runs of the head side differ
	TestReasonBaseVaried      TestReasonCode = "base-varied"      // the runs of the base side differ
	TestReasonNewAPI          TestReasonCode = "new-api"          // the head passes and the base could not build the test
	TestReasonInert           TestReasonCode = "inert"            // the head passes and the base passed or skipped itself
	TestReasonBuildConstraint TestReasonCode = "build-constraint" // a build rule leaves the test out
	TestReasonNestedModule    TestReasonCode = "nested-module"    // the test is under a go.mod of its own
)

// TestReason is the reason of a flagged test: the code of a sentence and, for the two codes that say how the runs differed, the runs of that side in run order.
type TestReason struct {
	Code TestReasonCode
	Runs []Observation
}

// Flagged says whether a canonical test has a reason in words: FAIL, MISSING and FLAKY.
func (s CanonicalState) Flagged() bool {
	return s == CanonicalFail || s == CanonicalMissing || s == CanonicalFlaky
}

// Flagged says whether a receipt test has a reason in words: FAILS, MISSING, FLAKY, NEW_API and INERT, every state that is neither a plain pass (PASS, DETECTS), a
// skip nor an exclusion.
func (s ReceiptState) Flagged() bool {
	return s == ReceiptFails || s == ReceiptMissing || s == ReceiptFlaky || s == ReceiptNewAPI || s == ReceiptInert
}

// CanonicalTestReasons derives the reasons of a test of a canonical run from its state, the runs of its head side in run order and, for an excluded test, its stored
// reason (build-constraint or nested-module). A test that is neither flagged nor excluded has none. A state that its runs contradict is ErrNoTestReason.
func CanonicalTestReasons(state CanonicalState, head []Observation, excluded string) ([]TestReason, error) {
	switch state {
	case CanonicalPass, CanonicalSkipped:
		return nil, nil
	case CanonicalExcluded:
		return excludedReasons(excluded)
	case CanonicalFail:
		return failureReasons(head)
	case CanonicalMissing:
		return missingReasons(head, nil)
	case CanonicalFlaky:
		return variedReasons(head, nil)
	}
	return nil, fmt.Errorf("%w: the state %d is none", ErrNoTestReason, state)
}

// ReceiptTestReasons derives the reasons of a test of a receipt from its state, the runs of its two sides in run order and, for an excluded test, its stored reason.
// A test that is neither flagged nor excluded has none; a FLAKY test whose two sides vary has two reasons, head first. A state that its runs contradict is
// ErrNoTestReason.
func ReceiptTestReasons(state ReceiptState, head, base []Observation, excluded string) ([]TestReason, error) {
	switch state {
	case ReceiptDetects, ReceiptSkipped:
		return nil, nil
	case ReceiptExcluded:
		return excludedReasons(excluded)
	case ReceiptFails:
		return failureReasons(head)
	case ReceiptMissing:
		return missingReasons(head, base)
	case ReceiptFlaky:
		return variedReasons(head, base)
	case ReceiptNewAPI:
		return sidesReasons(TestReasonNewAPI, result(head) == ObservedPass && result(base) == ObservedBuildError)
	case ReceiptInert:
		return sidesReasons(TestReasonInert, result(head) == ObservedPass && (result(base) == ObservedPass || result(base) == ObservedSkip))
	}
	return nil, fmt.Errorf("%w: the state %d is none", ErrNoTestReason, state)
}

// result is the observation of a side after the reruns: its last run, or none (0) for a side with no run.
func result(runs []Observation) Observation {
	if len(runs) == 0 {
		return 0
	}
	return runs[len(runs)-1]
}

// varies says that the runs of a side differ, which makes the test flaky on that side.
func varies(runs []Observation) bool {
	return slices.ContainsFunc(runs, func(o Observation) bool { return o != runs[0] })
}

// excludedReasons turns the stored reason of an excluded test into its code.
func excludedReasons(stored string) ([]TestReason, error) {
	if code := TestReasonCode(stored); code == TestReasonBuildConstraint || code == TestReasonNestedModule {
		return []TestReason{{Code: code}}, nil
	}
	return nil, fmt.Errorf("%w: the stored reason %q of an excluded test", ErrNoTestReason, stored)
}

// failureReasons decides a FAIL or FAILS test: its head side failed, or did not build.
func failureReasons(head []Observation) ([]TestReason, error) {
	switch result(head) {
	case ObservedFail:
		return []TestReason{{Code: TestReasonFailed}}, nil
	case ObservedBuildError:
		return []TestReason{{Code: TestReasonBuildError}}, nil
	}
	return nil, fmt.Errorf("%w: a failing test whose head side neither failed nor did not build", ErrNoTestReason)
}

// missingReasons decides a MISSING test: its head side gave no result, or it has one and the base side gave none (a receipt only; a canonical test has no base).
func missingReasons(head, base []Observation) ([]TestReason, error) {
	switch {
	case result(head) == ObservedAbsent:
		return []TestReason{{Code: TestReasonNoResult}}, nil
	case result(head) != 0 && result(base) == ObservedAbsent:
		return []TestReason{{Code: TestReasonNoBaseResult}}, nil
	}
	return nil, fmt.Errorf("%w: a missing test that gave a result on both sides", ErrNoTestReason)
}

// variedReasons decides a FLAKY test: the sides whose runs differ, head first. A canonical test has no base side.
func variedReasons(head, base []Observation) ([]TestReason, error) {
	var out []TestReason
	if varies(head) {
		out = append(out, TestReason{Code: TestReasonVaried, Runs: head})
	}
	if varies(base) {
		out = append(out, TestReason{Code: TestReasonBaseVaried, Runs: base})
	}
	if out == nil {
		return nil, fmt.Errorf("%w: a flaky test whose runs did not differ", ErrNoTestReason)
	}
	return out, nil
}

// sidesReasons gives the one reason of a state that its sides must show (NEW_API and INERT), and refuses sides that do not.
func sidesReasons(code TestReasonCode, shown bool) ([]TestReason, error) {
	if !shown {
		return nil, fmt.Errorf("%w: the sides of the test do not show %s", ErrNoTestReason, code)
	}
	return []TestReason{{Code: code}}, nil
}
