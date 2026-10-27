package execution_test

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

const (
	pass       = execution.ObservedPass
	fail       = execution.ObservedFail
	buildError = execution.ObservedBuildError
	skip       = execution.ObservedSkip
	absent     = execution.ObservedAbsent
	excluded   = execution.ObservedExcluded
)

func steady(o execution.Observation) execution.Side { return execution.Side{Result: o} }

func varied(o execution.Observation) execution.Side { return execution.Side{Result: o, Varied: true} }

func TestParseObservation(t *testing.T) {
	for text, want := range map[string]execution.Observation{
		"pass": pass, "fail": fail, "build-error": buildError, "skip": skip, "absent": absent, "excluded": excluded,
	} {
		got, err := execution.ParseObservation(text)
		if err != nil || got != want {
			t.Errorf("ParseObservation(%q) = %v, %v; want %v", text, got, err, want)
		}
		if got.String() != text {
			t.Errorf("%v.String() = %q, want %q", got, got.String(), text)
		}
	}
	for _, text := range []string{"", "PASS", "passed", "build_error", "skipped", " fail", "flaky"} {
		if got, err := execution.ParseObservation(text); !errors.Is(err, execution.ErrInvalidObservation) {
			t.Errorf("ParseObservation(%q) = %v, %v; want ErrInvalidObservation", text, got, err)
		}
	}
}

func TestStateNames(t *testing.T) {
	for state, want := range map[execution.ReceiptState]string{
		execution.ReceiptExcluded: "EXCLUDED", execution.ReceiptMissing: "MISSING", execution.ReceiptFlaky: "FLAKY",
		execution.ReceiptSkipped: "SKIPPED", execution.ReceiptFails: "FAILS", execution.ReceiptNewAPI: "NEW_API",
		execution.ReceiptDetects: "DETECTS", execution.ReceiptInert: "INERT",
	} {
		if got := state.String(); got != want {
			t.Errorf("receipt state %d = %q, want %q", state, got, want)
		}
	}
	for state, want := range map[execution.CanonicalState]string{
		execution.CanonicalPass: "PASS", execution.CanonicalFail: "FAIL", execution.CanonicalSkipped: "SKIPPED",
		execution.CanonicalMissing: "MISSING", execution.CanonicalFlaky: "FLAKY", execution.CanonicalExcluded: "EXCLUDED",
	} {
		if got := state.String(); got != want {
			t.Errorf("canonical state %d = %q, want %q", state, got, want)
		}
	}
	if got := execution.ReceiptState(0).String() + execution.CanonicalState(99).String(); got != "" {
		t.Errorf("a value that is not a state has the name %q, want none", got)
	}
}

