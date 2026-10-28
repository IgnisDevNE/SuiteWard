package execution

import "errors"

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
func (s CanonicalState) Flagged() bool { return false }

// Flagged says whether a receipt test has a reason in words: FAILS, MISSING, FLAKY, NEW_API and INERT, every state that is neither a plain pass (PASS, DETECTS), a
// skip nor an exclusion.
func (s ReceiptState) Flagged() bool { return false }

// CanonicalTestReasons derives the reasons of a test of a canonical run from its state, the runs of its head side in run order and, for an excluded test, its stored
// reason (build-constraint or nested-module). A test that is neither flagged nor excluded has none. A state that its runs contradict is ErrNoTestReason.
func CanonicalTestReasons(state CanonicalState, head []Observation, excluded string) ([]TestReason, error) {
	return nil, nil
}

// ReceiptTestReasons derives the reasons of a test of a receipt from its state, the runs of its two sides in run order and, for an excluded test, its stored reason.
// A test that is neither flagged nor excluded has none; a FLAKY test whose two sides vary has two reasons, head first. A state that its runs contradict is
// ErrNoTestReason.
func ReceiptTestReasons(state ReceiptState, head, base []Observation, excluded string) ([]TestReason, error) {
	return nil, nil
}
