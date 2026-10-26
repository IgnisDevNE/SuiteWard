package execution_test

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

func TestParseMode(t *testing.T) {
	for text, want := range map[string]execution.Mode{"fix": execution.ModeFix, "feature": execution.ModeFeature, "refactor": execution.ModeRefactor} {
		got, err := execution.ParseMode(text)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = %v, %v; want %v", text, got, err, want)
		}
		if got.String() != text {
			t.Errorf("%v.String() = %q, want %q", got, got.String(), text)
		}
	}
	// An absent mode is the caller's to decide; none and the empty string are not modes.
	for _, text := range []string{"", "none", "Fix", "FIX", "features", " refactor", "fix\n", "bugfix"} {
		if got, err := execution.ParseMode(text); !errors.Is(err, execution.ErrInvalidMode) {
			t.Errorf("ParseMode(%q) = %v, %v; want ErrInvalidMode", text, got, err)
		}
	}
	if got := execution.ModeNone.String(); got != "" {
		t.Errorf("ModeNone.String() = %q, want empty", got)
	}
}

func TestParseRunStatus(t *testing.T) {
	for text, want := range map[string]execution.RunStatus{"pending": execution.RunPending, "complete": execution.RunComplete, "unavailable": execution.RunUnavailable, "not-applicable": execution.RunNotApplicable} {
		got, err := execution.ParseRunStatus(text)
		if err != nil || got != want {
			t.Errorf("ParseRunStatus(%q) = %v, %v; want %v", text, got, err, want)
		}
		if got.String() != text {
			t.Errorf("%v.String() = %q, want %q", got, got.String(), text)
		}
	}
	for _, text := range []string{"", "completed", "Complete", "running", "failed"} {
		if got, err := execution.ParseRunStatus(text); !errors.Is(err, execution.ErrInvalidRunStatus) {
			t.Errorf("ParseRunStatus(%q) = %v, %v; want ErrInvalidRunStatus", text, got, err)
		}
	}
}

