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

// The run statuses. An unavailable run is never treated as a pass. RunNotApplicable is a receipt status only: no file
// matched by the canonical include differs from the canonical version the revision expects, so there is no receipt.
const (
	RunPending RunStatus = iota + 1
	RunComplete
	RunUnavailable
	RunNotApplicable
)

var runStatusNames = map[RunStatus]string{
	RunPending: "pending", RunComplete: "complete", RunUnavailable: "unavailable", RunNotApplicable: "not-applicable",
}

// ParseRunStatus reads pending, complete, unavailable or not-applicable, exactly as spelled.
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

// ApproveGate decides whether the receipt of the current head lets an approve proceed; the reason is none when it does.
//
//   - off and advisory never refuse.
//   - before-approval refuses only while the receipt is pending: an unavailable one does not refuse, whatever the tests say.
//   - required refuses unless the receipt is complete, the approve carries a mode, and the summary of that mode is met.
//   - A receipt that is not applicable, or complete with no evaluated test (empty, or nothing but EXCLUDED tests), never
//     refuses, and the approve needs no mode. A mode that is given must still be one.
//   - unblocked is the owner's /suiteward unblock for the head. It lifts an unavailable receipt under required: there is
//     nothing to judge, so the approve goes through with or without a mode. It never lifts a pending receipt, or a summary
//     that is not met.
//
// On an error the reason is none, so check the error first.
func (l ReceiptLevel) ApproveGate(status RunStatus, counts ReceiptCounts, mode Mode, unblocked bool) (Reason, error) {
	switch l {
	case ReceiptLevelOff, ReceiptLevelAdvisory:
		return ReasonNone, nil
	case ReceiptLevelBeforeApproval:
		return approveBeforeApproval(status)
	case ReceiptLevelRequired:
		return counts.approveRequired(status, mode, unblocked)
	}
	return ReasonNone, fmt.Errorf("%w: %d", ErrInvalidLevel, l)
}

func approveBeforeApproval(status RunStatus) (Reason, error) {
	switch status {
	case RunPending:
		return ReasonReceiptPending, nil
	case RunComplete, RunUnavailable, RunNotApplicable:
		return ReasonNone, nil
	}
	return ReasonNone, fmt.Errorf("%w: %d", ErrInvalidRunStatus, status)
}

func (c ReceiptCounts) approveRequired(status RunStatus, mode Mode, unblocked bool) (Reason, error) {
	switch status {
	case RunNotApplicable:
		return ReasonNone, nil
	case RunPending:
		return ReasonReceiptPending, nil
	case RunUnavailable:
		if !unblocked {
			return ReasonReceiptUnavailable, nil
		}
		return ReasonNone, checkMode(mode) // there is nothing to judge, so the approve needs no mode
	case RunComplete:
		if c.Evaluated() == 0 {
			return ReasonNone, checkMode(mode) // an empty receipt, or one of nothing but EXCLUDED tests, has nothing to judge either
		}
		if reason, err := modeGiven(mode); err != nil || reason != ReasonNone {
			return reason, err
		}
		summary, err := c.Summarize(mode)
		if err != nil || summary.Verdict == VerdictMet {
			return ReasonNone, err
		}
		return ReasonModeNotMet, nil
	}
	return ReasonNone, fmt.Errorf("%w: %d", ErrInvalidRunStatus, status)
}

// checkMode refuses a value that is neither ModeNone nor a mode.
func checkMode(mode Mode) error {
	if _, ok := modeNames[mode]; !ok && mode != ModeNone {
		return fmt.Errorf("%w: %d", ErrInvalidMode, mode)
	}
	return nil
}

// modeGiven is ReasonModeRequired when the approve carries no mode, and an error for a value that is not a mode.
func modeGiven(mode Mode) (Reason, error) {
	if mode == ModeNone {
		return ReasonModeRequired, nil
	}
	return ReasonNone, checkMode(mode)
}

// CheckGate decides whether the canonical run of the current head lets the Contract check pass; the reason is none when
// it may. off and advisory never hold it. required holds it unless the run is complete with no FAIL, MISSING or FLAKY test.
// unblocked is the owner's /suiteward unblock for the head: it lifts an unavailable run, and never a pending run or a failing
// test. RunNotApplicable is a receipt status, and an error here.
func (l CanonicalLevel) CheckGate(status RunStatus, counts CanonicalCounts, unblocked bool) (Reason, error) {
	switch l {
	case CanonicalLevelOff, CanonicalLevelAdvisory:
		return ReasonNone, nil
	case CanonicalLevelRequired:
		return counts.checkRequired(status, unblocked)
	}
	return ReasonNone, fmt.Errorf("%w: %d", ErrInvalidLevel, l)
}

func (c CanonicalCounts) checkRequired(status RunStatus, unblocked bool) (Reason, error) {
	switch status {
	case RunPending:
		return ReasonCanonicalPending, nil
	case RunUnavailable:
		if unblocked {
			return ReasonNone, nil
		}
		return ReasonCanonicalUnavailable, nil
	case RunComplete:
		if c.Blocking() > 0 {
			return ReasonCanonicalFailing, nil
		}
		return ReasonNone, nil
	}
	return ReasonNone, fmt.Errorf("%w: %d", ErrInvalidRunStatus, status)
}

// Conclusion is the conclusion of the SuiteWard / Receipt check.
type Conclusion uint8

// The conclusions.
const (
	ConclusionInProgress Conclusion = iota + 1
	ConclusionSuccess
	ConclusionFailure
	ConclusionNeutral
	ConclusionNone // not a GitHub value: no check is published, because the receipt does not apply
)

var conclusionNames = map[Conclusion]string{
	ConclusionInProgress: "in_progress",
	ConclusionSuccess:    "success",
	ConclusionFailure:    "failure",
	ConclusionNeutral:    "neutral",
}

// String returns the name GitHub gives the status or conclusion; ConclusionNone has none, so that no caller can send one to GitHub.
// It is empty for ConclusionNone and for a value that is not a conclusion.
func (c Conclusion) String() string { return conclusionNames[c] }

// ReceiptConclusion is the conclusion of the Receipt check: in progress while the receipt is pending, neutral when it is
// unavailable, and when it is complete a failure on any FAILS or MISSING test and a success otherwise. A receipt that is not
// applicable is ConclusionNone, a value and not an error because it is an ordinary outcome: the caller publishes no check.
// It takes no mode: the conclusion never depends on it.
func ReceiptConclusion(status RunStatus, counts ReceiptCounts) (Conclusion, error) {
	switch status {
	case RunPending:
		return ConclusionInProgress, nil
	case RunUnavailable:
		return ConclusionNeutral, nil
	case RunNotApplicable:
		return ConclusionNone, nil
	case RunComplete:
		if counts.Fails+counts.Missing > 0 {
			return ConclusionFailure, nil
		}
		return ConclusionSuccess, nil
	}
	return 0, fmt.Errorf("%w: %d", ErrInvalidRunStatus, status)
}
