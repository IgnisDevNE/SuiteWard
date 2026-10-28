package execution_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The reason of a flagged test (docs/contracts/runtime.md, "The reason in words"): derived from the state and the stored runs, first row wins, and the codes that
// name how the runs differed carry those runs.

func TestFlagged(t *testing.T) {
	for state, want := range map[execution.CanonicalState]bool{
		execution.CanonicalFail: true, execution.CanonicalMissing: true, execution.CanonicalFlaky: true,
		execution.CanonicalPass: false, execution.CanonicalSkipped: false, execution.CanonicalExcluded: false, 0: false,
	} {
		if got := state.Flagged(); got != want {
			t.Errorf("canonical %q Flagged() = %v, want %v", state, got, want)
		}
	}
	for state, want := range map[execution.ReceiptState]bool{
		execution.ReceiptFails: true, execution.ReceiptMissing: true, execution.ReceiptFlaky: true, execution.ReceiptNewAPI: true, execution.ReceiptInert: true,
		execution.ReceiptDetects: false, execution.ReceiptSkipped: false, execution.ReceiptExcluded: false, 0: false,
	} {
		if got := state.Flagged(); got != want {
			t.Errorf("receipt %q Flagged() = %v, want %v", state, got, want)
		}
	}
}

func runs(observations ...execution.Observation) []execution.Observation { return observations }

func reasons(codes ...execution.TestReasonCode) []execution.TestReason {
	out := make([]execution.TestReason, len(codes))
	for i, code := range codes {
		out[i] = execution.TestReason{Code: code}
	}
	return out
}

func differing(code execution.TestReasonCode, observed ...execution.Observation) execution.TestReason {
	return execution.TestReason{Code: code, Runs: observed}
}

