package app

import (
	"context"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The checks job decides what execution says about a head: the canonical gate on the Contract check, in this file, and the state of the Receipt check
// (docs/contracts/runtime.md, "The canonical gate" and "The SuiteWard / Receipt check"). The domain decides each rule; the job reads the job of the head,
// counts its tests and asks.

// executionOutcome is what the execution step of a pull request decided beside its jobs: the counts that its Contract check carries and its Receipt check.
type executionOutcome struct {
	counts  *checkExecution
	receipt receiptDecision
}

// executionStep plans and creates the jobs of the head, lets the canonical job gate its Contract check and decides its Receipt check. A head that could not be
// assessed is planned for once it is.
func (j *checksJob) executionStep(ctx context.Context, s *pullState, x *executionRun, assessed checks.Result, d *pullDecision) (executionOutcome, error) {
	jobs, err := j.execute(ctx, s, x, assessed)
	if err != nil {
		return executionOutcome{}, err
	}
	outcome, err := j.applyCanonicalGate(ctx, s, x, jobs, d)
	if err != nil {
		return executionOutcome{}, err
	}
	outcome.receipt, err = j.receiptCheck(ctx, s, jobs)
	return outcome, err
}

// ungated is the check as it was decided before the canonical gate held it back: a stored check with a gate reason is the success it replaced, unchanged for
// an unchanged head and ready otherwise, and a lift of the gate is not remembered (the gate decides it again).
func ungated(r checks.Result) checks.Result {
	r.Unblocked = false
	switch r.Reason {
	case reasonCanonicalPending, reasonCanonicalUnavailable, reasonCanonicalFailing:
		r.State, r.Reason = checks.Success, reasonReady
		if r.Change == changeCodes[inventory.Unchanged] {
			r.Reason = reasonUnchanged
		}
	}
	return r
}

// applyCanonicalGate lets the canonical job of the head hold the check back: only under canonical: required, and only where the check would be success
// (reason unchanged or ready), as every other reason keeps its precedence. A head with no canonical job yet reads as pending.
func (j *checksJob) applyCanonicalGate(ctx context.Context, s *pullState, x *executionRun, jobs headJobs, d *pullDecision) (executionOutcome, error) {
	base := ungated(d.result)
	if !x.known || x.policy.canonical != domain.CanonicalLevelRequired || base.State != checks.Success || (base.Reason != reasonUnchanged && base.Reason != reasonReady) {
		return executionOutcome{}, nil
	}
	status, counts, err := j.canonicalRun(ctx, jobs)
	if err != nil {
		return executionOutcome{}, err
	}
	unblocked := false
	if status == domain.RunUnavailable {
		if unblocked, err = j.executions.Unblocked(ctx, jobs.canonical.ID, s.enrollment.EnrolledAt); err != nil {
			return executionOutcome{}, fmt.Errorf("read the unblock of job %d: %w", jobs.canonical.ID, err)
		}
	}
	reason, err := x.policy.canonical.CheckGate(status, counts, unblocked)
	if err != nil {
		return executionOutcome{}, fmt.Errorf("decide the canonical gate: %w", err)
	}
	d.result = base
	switch reason {
	case domain.ReasonNone:
		d.result.Unblocked = status == domain.RunUnavailable && unblocked
	case domain.ReasonCanonicalFailing:
		d.result.State, d.result.Reason = checks.Failure, string(reason)
	default:
		d.result.State, d.result.Reason = checks.InProgress, string(reason)
	}
	if status != domain.RunComplete {
		return executionOutcome{}, nil
	}
	return executionOutcome{counts: &checkExecution{Tests: counts.Pass + counts.Fail + counts.Skipped + counts.Missing + counts.Flaky, Passed: counts.Pass,
		Skipped: counts.Skipped, Failed: counts.Fail, Missing: counts.Missing, Flaky: counts.Flaky, Excluded: counts.Excluded}}, nil
}

// canonicalStates are the canonical states as the store keeps them.
var canonicalStates = map[string]domain.CanonicalState{"pass": domain.CanonicalPass, "fail": domain.CanonicalFail, "skipped": domain.CanonicalSkipped,
	"missing": domain.CanonicalMissing, "flaky": domain.CanonicalFlaky, "excluded": domain.CanonicalExcluded}

// canonicalRun is the status of the canonical run of the head and, for a complete one, the count of its tests by state. No job reads as pending.
func (j *checksJob) canonicalRun(ctx context.Context, jobs headJobs) (domain.RunStatus, domain.CanonicalCounts, error) {
	if !jobs.hasCanonical {
		return domain.RunPending, domain.CanonicalCounts{}, nil
	}
	switch jobs.canonical.State {
	case execution.StatePending, execution.StateLeased:
		return domain.RunPending, domain.CanonicalCounts{}, nil
	case execution.StateUnavailable:
		return domain.RunUnavailable, domain.CanonicalCounts{}, nil
	case execution.StateComplete:
		tests, err := j.executions.Tests(ctx, jobs.canonical.ID)
		if err != nil {
			return 0, domain.CanonicalCounts{}, fmt.Errorf("read the tests of job %d: %w", jobs.canonical.ID, err)
		}
		states := make([]domain.CanonicalState, len(tests))
		for i, t := range tests {
			state, known := canonicalStates[t.State]
			if !known {
				return 0, domain.CanonicalCounts{}, fmt.Errorf("job %d stores the test state %q, which is no state of a canonical run", jobs.canonical.ID, t.State)
			}
			states[i] = state
		}
		counts, err := domain.CountCanonical(states)
		return domain.RunComplete, counts, err
	}
	return 0, domain.CanonicalCounts{}, fmt.Errorf("the canonical job %d of the head is %s: only a job that is not cancelled is read", jobs.canonical.ID, jobs.canonical.State)
}