func TestSummarize(t *testing.T) {
	met := execution.ModeSummary{Verdict: execution.VerdictMet}
	notMet := func(reason execution.Reason) execution.ModeSummary {
		return execution.ModeSummary{Verdict: execution.VerdictNotMet, Reason: reason}
	}
	tests := []struct {
		name   string
		mode   execution.Mode
		counts execution.ReceiptCounts
		want   execution.ModeSummary
	}{
		// fix: at least one DETECTS and no FAILS, MISSING or FLAKY.
		{"fix with a detecting test", execution.ModeFix, execution.ReceiptCounts{Detects: 1, Inert: 2}, met},
		{"fix tolerates NEW API and skipped tests beside a detecting one", execution.ModeFix, execution.ReceiptCounts{Detects: 1, NewAPI: 1, Skipped: 1}, met},
		{"fix: NEW API is not a detection", execution.ModeFix, execution.ReceiptCounts{NewAPI: 2}, notMet(execution.ReasonNoDetection)},
		{"fix with only inert tests", execution.ModeFix, execution.ReceiptCounts{Inert: 3}, notMet(execution.ReasonNoDetection)},
		{"fix with only skipped tests", execution.ModeFix, execution.ReceiptCounts{Skipped: 1}, notMet(execution.ReasonNoDetection)},
		{"fix blocked by a failure", execution.ModeFix, execution.ReceiptCounts{Detects: 1, Fails: 1}, notMet(execution.ReasonBlockingTests)},
		{"fix blocked by a missing test", execution.ModeFix, execution.ReceiptCounts{Detects: 1, Missing: 1}, notMet(execution.ReasonBlockingTests)},
		{"fix blocked by a flaky test", execution.ModeFix, execution.ReceiptCounts{Detects: 1, Flaky: 1}, notMet(execution.ReasonBlockingTests)},
		{"fix with an empty receipt", execution.ModeFix, execution.ReceiptCounts{}, notMet(execution.ReasonNoEvaluatedTests)},
		{"fix leaves EXCLUDED tests out: an all-EXCLUDED receipt is empty", execution.ModeFix, execution.ReceiptCounts{Excluded: 4}, notMet(execution.ReasonNoEvaluatedTests)},
		{"fix is met whatever the EXCLUDED count", execution.ModeFix, execution.ReceiptCounts{Detects: 1, Excluded: 4}, met},

		// feature: no FAILS, MISSING or FLAKY, and at least one DETECTS or NEW_API.
		{"feature with a detecting test", execution.ModeFeature, execution.ReceiptCounts{Detects: 1}, met},
		{"feature with a NEW API test", execution.ModeFeature, execution.ReceiptCounts{NewAPI: 1, Inert: 1}, met},
		{"feature with a skipped test and a NEW API one", execution.ModeFeature, execution.ReceiptCounts{NewAPI: 1, Skipped: 2}, met},
		{"feature with only inert tests: no test detects anything", execution.ModeFeature, execution.ReceiptCounts{Inert: 5}, notMet(execution.ReasonNoDetection)},
		{"feature blocked by a failure", execution.ModeFeature, execution.ReceiptCounts{NewAPI: 1, Fails: 1}, notMet(execution.ReasonBlockingTests)},
		{"feature blocked by a flaky test", execution.ModeFeature, execution.ReceiptCounts{Detects: 1, Flaky: 1}, notMet(execution.ReasonBlockingTests)},
		{"feature with an empty receipt", execution.ModeFeature, execution.ReceiptCounts{}, notMet(execution.ReasonNoEvaluatedTests)},
		{"feature with only EXCLUDED tests", execution.ModeFeature, execution.ReceiptCounts{Excluded: 1}, notMet(execution.ReasonNoEvaluatedTests)},

		// refactor: every evaluated test INERT and none FAILS, MISSING or FLAKY.
		{"refactor with only inert tests", execution.ModeRefactor, execution.ReceiptCounts{Inert: 3}, met},
		{"refactor with an empty receipt", execution.ModeRefactor, execution.ReceiptCounts{}, met},
		{"refactor with only EXCLUDED tests", execution.ModeRefactor, execution.ReceiptCounts{Excluded: 2}, met},
		{"refactor ignores EXCLUDED tests beside inert ones", execution.ModeRefactor, execution.ReceiptCounts{Inert: 1, Excluded: 2}, met},
		{"refactor with a detecting test", execution.ModeRefactor, execution.ReceiptCounts{Inert: 1, Detects: 1}, notMet(execution.ReasonNotInert)},
		{"refactor with a NEW API test", execution.ModeRefactor, execution.ReceiptCounts{Inert: 1, NewAPI: 1}, notMet(execution.ReasonNotInert)},
		{"refactor with a skipped test", execution.ModeRefactor, execution.ReceiptCounts{Inert: 1, Skipped: 1}, notMet(execution.ReasonNotInert)},
		{"refactor blocked by a failure", execution.ModeRefactor, execution.ReceiptCounts{Inert: 1, Fails: 1}, notMet(execution.ReasonBlockingTests)},
		{"refactor blocked by a missing test", execution.ModeRefactor, execution.ReceiptCounts{Inert: 1, Missing: 1}, notMet(execution.ReasonBlockingTests)},
		{"refactor blocked by a flaky test", execution.ModeRefactor, execution.ReceiptCounts{Inert: 1, Flaky: 1}, notMet(execution.ReasonBlockingTests)},

		// no mode: counts only, no judgment.
		{"no mode over an empty receipt", execution.ModeNone, execution.ReceiptCounts{}, execution.ModeSummary{Verdict: execution.VerdictNotJudged}},
		{"no mode over failures", execution.ModeNone, execution.ReceiptCounts{Fails: 2, Missing: 1}, execution.ModeSummary{Verdict: execution.VerdictNotJudged}},
		{"no mode over detections", execution.ModeNone, execution.ReceiptCounts{Detects: 2}, execution.ModeSummary{Verdict: execution.VerdictNotJudged}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.counts.Summarize(tt.mode)
			if err != nil || got != tt.want {
				t.Errorf("Summarize(%v) over %+v = %+v, %v; want %+v", tt.mode, tt.counts, got, err, tt.want)
			}
		})
	}
	if got, err := (execution.ReceiptCounts{Detects: 1}).Summarize(execution.Mode(9)); !errors.Is(err, execution.ErrInvalidMode) {
		t.Errorf("Summarize with an invalid mode = %+v, %v; want ErrInvalidMode", got, err)
	}
}