func TestReceiptTestReasons(t *testing.T) {
	tests := []struct {
		name       string
		state      execution.ReceiptState
		head, base []execution.Observation
		excluded   string
		want       []execution.TestReason
	}{
		{"FAILS, the head failed", execution.ReceiptFails, runs(fail), runs(pass), "", reasons(execution.TestReasonFailed)},
		{"FAILS, three failing runs", execution.ReceiptFails, runs(fail, fail, fail), runs(pass), "", reasons(execution.TestReasonFailed)},
		{"FAILS, the head did not build", execution.ReceiptFails, runs(buildError), runs(buildError), "", reasons(execution.TestReasonBuildError)},
		{"MISSING, the head gave no result", execution.ReceiptMissing, runs(absent), runs(pass), "", reasons(execution.TestReasonNoResult)},
		{"MISSING, the head ended with no result after a pass: not FLAKY", execution.ReceiptMissing, runs(pass, absent), runs(pass), "", reasons(execution.TestReasonNoResult)},
		{"MISSING, both sides gave none: the head first", execution.ReceiptMissing, runs(absent), runs(absent), "", reasons(execution.TestReasonNoResult)},
		{"MISSING, only the base gave none", execution.ReceiptMissing, runs(pass), runs(absent), "", reasons(execution.TestReasonNoBaseResult)},
		{"FLAKY, the head varies", execution.ReceiptFlaky, runs(pass, fail, pass), runs(pass), "", []execution.TestReason{differing(execution.TestReasonVaried, pass, fail, pass)}},
		{"FLAKY, the base varies", execution.ReceiptFlaky, runs(pass), runs(fail, pass), "", []execution.TestReason{differing(execution.TestReasonBaseVaried, fail, pass)}},
		{"FLAKY, both sides vary: two reasons, head first", execution.ReceiptFlaky, runs(pass, skip), runs(fail, pass), "",
			[]execution.TestReason{differing(execution.TestReasonVaried, pass, skip), differing(execution.TestReasonBaseVaried, fail, pass)}},
		{"NEW API", execution.ReceiptNewAPI, runs(pass), runs(buildError), "", reasons(execution.TestReasonNewAPI)},
		{"INERT, the base passed", execution.ReceiptInert, runs(pass), runs(pass), "", reasons(execution.TestReasonInert)},
		{"INERT, the base skipped itself", execution.ReceiptInert, runs(pass), runs(skip), "", reasons(execution.TestReasonInert)},
		{"EXCLUDED by a build rule", execution.ReceiptExcluded, nil, nil, "build-constraint", reasons(execution.TestReasonBuildConstraint)},
		{"EXCLUDED by a nested module", execution.ReceiptExcluded, nil, nil, "nested-module", reasons(execution.TestReasonNestedModule)},
		{"DETECTS has none", execution.ReceiptDetects, runs(pass), runs(fail), "", nil},
		{"SKIPPED has none", execution.ReceiptSkipped, runs(skip), runs(pass), "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := execution.ReceiptTestReasons(tc.state, tc.head, tc.base, tc.excluded)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ReceiptTestReasons = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestReceiptTestReasonsRefuseWhatNoRowDecides(t *testing.T) {
	tests := []struct {
		name       string
		state      execution.ReceiptState
		head, base []execution.Observation
		excluded   string
	}{
		{"FAILS with a head that passed", execution.ReceiptFails, runs(pass), runs(pass), ""},
		{"FAILS with no run", execution.ReceiptFails, nil, runs(pass), ""},
		{"MISSING with two sides that have results", execution.ReceiptMissing, runs(pass), runs(pass), ""},
		{"FLAKY with no side that varies", execution.ReceiptFlaky, runs(pass, pass), runs(fail), ""},
		{"INERT with a head that failed", execution.ReceiptInert, runs(fail), runs(pass), ""},
		{"INERT over a base that did not build", execution.ReceiptInert, runs(pass), runs(buildError), ""},
		{"NEW API over a base that passed", execution.ReceiptNewAPI, runs(pass), runs(pass), ""},
		{"EXCLUDED with no stored reason", execution.ReceiptExcluded, nil, nil, ""},
		{"EXCLUDED with a reason of no known kind", execution.ReceiptExcluded, nil, nil, "something-else"},
		{"a state that is none", 0, runs(pass), runs(pass), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := execution.ReceiptTestReasons(tc.state, tc.head, tc.base, tc.excluded); !errors.Is(err, execution.ErrNoTestReason) {
				t.Errorf("ReceiptTestReasons = %v, %v; want ErrNoTestReason", got, err)
			}
		})
	}
}

func TestCanonicalTestReasons(t *testing.T) {
	tests := []struct {
		name     string
		state    execution.CanonicalState
		head     []execution.Observation
		excluded string
		want     []execution.TestReason
	}{
		{"FAIL, the head failed", execution.CanonicalFail, runs(fail), "", reasons(execution.TestReasonFailed)},
		{"FAIL, the head did not build", execution.CanonicalFail, runs(buildError), "", reasons(execution.TestReasonBuildError)},
		{"MISSING", execution.CanonicalMissing, runs(absent), "", reasons(execution.TestReasonNoResult)},
		{"FLAKY", execution.CanonicalFlaky, runs(pass, fail), "", []execution.TestReason{differing(execution.TestReasonVaried, pass, fail)}},
		{"EXCLUDED by a build rule", execution.CanonicalExcluded, nil, "build-constraint", reasons(execution.TestReasonBuildConstraint)},
		{"EXCLUDED by a nested module", execution.CanonicalExcluded, nil, "nested-module", reasons(execution.TestReasonNestedModule)},
		{"PASS has none", execution.CanonicalPass, runs(pass), "", nil},
		{"SKIPPED has none", execution.CanonicalSkipped, runs(skip), "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := execution.CanonicalTestReasons(tc.state, tc.head, tc.excluded)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("CanonicalTestReasons = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestCanonicalTestReasonsRefuseWhatNoRowDecides(t *testing.T) {
	tests := []struct {
		name     string
		state    execution.CanonicalState
		head     []execution.Observation
		excluded string
	}{
		{"FAIL with a head that passed", execution.CanonicalFail, runs(pass), ""},
		{"MISSING with a result", execution.CanonicalMissing, runs(pass), ""},
		{"FLAKY whose runs did not vary", execution.CanonicalFlaky, runs(pass, pass), ""},
		{"FLAKY with no run", execution.CanonicalFlaky, nil, ""},
		{"EXCLUDED with no stored reason", execution.CanonicalExcluded, nil, ""},
		{"a state that is none", 0, runs(pass), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := execution.CanonicalTestReasons(tc.state, tc.head, tc.excluded); !errors.Is(err, execution.ErrNoTestReason) {
				t.Errorf("CanonicalTestReasons = %v, %v; want ErrNoTestReason", got, err)
			}
		})
	}
}
