package app

import (
	"context"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The checks job decides what execution says about a head: the canonical gate on the Contract check, in this file, and the state of the Receipt check
// (docs/contracts/runtime.md, "The canonical gate" and "The SuiteWard / Receipt check"). The domain decides each rule; the job reads the job of the head,
// counts its tests and asks.

// executionOutcome is what the execution step of a pull request decided beside its jobs: the counts that its Contract check carries, its Receipt check, and the
// silence of the runner that the step read, which the comment of the head reads too (the zero value, for a step that failed, is not a reading).
type executionOutcome struct {
	counts  *checkExecution
	receipt receiptDecision
	silent  bool

	// What the comments of the head and the counts of an awaiting check read: the Set of the head (headSetOf; zero for a step that failed or a head that no run decides, which is
	// never final), the revision of the head, whether the approve of its check names a mode, and the weakened count that the counts of an awaiting check state.
	set         domain.Set
	reference   string
	approveMode bool
	weakened    int
}

// executionStep plans and creates the jobs of the head, lets the canonical job gate its Contract check and decides its Receipt check. A head that could not be
// assessed is planned for once it is. It reads the silence of the runner once, so that the two checks and the comment of the head never disagree about it.
func (j *checksJob) executionStep(ctx context.Context, s *pullState, x *executionRun, assessed checks.Result, d *pullDecision) (executionOutcome, error) {
	jobs, err := j.execute(ctx, s, x, assessed)
	if err != nil {
		return executionOutcome{}, err
	}
	silent := j.liveness.Silent(j.now())
	outcome, err := j.applyCanonicalGate(ctx, s, x, jobs, d, silent)
	if err != nil {
		return executionOutcome{}, err
	}
	outcome.receipt, err = j.receiptCheck(ctx, s, x, jobs, silent)
	outcome.silent = silent
	if err != nil {
		return outcome, err
	}
	base := ungated(d.result)
	outcome.reference, outcome.approveMode = base.Reference, x.namesMode(base)
	outcome.set, err = j.headSetOf(ctx, s, x, jobs, silent, d)
	return outcome, err
}

// runnerNotResponding says that the job a gate reads waits for a runner that no request reached for NotRespondingAfter (docs/contracts/runtime.md, "Not responding"):
// the job is leased, or pending for a reason other than GitHub (Job.WaitsForRunner, the rule of the domain), and the runner is silent, as the pass read it. A plan with no
// job yet waits for nothing. Each check reads its own job, so a head-level reading (headNotResponding) never changes what a check says.
func runnerNotResponding(job execution.Job, found, silent bool) bool {
	return silent && found && job.WaitsForRunner()
}

// headNotResponding says whether a head reads "not responding" and which of its wanted jobs wait for the runner, 0 for none (docs/contracts/runtime.md, "Not responding",
// "Definitions"), for a runner whose silence the caller read once for the pass (executionOutcome.silent): the function never reads the clock. It is domain.Set.NotResponding,
// the rule that the comment of the head and the page read, so the wanted jobs are the contract's: the canonical job when the canonical level is not off, whatever the pass
// decided of the head, and the receipt job when the receipt level is not off and the pass found that the receipt applies. A head whose check another rule decided has no
// canonical job planned and none read, but the job that is still open waits, so it is read here. A policy that was not read, or that names no execution, wants nothing
// (the zero Set). The gates do not call this: each check reads its own job (runnerNotResponding).
//
// The Set that it read from comes back with the answer: its levels, whether the receipt applies and was decided, and the two jobs. The comment fills the rest of it
// (HeadReason, Earlier, the counts of the runs, Weakened) and asks it for Unblock, Holds and Next, so that it never rebuilds the jobs. Two things are the caller's: the
// jobs carry Unblocked false (read the unblock rows of the head and set it, or an unavailable job that the owner unblocked reads as lost), and a canonical job read here is
// in the Set and not in jobs. Waiting.Unblock of the first answer is not to be read: it depends on the reason of the head, which only the filled Set holds.
func (j *checksJob) headNotResponding(ctx context.Context, s *pullState, x *executionRun, jobs headJobs, silent bool) (set domain.Set, waiting domain.JobPair, reading bool, err error) {
	if !x.known || !x.policy.wants() {
		return domain.Set{}, domain.JobPair{}, false, nil
	}
	canonical, hasCanonical := jobs.canonical, jobs.hasCanonical
	if x.policy.canonicalOn() && !jobs.canonicalPlanned {
		if canonical, hasCanonical, err = j.reads().gateJob(ctx, s.p.RepositoryID, s.pull.Number, s.pull.HeadSHA, execution.PlanCanonical, artifact.Digest{}); err != nil {
			return domain.Set{}, domain.JobPair{}, false, err
		}
	}
	set = domain.Set{CanonicalLevel: x.policy.canonical, ReceiptLevel: x.policy.receipt, ReceiptApplies: jobs.receiptApplies, ReceiptDecided: jobs.receiptDecided}
	if set.CanonicalJob, err = planJob(canonical, hasCanonical); err != nil {
		return domain.Set{}, domain.JobPair{}, false, err
	}
	if set.ReceiptJob, err = planJob(jobs.receipt, jobs.hasReceipt); err != nil {
		return domain.Set{}, domain.JobPair{}, false, err
	}
	answer, reading := set.NotResponding(silent)
	return set, answer.Jobs, reading, nil
}

// planJob is the job of a plan as a set holds it: the zero job for a plan with none. A cancelled job is an error: the gates never return one.
func planJob(job execution.Job, found bool) (domain.Job, error) {
	if !found {
		return domain.Job{}, nil
	}
	inSet, ok := job.InSet(false)
	if !ok {
		return domain.Job{}, fmt.Errorf("read the %s job %d: it is %s: only a job that is not cancelled is read", job.Plan, job.ID, job.State)
	}
	return inSet, nil
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
func (j *checksJob) applyCanonicalGate(ctx context.Context, s *pullState, x *executionRun, jobs headJobs, d *pullDecision, silent bool) (executionOutcome, error) {
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
	if reason == domain.ReasonCanonicalPending && runnerNotResponding(jobs.canonical, jobs.hasCanonical, silent) {
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

// runnerBack says that the runner is back for the current head: it has a job with an unblock row (since the enrollment), the reason no-executor and an end earlier than the
// last real executor request (docs/contracts/runtime.md, "The early unblock and its hold"). It reads both plans, whether the owner's command ended the job or lifted it after
// the lease wait. The comment that warns the owner reads it.
func (j *checksJob) runnerBack(ctx context.Context, s *pullState) (bool, error) {
	for _, plan := range []execution.Plan{execution.PlanCanonical, execution.PlanReceipt} {
		jobs, err := j.executions.HeadJobs(ctx, s.p.RepositoryID, s.pull.Number, s.pull.HeadSHA, plan)
		if err != nil {
			return false, fmt.Errorf("read the %s jobs of the head: %w", plan, err)
		}
		for _, job := range jobs {
			if job.State != execution.StateUnavailable || job.Reason != execution.ReasonNoExecutor || !j.liveness.HeardSince(job.FinishedAt) {
				continue
			}
			unblocked, err := j.executions.Unblocked(ctx, job.ID, s.enrollment.EnrolledAt)
			if err != nil {
				return false, fmt.Errorf("read the unblock of job %d: %w", job.ID, err)
			}
			if unblocked {
				return true, nil
			}
		}
	}
	return false, nil
}