func TestReceiptApproveGate(t *testing.T) {
	statuses := []execution.RunStatus{execution.RunPending, execution.RunUnavailable, execution.RunComplete, execution.RunNotApplicable}
	failing := execution.ReceiptCounts{Fails: 1, Missing: 1, Flaky: 1}

	// off and advisory never refuse: not on a pending, unavailable or failing receipt, and not without a mode.
	for _, level := range []execution.ReceiptLevel{execution.ReceiptLevelOff, execution.ReceiptLevelAdvisory} {
		for _, status := range statuses {
			for _, mode := range []execution.Mode{execution.ModeNone, execution.ModeFix, execution.ModeRefactor} {
				for _, unblocked := range []bool{false, true} {
					if got, err := level.ApproveGate(status, failing, mode, unblocked); err != nil || got != execution.ReasonNone {
						t.Errorf("%v gate, %v receipt, mode %v, unblocked %v = %q, %v; want no refusal", level, status, mode, unblocked, got, err)
					}
				}
			}
		}
	}

	tests := []struct {
		name   string
		level  execution.ReceiptLevel
		status execution.RunStatus
		counts execution.ReceiptCounts
		mode   execution.Mode
		want   execution.Reason
	}{
		// before-approval: only a pending receipt refuses; the rest is whatever it says and whatever the mode.
		{"before-approval, pending", execution.ReceiptLevelBeforeApproval, execution.RunPending, execution.ReceiptCounts{}, execution.ModeNone, execution.ReasonReceiptPending},
		{"before-approval, unavailable does not refuse", execution.ReceiptLevelBeforeApproval, execution.RunUnavailable, execution.ReceiptCounts{}, execution.ModeNone, execution.ReasonNone},
		{"before-approval, not applicable", execution.ReceiptLevelBeforeApproval, execution.RunNotApplicable, execution.ReceiptCounts{}, execution.ModeNone, execution.ReasonNone},
		{"before-approval, complete and failing, no mode", execution.ReceiptLevelBeforeApproval, execution.RunComplete, failing, execution.ModeNone, execution.ReasonNone},
		{"before-approval, complete and empty", execution.ReceiptLevelBeforeApproval, execution.RunComplete, execution.ReceiptCounts{}, execution.ModeFix, execution.ReasonNone},

		// required: complete, a mode, and that mode's summary met.
		{"required, pending", execution.ReceiptLevelRequired, execution.RunPending, execution.ReceiptCounts{Detects: 1}, execution.ModeFix, execution.ReasonReceiptPending},
		{"required, unavailable", execution.ReceiptLevelRequired, execution.RunUnavailable, execution.ReceiptCounts{Detects: 1}, execution.ModeFix, execution.ReasonReceiptUnavailable},
		{"required, pending and no mode: the receipt state comes first", execution.ReceiptLevelRequired, execution.RunPending, execution.ReceiptCounts{}, execution.ModeNone, execution.ReasonReceiptPending},
		{"required, complete but no mode", execution.ReceiptLevelRequired, execution.RunComplete, execution.ReceiptCounts{Detects: 1}, execution.ModeNone, execution.ReasonModeRequired},
		{"required, complete and empty, no mode", execution.ReceiptLevelRequired, execution.RunComplete, execution.ReceiptCounts{}, execution.ModeNone, execution.ReasonModeRequired},
		{"required, fix met", execution.ReceiptLevelRequired, execution.RunComplete, execution.ReceiptCounts{Detects: 1, Inert: 1}, execution.ModeFix, execution.ReasonNone},
		{"required, fix not met", execution.ReceiptLevelRequired, execution.RunComplete, execution.ReceiptCounts{Inert: 1}, execution.ModeFix, execution.ReasonModeNotMet},
		{"required, fix blocked by a failure", execution.ReceiptLevelRequired, execution.RunComplete, execution.ReceiptCounts{Detects: 1, Fails: 1}, execution.ModeFix, execution.ReasonModeNotMet},
		{"required, feature met by NEW API", execution.ReceiptLevelRequired, execution.RunComplete, execution.ReceiptCounts{NewAPI: 1}, execution.ModeFeature, execution.ReasonNone},
		{"required, refactor met", execution.ReceiptLevelRequired, execution.RunComplete, execution.ReceiptCounts{Inert: 2}, execution.ModeRefactor, execution.ReasonNone},
		{"required, refactor met by an empty receipt", execution.ReceiptLevelRequired, execution.RunComplete, execution.ReceiptCounts{}, execution.ModeRefactor, execution.ReasonNone},
		{"required, refactor not met by a detection", execution.ReceiptLevelRequired, execution.RunComplete, execution.ReceiptCounts{Inert: 1, Detects: 1}, execution.ModeRefactor, execution.ReasonModeNotMet},
		{"required, fix over an empty receipt", execution.ReceiptLevelRequired, execution.RunComplete, execution.ReceiptCounts{}, execution.ModeFix, execution.ReasonModeNotMet},

		// not applicable: no evaluated file, so no receipt, and the approve needs no mode.
		{"required, not applicable and no mode", execution.ReceiptLevelRequired, execution.RunNotApplicable, execution.ReceiptCounts{}, execution.ModeNone, execution.ReasonNone},
		{"required, not applicable with a mode", execution.ReceiptLevelRequired, execution.RunNotApplicable, execution.ReceiptCounts{}, execution.ModeFix, execution.ReasonNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.level.ApproveGate(tt.status, tt.counts, tt.mode, false)
			if err != nil || got != tt.want {
				t.Errorf("ApproveGate = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

// The owner's /suiteward unblock lifts an unavailable receipt for the current head and nothing else.
func TestReceiptApproveGateUnblocked(t *testing.T) {
	failing := execution.ReceiptCounts{Detects: 1, Fails: 1}
	tests := []struct {
		name   string
		level  execution.ReceiptLevel
		status execution.RunStatus
		counts execution.ReceiptCounts
		mode   execution.Mode
		want   execution.Reason
	}{
		{"required, unavailable and unblocked, with a mode", execution.ReceiptLevelRequired, execution.RunUnavailable, execution.ReceiptCounts{}, execution.ModeFix, execution.ReasonNone},
		{"required, unavailable and unblocked, whatever the mode: nothing to judge", execution.ReceiptLevelRequired, execution.RunUnavailable, execution.ReceiptCounts{}, execution.ModeRefactor, execution.ReasonNone},
		{"required, unavailable and unblocked, still needs a mode", execution.ReceiptLevelRequired, execution.RunUnavailable, execution.ReceiptCounts{}, execution.ModeNone, execution.ReasonModeRequired},
		{"before-approval, unavailable and unblocked", execution.ReceiptLevelBeforeApproval, execution.RunUnavailable, execution.ReceiptCounts{}, execution.ModeNone, execution.ReasonNone},
		// It never lifts a pending run, or failing tests.
		{"required, pending and unblocked", execution.ReceiptLevelRequired, execution.RunPending, execution.ReceiptCounts{}, execution.ModeFix, execution.ReasonReceiptPending},
		{"before-approval, pending and unblocked", execution.ReceiptLevelBeforeApproval, execution.RunPending, execution.ReceiptCounts{}, execution.ModeNone, execution.ReasonReceiptPending},
		{"required, complete with a failing test, unblocked", execution.ReceiptLevelRequired, execution.RunComplete, failing, execution.ModeFix, execution.ReasonModeNotMet},
		{"required, complete without a mode, unblocked", execution.ReceiptLevelRequired, execution.RunComplete, execution.ReceiptCounts{Detects: 1}, execution.ModeNone, execution.ReasonModeRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.level.ApproveGate(tt.status, tt.counts, tt.mode, true)
			if err != nil || got != tt.want {
				t.Errorf("ApproveGate = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	// An unblock does not excuse an invalid mode either.
	if got, err := execution.ReceiptLevelRequired.ApproveGate(execution.RunUnavailable, execution.ReceiptCounts{}, execution.Mode(9), true); !errors.Is(err, execution.ErrInvalidMode) {
		t.Errorf("unblocked with an invalid mode = %q, %v; want ErrInvalidMode", got, err)
	}
}

func TestReceiptApproveGateRefusesInvalidInput(t *testing.T) {
	if got, err := execution.ReceiptLevel(0).ApproveGate(execution.RunComplete, execution.ReceiptCounts{}, execution.ModeFix, false); !errors.Is(err, execution.ErrInvalidLevel) {
		t.Errorf("invalid level = %q, %v; want ErrInvalidLevel", got, err)
	}
	for _, level := range []execution.ReceiptLevel{execution.ReceiptLevelBeforeApproval, execution.ReceiptLevelRequired} {
		if got, err := level.ApproveGate(execution.RunStatus(0), execution.ReceiptCounts{}, execution.ModeFix, false); !errors.Is(err, execution.ErrInvalidRunStatus) {
			t.Errorf("%v with an invalid status = %q, %v; want ErrInvalidRunStatus", level, got, err)
		}
	}
	if got, err := execution.ReceiptLevelRequired.ApproveGate(execution.RunComplete, execution.ReceiptCounts{}, execution.Mode(9), false); !errors.Is(err, execution.ErrInvalidMode) {
		t.Errorf("required with an invalid mode = %q, %v; want ErrInvalidMode", got, err)
	}
}

func TestCanonicalCheckGate(t *testing.T) {
	healthy := execution.CanonicalCounts{Pass: 5, Skipped: 2, Excluded: 3}
	tests := []struct {
		name   string
		level  execution.CanonicalLevel
		status execution.RunStatus
		counts execution.CanonicalCounts
		want   execution.Reason
	}{
		// off and advisory never change the check.
		{"off, pending", execution.CanonicalLevelOff, execution.RunPending, execution.CanonicalCounts{}, execution.ReasonNone},
		{"off, failing", execution.CanonicalLevelOff, execution.RunComplete, execution.CanonicalCounts{Fail: 1}, execution.ReasonNone},
		{"advisory, unavailable", execution.CanonicalLevelAdvisory, execution.RunUnavailable, execution.CanonicalCounts{}, execution.ReasonNone},
		{"advisory, failing", execution.CanonicalLevelAdvisory, execution.RunComplete, execution.CanonicalCounts{Fail: 1, Missing: 1, Flaky: 1}, execution.ReasonNone},

		// required: complete with no FAIL, MISSING or FLAKY; PASS, SKIPPED and EXCLUDED are fine.
		{"required, complete and healthy", execution.CanonicalLevelRequired, execution.RunComplete, healthy, execution.ReasonNone},
		{"required, complete and empty", execution.CanonicalLevelRequired, execution.RunComplete, execution.CanonicalCounts{}, execution.ReasonNone},
		{"required, complete with only EXCLUDED tests", execution.CanonicalLevelRequired, execution.RunComplete, execution.CanonicalCounts{Excluded: 2}, execution.ReasonNone},
		{"required, pending", execution.CanonicalLevelRequired, execution.RunPending, healthy, execution.ReasonCanonicalPending},
		{"required, unavailable", execution.CanonicalLevelRequired, execution.RunUnavailable, healthy, execution.ReasonCanonicalUnavailable},
		{"required, a failure", execution.CanonicalLevelRequired, execution.RunComplete, execution.CanonicalCounts{Pass: 5, Fail: 1}, execution.ReasonCanonicalFailing},
		{"required, a missing test", execution.CanonicalLevelRequired, execution.RunComplete, execution.CanonicalCounts{Pass: 5, Missing: 1}, execution.ReasonCanonicalFailing},
		{"required, a flaky test", execution.CanonicalLevelRequired, execution.RunComplete, execution.CanonicalCounts{Pass: 5, Flaky: 1}, execution.ReasonCanonicalFailing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.level.CheckGate(tt.status, tt.counts, false)
			if err != nil || got != tt.want {
				t.Errorf("CheckGate = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	if got, err := execution.CanonicalLevel(0).CheckGate(execution.RunComplete, healthy, false); !errors.Is(err, execution.ErrInvalidLevel) {
		t.Errorf("invalid level = %q, %v; want ErrInvalidLevel", got, err)
	}
	if got, err := execution.CanonicalLevelRequired.CheckGate(execution.RunStatus(7), healthy, false); !errors.Is(err, execution.ErrInvalidRunStatus) {
		t.Errorf("invalid status = %q, %v; want ErrInvalidRunStatus", got, err)
	}
}

// The owner's /suiteward unblock lifts an unavailable canonical run for the current head and nothing else.
func TestCanonicalCheckGateUnblocked(t *testing.T) {
	healthy := execution.CanonicalCounts{Pass: 5}
	failing := execution.CanonicalCounts{Pass: 5, Fail: 1}
	tests := []struct {
		name   string
		status execution.RunStatus
		counts execution.CanonicalCounts
		want   execution.Reason
	}{
		{"unavailable and unblocked", execution.RunUnavailable, execution.CanonicalCounts{}, execution.ReasonNone},
		{"pending and unblocked", execution.RunPending, healthy, execution.ReasonCanonicalPending},
		{"complete with a failure, unblocked", execution.RunComplete, failing, execution.ReasonCanonicalFailing},
		{"complete and healthy, unblocked", execution.RunComplete, healthy, execution.ReasonNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := execution.CanonicalLevelRequired.CheckGate(tt.status, tt.counts, true)
			if err != nil || got != tt.want {
				t.Errorf("CheckGate = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	// There is no canonical run that does not apply: a repository with no declared test completes with zero tests.
	if got, err := execution.CanonicalLevelRequired.CheckGate(execution.RunNotApplicable, healthy, false); !errors.Is(err, execution.ErrInvalidRunStatus) {
		t.Errorf("a not-applicable canonical run = %q, %v; want ErrInvalidRunStatus", got, err)
	}
}

func TestReceiptConclusion(t *testing.T) {
	tests := []struct {
		name   string
		status execution.RunStatus
		counts execution.ReceiptCounts
		want   execution.Conclusion
		text   string
	}{
		{"pending", execution.RunPending, execution.ReceiptCounts{Fails: 3}, execution.ConclusionInProgress, "in_progress"},
		{"unavailable, whatever the counts", execution.RunUnavailable, execution.ReceiptCounts{Fails: 3}, execution.ConclusionNeutral, "neutral"},
		// Not applicable is no check at all: the caller publishes nothing, so it must not send this name to GitHub.
		{"not applicable, whatever the counts", execution.RunNotApplicable, execution.ReceiptCounts{Fails: 3}, execution.ConclusionNone, "none"},
		{"a failing test", execution.RunComplete, execution.ReceiptCounts{Fails: 1, Detects: 4}, execution.ConclusionFailure, "failure"},
		{"a missing test", execution.RunComplete, execution.ReceiptCounts{Missing: 1, Detects: 4}, execution.ConclusionFailure, "failure"},
		{"detections", execution.RunComplete, execution.ReceiptCounts{Detects: 2}, execution.ConclusionSuccess, "success"},
		{"inert tests", execution.RunComplete, execution.ReceiptCounts{Inert: 2}, execution.ConclusionSuccess, "success"},
		{"flaky tests only", execution.RunComplete, execution.ReceiptCounts{Flaky: 2}, execution.ConclusionSuccess, "success"},
		{"skipped tests only", execution.RunComplete, execution.ReceiptCounts{Skipped: 2}, execution.ConclusionSuccess, "success"},
		{"NEW API tests only", execution.RunComplete, execution.ReceiptCounts{NewAPI: 2}, execution.ConclusionSuccess, "success"},
		{"EXCLUDED tests only", execution.RunComplete, execution.ReceiptCounts{Excluded: 2}, execution.ConclusionSuccess, "success"},
		{"an empty receipt", execution.RunComplete, execution.ReceiptCounts{}, execution.ConclusionSuccess, "success"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := execution.ReceiptConclusion(tt.status, tt.counts)
			if err != nil || got != tt.want || got.String() != tt.text {
				t.Errorf("ReceiptConclusion = %v (%q), %v; want %v (%q)", got, got.String(), err, tt.want, tt.text)
			}
		})
	}
	if got, err := execution.ReceiptConclusion(execution.RunStatus(0), execution.ReceiptCounts{}); !errors.Is(err, execution.ErrInvalidRunStatus) {
		t.Errorf("invalid status = %v, %v; want ErrInvalidRunStatus", got, err)
	}
	if got := execution.Conclusion(0).String(); got != "" {
		t.Errorf("a value that is not a conclusion has the name %q, want none", got)
	}
}