func TestClassifyReceiptFollowsThePrecedenceTable(t *testing.T) {
	tests := []struct {
		name      string
		head      execution.Side
		base      execution.Side
		baseBuilt bool
		want      execution.ReceiptState
	}{
		// 0. EXCLUDED is decided statically and comes first, whatever else the sides show.
		{"excluded on both sides", steady(excluded), steady(excluded), true, execution.ReceiptExcluded},
		{"excluded on the head only", steady(excluded), steady(pass), true, execution.ReceiptExcluded},
		{"excluded on the base only", steady(pass), steady(excluded), true, execution.ReceiptExcluded},
		{"excluded beats an absent head", steady(absent), steady(excluded), true, execution.ReceiptExcluded},
		{"excluded beats flaky", varied(excluded), steady(excluded), true, execution.ReceiptExcluded},

		// 1. MISSING: the head side absent, or the base side absent while the base package built.
		{"head absent", steady(absent), steady(pass), true, execution.ReceiptMissing},
		{"head absent and base package not built", steady(absent), steady(buildError), false, execution.ReceiptMissing},
		{"base absent with a built base package", steady(pass), steady(absent), true, execution.ReceiptMissing},
		{"head skip and base absent with a built base package", steady(skip), steady(absent), true, execution.ReceiptMissing},
		{"head fail and base absent with a built base package", steady(fail), steady(absent), true, execution.ReceiptMissing},
		{"MISSING beats FLAKY: head absent, base varied", steady(absent), varied(fail), true, execution.ReceiptMissing},
		{"MISSING beats FLAKY: head varied, base absent and built", varied(pass), steady(absent), true, execution.ReceiptMissing},

		// 2. FLAKY: either side varied across its runs.
		{"head varied", varied(pass), steady(fail), true, execution.ReceiptFlaky},
		{"base varied", steady(pass), varied(fail), true, execution.ReceiptFlaky},
		{"base varied over build errors", steady(pass), varied(buildError), false, execution.ReceiptFlaky},
		{"FLAKY beats SKIPPED", varied(skip), steady(pass), true, execution.ReceiptFlaky},
		{"FLAKY beats FAILS", varied(fail), steady(pass), true, execution.ReceiptFlaky},

		// 3. SKIPPED: the head side skipped.
		{"head skip over a base pass", steady(skip), steady(pass), true, execution.ReceiptSkipped},
		{"head skip over a base fail", steady(skip), steady(fail), true, execution.ReceiptSkipped},
		{"head skip over a base build error", steady(skip), steady(buildError), false, execution.ReceiptSkipped},
		{"head skip over an absent base that did not build", steady(skip), steady(absent), false, execution.ReceiptSkipped},

		// 4. FAILS: the head side failed or did not build.
		{"head fail over a base pass", steady(fail), steady(pass), true, execution.ReceiptFails},
		{"head fail over a base fail", steady(fail), steady(fail), true, execution.ReceiptFails},
		{"head build error over a base pass", steady(buildError), steady(pass), true, execution.ReceiptFails},
		{"head build error over a base build error", steady(buildError), steady(buildError), false, execution.ReceiptFails},

		// 5. NEW_API: the head passes, the base package did not build.
		{"head pass over a base build error", steady(pass), steady(buildError), false, execution.ReceiptNewAPI},

		// 6. DETECTS: the head passes, the base fails.
		{"head pass over a base fail", steady(pass), steady(fail), true, execution.ReceiptDetects},

		// 7. INERT: the head passes, the base passes or skips.
		{"head pass over a base pass", steady(pass), steady(pass), true, execution.ReceiptInert},
		{"head pass over a base skip", steady(pass), steady(skip), true, execution.ReceiptInert},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := execution.ClassifyReceipt(tt.head, tt.base, tt.baseBuilt)
			if err != nil || got != tt.want {
				t.Errorf("ClassifyReceipt = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestClassifyReceiptRefusesWhatNoRowDecides(t *testing.T) {
	// A head pass over a base that is absent although its package did not build: no row matches. It is an error, not NEW_API.
	if got, err := execution.ClassifyReceipt(steady(pass), steady(absent), false); !errors.Is(err, execution.ErrUnclassifiable) {
		t.Errorf("head pass over an absent base that did not build = %v, %v; want ErrUnclassifiable", got, err)
	}
	for _, o := range []execution.Observation{0, 7, 255} {
		if got, err := execution.ClassifyReceipt(steady(o), steady(pass), true); !errors.Is(err, execution.ErrInvalidObservation) {
			t.Errorf("head observation %d = %v, %v; want ErrInvalidObservation", o, got, err)
		}
		if got, err := execution.ClassifyReceipt(steady(pass), steady(o), true); !errors.Is(err, execution.ErrInvalidObservation) {
			t.Errorf("base observation %d = %v, %v; want ErrInvalidObservation", o, got, err)
		}
	}
}

func TestClassifyCanonicalMirrorsTheReceiptOrder(t *testing.T) {
	tests := []struct {
		name string
		head execution.Side
		want execution.CanonicalState
	}{
		{"excluded", steady(excluded), execution.CanonicalExcluded},
		{"excluded beats flaky", varied(excluded), execution.CanonicalExcluded},
		{"absent", steady(absent), execution.CanonicalMissing},
		{"MISSING beats FLAKY", varied(absent), execution.CanonicalMissing},
		{"varied pass", varied(pass), execution.CanonicalFlaky},
		{"varied fail", varied(fail), execution.CanonicalFlaky},
		{"skip", steady(skip), execution.CanonicalSkipped},
		{"fail", steady(fail), execution.CanonicalFail},
		{"build error", steady(buildError), execution.CanonicalFail},
		{"pass", steady(pass), execution.CanonicalPass},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := execution.ClassifyCanonical(tt.head)
			if err != nil || got != tt.want {
				t.Errorf("ClassifyCanonical = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
	if got, err := execution.ClassifyCanonical(steady(0)); !errors.Is(err, execution.ErrInvalidObservation) {
		t.Errorf("zero observation = %v, %v; want ErrInvalidObservation", got, err)
	}
}

func TestCountReceiptKeepsExcludedApartFromEveryPass(t *testing.T) {
	counts, err := execution.CountReceipt([]execution.ReceiptState{
		execution.ReceiptExcluded, execution.ReceiptExcluded, execution.ReceiptMissing, execution.ReceiptFlaky, execution.ReceiptSkipped,
		execution.ReceiptFails, execution.ReceiptNewAPI, execution.ReceiptDetects, execution.ReceiptDetects, execution.ReceiptInert,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := execution.ReceiptCounts{Excluded: 2, Missing: 1, Flaky: 1, Skipped: 1, Fails: 1, NewAPI: 1, Detects: 2, Inert: 1}
	if counts != want {
		t.Errorf("counts = %+v, want %+v", counts, want)
	}
	if got := counts.Evaluated(); got != 8 {
		t.Errorf("Evaluated() = %d, want 8: the EXCLUDED tests did not run", got)
	}
	if got := counts.Blocking(); got != 3 {
		t.Errorf("Blocking() = %d, want 3: FAILS, MISSING and FLAKY", got)
	}
	empty, err := execution.CountReceipt(nil)
	if err != nil || empty != (execution.ReceiptCounts{}) || empty.Evaluated() != 0 {
		t.Errorf("CountReceipt(nil) = %+v, %v; want zero counts", empty, err)
	}
	if _, err := execution.CountReceipt([]execution.ReceiptState{execution.ReceiptInert, 0}); !errors.Is(err, execution.ErrInvalidState) {
		t.Errorf("a value that is not a state: err = %v, want ErrInvalidState", err)
	}
}

// A changed test file that go cannot parse lists no test, so it is no evaluated test, and it blocks like a failing one: a broken file never
// makes an empty receipt.
func TestUnparsedFilesBlockAndAreNoEvaluatedTest(t *testing.T) {
	tests := []struct {
		name                string
		counts              execution.ReceiptCounts
		evaluated, blocking int
		empty               bool
	}{
		{"nothing", execution.ReceiptCounts{}, 0, 0, true},
		{"EXCLUDED tests only", execution.ReceiptCounts{Excluded: 3}, 0, 0, true},
		{"one file", execution.ReceiptCounts{Unparsed: 1}, 0, 1, false},
		{"files beside EXCLUDED tests", execution.ReceiptCounts{Excluded: 3, Unparsed: 2}, 0, 2, false},
		{"files beside the blocking states", execution.ReceiptCounts{Fails: 1, Missing: 1, Flaky: 1, Unparsed: 2}, 3, 5, false},
		{"files beside passing tests", execution.ReceiptCounts{Detects: 2, Inert: 1, Unparsed: 1}, 3, 1, false},
		{"a test that ran", execution.ReceiptCounts{Skipped: 1}, 1, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.counts.Evaluated(); got != tt.evaluated {
				t.Errorf("Evaluated() = %d, want %d: a file that does not parse is no test", got, tt.evaluated)
			}
			if got := tt.counts.Blocking(); got != tt.blocking {
				t.Errorf("Blocking() = %d, want %d", got, tt.blocking)
			}
			if got := tt.counts.Empty(); got != tt.empty {
				t.Errorf("Empty() = %v, want %v: nothing evaluated and nothing unparsed", got, tt.empty)
			}
		})
	}
	// Counting states never invents one: the caller sets Unparsed.
	counts, err := execution.CountReceipt([]execution.ReceiptState{execution.ReceiptDetects})
	if err != nil || counts.Unparsed != 0 {
		t.Errorf("CountReceipt = %+v, %v; want no unparsed file", counts, err)
	}
}

func TestCountCanonicalNeverCountsExcludedAsAPass(t *testing.T) {
	counts, err := execution.CountCanonical([]execution.CanonicalState{
		execution.CanonicalPass, execution.CanonicalExcluded, execution.CanonicalExcluded, execution.CanonicalFail,
		execution.CanonicalSkipped, execution.CanonicalMissing, execution.CanonicalFlaky, execution.CanonicalPass,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := execution.CanonicalCounts{Pass: 2, Fail: 1, Skipped: 1, Missing: 1, Flaky: 1, Excluded: 2}
	if counts != want {
		t.Errorf("counts = %+v, want %+v", counts, want)
	}
	if got := counts.Blocking(); got != 3 {
		t.Errorf("Blocking() = %d, want 3: FAIL, MISSING and FLAKY", got)
	}
	if _, err := execution.CountCanonical([]execution.CanonicalState{200}); !errors.Is(err, execution.ErrInvalidState) {
		t.Errorf("a value that is not a state: err = %v, want ErrInvalidState", err)
	}
}
