package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The state of a test is the service's decision, not the executor's: it comes from the observations, by the rules of internal/domain/execution
// (ADR 0028, section 5). The executor reports what each side showed, in run order, and the service reads one side as the domain's Side.

// testSides are the two sides of a test; a canonical test has the head only, and an excluded one has neither run.
type testSides struct{ head, base domain.Side }

// deriveStates sets the state of every test, as the lower-case name the store keeps. A test whose observations no rule decides is an error: nothing
// is stored.
func deriveStates(plan execution.Plan, tests []execution.Test) error {
	sides := make([]testSides, len(tests))
	unbuilt := map[string]bool{} // the packages whose base side did not build
	for i, t := range tests {
		s, err := sidesOf(plan, t)
		if err != nil {
			return err
		}
		sides[i] = s
		if s.base.Result == domain.ObservedBuildError {
			unbuilt[t.Package] = true
		}
	}
	for i := range tests {
		state, err := classify(plan, sides[i], !unbuilt[tests[i].Package])
		if err != nil {
			return fmt.Errorf("the state of %s: %w", tests[i].Name, err)
		}
		tests[i].State = strings.ToLower(state)
	}
	return nil
}

// sidesOf reads the sides of a test: an excluded one has no run, and is excluded on both.
func sidesOf(plan execution.Plan, t execution.Test) (testSides, error) {
	if t.ExcludedReason != "" {
		excluded := domain.Side{Result: domain.ObservedExcluded}
		return testSides{head: excluded, base: excluded}, nil
	}
	head, err := sideOf(t.HeadRuns)
	if err != nil || plan != execution.PlanReceipt {
		return testSides{head: head}, err
	}
	base, err := sideOf(t.BaseRuns)
	return testSides{head: head, base: base}, err
}

// sideOf reads the runs of one side in run order: its result is the observation after the reruns, the last, and it varied when the runs differ.
func sideOf(runs []string) (domain.Side, error) {
	if len(runs) == 0 {
		return domain.Side{}, errors.New("a side has no observation")
	}
	var side domain.Side
	for i, name := range runs {
		o, err := domain.ParseObservation(name)
		if err != nil || o == domain.ObservedExcluded { // excluded is a reason, not something a run shows
			return domain.Side{}, errors.New("a side has an observation that is none of pass, fail, build-error, skip and absent")
		}
		if i > 0 && name != runs[0] {
			side.Varied = true
		}
		side.Result = o
	}
	return side, nil
}

// classify decides the state of a test. built says that the base package built, which a base that is absent needs to be MISSING: a package that
// did not build reports build-error for every test of it, so a base that is absent in one that has such a test has no state.
func classify(plan execution.Plan, s testSides, built bool) (string, error) {
	if plan == execution.PlanReceipt {
		state, err := domain.ClassifyReceipt(s.head, s.base, built)
		return state.String(), err
	}
	state, err := domain.ClassifyCanonical(s.head)
	return state.String(), err
}
