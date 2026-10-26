package execution

import "errors"

var (
	// ErrInvalidMode reports a mode that is not fix, feature or refactor.
	ErrInvalidMode = errors.New("invalid mode")
	// ErrInvalidRunStatus reports a run status that is not pending, complete or unavailable.
	ErrInvalidRunStatus = errors.New("invalid run status")
)

// Mode is what the owner says a change is, given in the approve command and never read from candidate text.
type Mode uint8

// The modes. ModeNone is no mode given: the receipt then shows counts and no judgment.
const (
	ModeNone Mode = iota
	ModeFix
	ModeFeature
	ModeRefactor
)

// ParseMode reads fix, feature or refactor, exactly as spelled. An absent mode is ModeNone, which the caller decides.
func ParseMode(string) (Mode, error) { return 0, errNotImplemented }

// String returns the mode as the command spells it, or empty for ModeNone and for a value that is not a mode.
func (Mode) String() string { return "" }

// RunStatus is the status of one plan on one head.
type RunStatus uint8

// The run statuses. An unavailable run is never treated as a pass.
const (
	RunPending RunStatus = iota + 1
	RunComplete
	RunUnavailable
)

// ParseRunStatus reads pending, complete or unavailable, exactly as spelled.
func ParseRunStatus(string) (RunStatus, error) { return 0, errNotImplemented }

// String returns the stored name of the status, or empty for a value that is not one.
func (RunStatus) String() string { return "" }

// Reason is a stable code that says why a summary is not met or why a gate holds. The empty Reason is none.
type Reason string

// The reasons.
const (
	ReasonNone Reason = ""

	// Why a mode summary is not met.
	ReasonNoEvaluatedTests Reason = "no-evaluated-tests" // no test ran, so nothing detects the change
	ReasonBlockingTests    Reason = "blocking-tests"     // a test fails, is missing or is flaky
	ReasonNoDetection      Reason = "no-detection"       // no test detects anything
	ReasonNotInert         Reason = "not-inert"          // a refactor with a test that is not inert

	// Why the receipt gate refuses an approve.
	ReasonReceiptPending     Reason = "receipt-pending"
	ReasonReceiptUnavailable Reason = "receipt-unavailable"
	ReasonModeRequired       Reason = "mode-required"
	ReasonModeNotMet         Reason = "mode-not-met"

	// Why the canonical gate holds the Contract check.
	ReasonCanonicalPending     Reason = "canonical-pending"
	ReasonCanonicalUnavailable Reason = "canonical-unavailable"
	ReasonCanonicalFailing     Reason = "canonical-failing"
)

// Verdict is the outcome of a mode summary.
type Verdict uint8

// The verdicts. VerdictNotJudged is the summary of no mode: counts only.
const (
	VerdictNotJudged Verdict = iota + 1
	VerdictMet
	VerdictNotMet
)

// ModeSummary is the judgment of a receipt against a mode. Reason says why it is not met, and is none otherwise.
type ModeSummary struct {
	Verdict Verdict
	Reason  Reason
}

// Summarize judges the counts of a receipt against mode. EXCLUDED tests are left out: they neither detect nor block, and
// a receipt of nothing but EXCLUDED tests is an empty receipt.
func (ReceiptCounts) Summarize(Mode) (ModeSummary, error) { return ModeSummary{}, errNotImplemented }

// ApproveGate decides whether the receipt of the current head lets an approve proceed; the reason is none when it does.
// off and advisory never refuse. before-approval refuses unless the receipt is complete. required also refuses when the
// approve carries no mode, or its mode's summary is not met.
func (ReceiptLevel) ApproveGate(RunStatus, ReceiptCounts, Mode) (Reason, error) {
	return ReasonNone, errNotImplemented
}

// CheckGate decides whether the canonical run of the current head lets the Contract check pass; the reason is none when
// it may. off and advisory never hold it. required holds it unless the run is complete with no FAIL, MISSING or FLAKY test.
func (CanonicalLevel) CheckGate(RunStatus, CanonicalCounts) (Reason, error) {
	return ReasonNone, errNotImplemented
}

// Conclusion is the conclusion of the SuiteWard / Receipt check.
type Conclusion uint8

// The conclusions.
const (
	ConclusionInProgress Conclusion = iota + 1
	ConclusionSuccess
	ConclusionFailure
	ConclusionNeutral
)

// String returns the name GitHub gives the status or conclusion, or empty for a value that is not one.
func (Conclusion) String() string { return "" }

// ReceiptConclusion is the conclusion of the Receipt check: in progress while the receipt is pending, neutral when it is
// unavailable, and when it is complete a failure on any FAILS or MISSING test and a success otherwise. It takes no mode:
// the conclusion never depends on it.
func ReceiptConclusion(RunStatus, ReceiptCounts) (Conclusion, error) { return 0, errNotImplemented }
