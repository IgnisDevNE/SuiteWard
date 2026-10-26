package execution

import (
	"errors"
	"fmt"
)

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

var modeNames = map[Mode]string{ModeFix: "fix", ModeFeature: "feature", ModeRefactor: "refactor"}

// ParseMode reads fix, feature or refactor, exactly as spelled. An absent mode is ModeNone, which the caller decides.
func ParseMode(s string) (Mode, error) { return parseEnum(modeNames, s, ErrInvalidMode) }

// String returns the mode as the command spells it, or empty for ModeNone and for a value that is not a mode.
func (m Mode) String() string { return modeNames[m] }

// RunStatus is the status of one plan on one head.
type RunStatus uint8

// The run statuses. An unavailable run is never treated as a pass.
const (
	RunPending RunStatus = iota + 1
	RunComplete
	RunUnavailable
)

var runStatusNames = map[RunStatus]string{RunPending: "pending", RunComplete: "complete", RunUnavailable: "unavailable"}

// ParseRunStatus reads pending, complete or unavailable, exactly as spelled.
func ParseRunStatus(s string) (RunStatus, error) {
	return parseEnum(runStatusNames, s, ErrInvalidRunStatus)
}

// String returns the stored name of the status, or empty for a value that is not one.
func (s RunStatus) String() string { return runStatusNames[s] }

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

func notMet(reason Reason) ModeSummary { return ModeSummary{Verdict: VerdictNotMet, Reason: reason} }

// Summarize judges the counts of a receipt against mode. EXCLUDED tests are left out: they neither detect nor block, and
// a receipt of nothing but EXCLUDED tests is an empty receipt.
//
//   - fix is met by at least one DETECTS and no FAILS, MISSING or FLAKY;
//   - feature is met by at least one DETECTS or NEW_API and no FAILS, MISSING or FLAKY;
//   - refactor is met when every evaluated test is INERT;
//   - an empty receipt meets refactor and neither of the other two.
func (c ReceiptCounts) Summarize(mode Mode) (ModeSummary, error) {
	switch mode {
	case ModeNone:
		return ModeSummary{Verdict: VerdictNotJudged}, nil
	case ModeFix:
		return c.judgeDetection(c.Detects), nil
	case ModeFeature:
		return c.judgeDetection(c.Detects + c.NewAPI), nil
	case ModeRefactor:
		return c.judgeRefactor(), nil
	}
	return ModeSummary{}, fmt.Errorf("%w: %d", ErrInvalidMode, mode)
}

func (c ReceiptCounts) judgeDetection(detections int) ModeSummary {
	switch {
	case c.Evaluated() == 0:
		return notMet(ReasonNoEvaluatedTests)
	case c.Blocking() > 0:
		return notMet(ReasonBlockingTests)
	case detections == 0:
		return notMet(ReasonNoDetection)
	}
	return ModeSummary{Verdict: VerdictMet}
}

func (c ReceiptCounts) judgeRefactor() ModeSummary {
	switch {
	case c.Blocking() > 0:
		return notMet(ReasonBlockingTests)
	case c.Inert != c.Evaluated():
		return notMet(ReasonNotInert)
	}
	return ModeSummary{Verdict: VerdictMet}
}

// settled is none when the run is complete, and otherwise the reason its status gives; a value that is not a status is an error.
func settled(status RunStatus, pending, unavailable Reason) (Reason, error) {
	switch status {
	case RunComplete:
		return ReasonNone, nil
	case RunPending:
		return pending, nil
	case RunUnavailable:
		return unavailable, nil
	}
	return ReasonNone, fmt.Errorf("%w: %d", ErrInvalidRunStatus, status)
}

// ApproveGate decides whether the receipt of the current head lets an approve proceed; the reason is none when it does.
// off and advisory never refuse. before-approval refuses unless the receipt is complete. required also refuses when the
// approve carries no mode, or the summary of its mode is not met.
func (l ReceiptLevel) ApproveGate(status RunStatus, counts ReceiptCounts, mode Mode) (Reason, error) {
	switch l {
	case ReceiptLevelOff, ReceiptLevelAdvisory:
		return ReasonNone, nil
	case ReceiptLevelBeforeApproval:
		return settled(status, ReasonReceiptPending, ReasonReceiptUnavailable)
	case ReceiptLevelRequired:
		if reason, err := settled(status, ReasonReceiptPending, ReasonReceiptUnavailable); err != nil || reason != ReasonNone {
			return reason, err
		}
		return counts.judgeApprove(mode)
	}
	return ReasonNone, fmt.Errorf("%w: %d", ErrInvalidLevel, l)
}

func (c ReceiptCounts) judgeApprove(mode Mode) (Reason, error) {
	if mode == ModeNone {
		return ReasonModeRequired, nil
	}
	summary, err := c.Summarize(mode)
	if err != nil {
		return ReasonNone, err
	}
	if summary.Verdict != VerdictMet {
		return ReasonModeNotMet, nil
	}
	return ReasonNone, nil
}

// CheckGate decides whether the canonical run of the current head lets the Contract check pass; the reason is none when
// it may. off and advisory never hold it. required holds it unless the run is complete with no FAIL, MISSING or FLAKY test.
func (l CanonicalLevel) CheckGate(status RunStatus, counts CanonicalCounts) (Reason, error) {
	switch l {
	case CanonicalLevelOff, CanonicalLevelAdvisory:
		return ReasonNone, nil
	case CanonicalLevelRequired:
		if reason, err := settled(status, ReasonCanonicalPending, ReasonCanonicalUnavailable); err != nil || reason != ReasonNone {
			return reason, err
		}
		if counts.Blocking() > 0 {
			return ReasonCanonicalFailing, nil
		}
		return ReasonNone, nil
	}
	return ReasonNone, fmt.Errorf("%w: %d", ErrInvalidLevel, l)
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

var conclusionNames = map[Conclusion]string{
	ConclusionInProgress: "in_progress",
	ConclusionSuccess:    "success",
	ConclusionFailure:    "failure",
	ConclusionNeutral:    "neutral",
}

// String returns the name GitHub gives the status or conclusion, or empty for a value that is not one.
func (c Conclusion) String() string { return conclusionNames[c] }

// ReceiptConclusion is the conclusion of the Receipt check: in progress while the receipt is pending, neutral when it is
// unavailable, and when it is complete a failure on any FAILS or MISSING test and a success otherwise. It takes no mode:
// the conclusion never depends on it.
func ReceiptConclusion(status RunStatus, counts ReceiptCounts) (Conclusion, error) {
	switch status {
	case RunPending:
		return ConclusionInProgress, nil
	case RunUnavailable:
		return ConclusionNeutral, nil
	case RunComplete:
		if counts.Fails+counts.Missing > 0 {
			return ConclusionFailure, nil
		}
		return ConclusionSuccess, nil
	}
	return 0, fmt.Errorf("%w: %d", ErrInvalidRunStatus, status)
}
