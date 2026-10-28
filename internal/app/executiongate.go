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
	outcome.receipt, err = j.receiptCheck(ctx, s, x, jobs)
	return outcome, err
}

// runnerNotResponding says that the job a gate reads waits for a runner that no request reached for NotRespondingAfter (docs/contracts/runtime.md, "Not responding"):
// the job is leased, or pending for a reason other than GitHub, and the runner is silent. A plan with no job yet waits for nothing. Each check reads its own job.
func (j *checksJob) runnerNotResponding(job execution.Job, found bool) bool {
	return found && job.WaitsForRunner() && j.liveness.Silent(j.now())
}

// headNotResponding says that a head reads "not responding": the runner is silent and at least one wanted job of the head waits for it. The wanted jobs are the canonical
// job when the canonical level is not off and the receipt job when the receipt applies, as the gates read them. The comment of the head reads this.
func (j *checksJob) headNotResponding(jobs headJobs) bool {
	return (jobs.canonicalPlanned && j.runnerNotResponding(jobs.canonical, jobs.hasCanonical)) || (jobs.receiptApplies && j.runnerNotResponding(jobs.receipt, jobs.hasReceipt))
}

// ungated is the check as it was decided before the canonical gate held it back: a stored check with a gate reason is the success it replaced, unchanged for
// an unchanged head and ready otherwise, and a lift of the gate is not remembered (the gate decides it again).
func ungated(r checks.Result) checks.Result {
	r.Unblocked = false
	switch r.Reason {
	case reasonCanonicalPending, reasonCanonicalNotResponding, reasonCanonicalUnavailable, reasonCanonicalFailing:
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
	run, err := j.reads().canonical(ctx, jobs.canonical, jobs.hasCanonical)
	if err != nil {
		return executionOutcome{}, err
	}
	status, counts := run.status, run.counts
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
	if reason == domain.ReasonCanonicalPending && j.runnerNotResponding(jobs.canonical, jobs.hasCanonical) {
		reason = domain.ReasonCanonicalNotResponding
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
	return executionOutcome{counts: checkCountsOf(counts)}, nil
}
