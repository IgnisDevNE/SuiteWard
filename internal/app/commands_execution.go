package app

import (
	"context"
	"errors"
	"strconv"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The execution commands in the commands job (M2.1; docs/contracts/runtime.md, "Commands"): the preview mode, the unblock and the receipt gate of an approve.

// refuse records a refusal with its acknowledgment: disposition d with the fixed reason of the acknowledgment.
func (j *commandsJob) refuse(ctx context.Context, enrollment owner.State, c commands.Candidate, d commands.Disposition, reason string) error {
	ack, err := newBootstrapAck(commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}, c.Comment.CommentID, enrollment.Owner.Login, d, reason, "")
	if err != nil {
		return &opaque{what: "build the acknowledgment", cause: err}
	}
	return j.record(ctx, c, d, "", &ack)
}

// setMode decides a /suiteward mode: it records the preview mode of the pull request (the entry carries it). It takes no code and so no step, and a pull request
// that is not open is no reason to ignore it: a fork and a pull request that is not open are refused and acknowledged.
func (j *commandsJob) setMode(ctx context.Context, enrollment owner.State, c commands.Candidate, mode domain.Mode) error {
	pull, err := j.observedPull(ctx, c)
	if err != nil {
		return err
	}
	switch {
	case isFork(pull.HeadRepo, c.Repo):
		return j.refuse(ctx, enrollment, c, commands.ModeRefused, reasonFork)
	case !pull.NotOpenSince.IsZero():
		return j.refuse(ctx, enrollment, c, commands.ModeRefused, reasonNotOpen)
	}
	ack, err := newModeSetAck(commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}, c.Comment.CommentID, enrollment.Owner.Login, mode)
	if err != nil {
		return &opaque{what: "build the acknowledgment", cause: err}
	}
	return j.record(ctx, c, commands.ModeSet, "", &ack)
}

// unblock decides a /suiteward unblock whose code was verified and consumed, on the pull request as the synchronization observed it (docs/contracts/runtime.md,
// "The early unblock and its hold"): a fork and a pull request that is not open are refused (the code stays consumed). Otherwise it lifts the wanted jobs of the head
// that are unavailable now and, while the runner does not respond, ends and lifts the ones that wait for it. With nothing to lift it is refused with runner-responding
// when a wanted job waits for a runner that responds, and with not-unavailable otherwise.
func (j *commandsJob) unblock(ctx context.Context, enrollment owner.State, c commands.Candidate, pull githubsync.PullState) error {
	switch {
	case isFork(pull.HeadRepo, c.Repo):
		return j.refuse(ctx, enrollment, c, commands.UnblockRefused, reasonFork)
	case !pull.NotOpenSince.IsZero():
		return j.refuse(ctx, enrollment, c, commands.UnblockRefused, reasonNotOpen)
	}
	lift, err := j.liftable(ctx, c, pull.HeadSHA)
	if err != nil {
		return err
	}
	switch {
	case len(lift.jobs) > 0:
	case lift.waiting:
		return j.refuse(ctx, enrollment, c, commands.UnblockRefused, reasonRunnerResponding)
	default:
		return j.refuse(ctx, enrollment, c, commands.UnblockRefused, reasonNotUnavailable)
	}
	ack, err := newUnblockedAck(commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}, c.Comment.CommentID, enrollment.Owner.Login, lift.plans, lift.ended)
	if err != nil {
		return &opaque{what: "build the acknowledgment", cause: err}
	}
	entry := j.entry(c, commands.Unblocked, "")
	return j.written(j.executions.RecordUnblock(ctx, entry, &ack, pull.HeadSHA, lift.jobs, entry.DecidedAt))
}

// liftSet is what an unblock does to the head: the jobs it lifts, each as the command read it (the store ends an open one only while it is still so), the plans of
// those jobs and the plans whose open job it ends. waiting says that a wanted job waits for the runner, which responds, so that it is neither lifted nor ended.
type liftSet struct {
	jobs         []execution.UnblockJob
	plans, ended commandtext.Plans
	waiting      bool
}

// liftable reads the wanted jobs of the head: the canonical job when the canonical level is not off, and the receipt job when the receipt level is not off and the
// receipt applies to the revision of the pull request, each when it is the job of its plan now. An unavailable one is lifted. One that waits for the runner (leased,
// or pending for a reason other than GitHub) is ended and lifted when the runner is silent, and is left alone, to run as before, when it responds.
func (j *commandsJob) liftable(ctx context.Context, c commands.Candidate, head string) (lift liftSet, err error) {
	view, err := j.reads().receiptView(ctx, c.RepositoryID, func(ctx context.Context, tx governance.Tx) (contract.Proposal, bool, error) {
		id, found, err := tx.ChangeProposalFor(ctx, carrierID(c.RepositoryID, c.PullNumber))
		if err != nil || !found {
			return contract.Proposal{}, false, err
		}
		proposal, _, err := tx.Proposal(ctx, id)
		return proposal, err == nil, err
	})
	if err != nil {
		return liftSet{}, err
	}
	canonicalLevel, receiptLevel, err := j.levels(ctx, view.canonical)
	if err != nil {
		return liftSet{}, err
	}
	if canonicalLevel == domain.CanonicalLevelOff && receiptLevel == domain.ReceiptLevelOff {
		return lift, nil // nothing runs, so nothing waits for the runner
	}
	silent := j.liveness.Silent(j.now()) // read once for the command, before the decisions: both plans see the same runner
	if canonicalLevel != domain.CanonicalLevelOff {
		job, found, err := j.reads().gateJob(ctx, c.RepositoryID, c.PullNumber, head, execution.PlanCanonical, artifact.Digest{})
		if err != nil {
			return liftSet{}, err
		}
		lift.add(execution.PlanCanonical, job, action(job, found, silent))
	}
	if receiptLevel == domain.ReceiptLevelOff {
		return lift, nil
	}
	applies, governing, err := j.reads().receiptApplies(ctx, view)
	if err != nil || !applies {
		return lift, err
	}
	job, found, err := j.reads().gateJob(ctx, c.RepositoryID, c.PullNumber, head, execution.PlanReceipt, governing)
	if err != nil {
		return liftSet{}, err
	}
	lift.add(execution.PlanReceipt, job, action(job, found, silent))
	return lift, nil
}

// liftAction is what an unblock does to the job of a plan.
type liftAction uint8

const (
	leaveJob liftAction = iota // nothing to lift: no job, or one that is complete or cancelled, or that waits on GitHub
	liftJob                    // it is unavailable already: lift it as it is
	endJob                     // it waits for a runner that does not respond: end it for no-executor and lift it
	waitJob                    // it waits for a runner that responds: leave it to run, and refuse the unblock if nothing else is lifted
)

// action decides what an unblock does to the job that the gates read for a plan, given whether the runner is silent.
func action(job execution.Job, found, silent bool) liftAction {
	switch {
	case !found:
		return leaveJob
	case job.State == execution.StateUnavailable:
		return liftJob
	case !job.WaitsForRunner():
		return leaveJob
	case silent:
		return endJob
	}
	return waitJob
}

// add puts the job of a plan in the set as its action says.
func (s *liftSet) add(plan execution.Plan, job execution.Job, action liftAction) {
	switch action {
	case waitJob:
		s.waiting = true
		return
	case leaveJob:
		return
	}
	s.jobs = append(s.jobs, execution.UnblockJob{ID: job.ID, State: job.State, LeaseID: job.LeaseID})
	if plan == execution.PlanCanonical {
		s.plans.Canonical, s.ended.Canonical = true, action == endJob
	} else {
		s.plans.Receipt, s.ended.Receipt = true, action == endJob
	}
}

// levels are the levels of the effective policy, from the declaration of the Suite's current canonical version: off and off before the first version and when the
// declaration names no profile (docs/contracts/runtime.md, "The effective policy").
func (j *commandsJob) levels(ctx context.Context, canonical contract.CanonicalSnapshot) (domain.CanonicalLevel, domain.ReceiptLevel, error) {
	if _, current := canonical.Suite().CurrentVersionID(); !current {
		return domain.CanonicalLevelOff, domain.ReceiptLevelOff, nil
	}
	_, declaration, err := governance.CanonicalDeclaration(ctx, j.contents, canonical)
	if errors.Is(err, governance.ErrNoCanonicalDeclaration) {
		return domain.CanonicalLevelOff, domain.ReceiptLevelOff, nil
	}
	if err != nil {
		return 0, 0, &opaque{what: "read the canonical declaration", cause: err}
	}
	policy, named := declaration.Execution()
	if !named {
		return domain.CanonicalLevelOff, domain.ReceiptLevelOff, nil
	}
	canonicalLevel, receiptLevel, err := executionLevels(policy)
	if err != nil {
		return 0, 0, &opaque{what: "read the levels of the policy", cause: err}
	}
	return canonicalLevel, receiptLevel, nil
}

// gateOutcome is what the receipt gate decided for an approve. applies says that it read a receipt, so the entry is recorded with record; reason is why it refuses the
// approve, and none when it lets it through. counts and markers are the receipt of the head, which the acknowledgment of a mode that was not met states.
type gateOutcome struct {
	applies bool
	record  execution.ApprovalGate
	reason  domain.Reason
	counts  domain.ReceiptCounts
	markers int
}

// receiptGate is step 6a of the owner's commands: it reads the receipt of the head that the synchronization observed and decides an approve of the proposal's current
// revision. It does not apply (the outcome is empty) when the Suite has no canonical, the policy's receipt level is off, the approve names another revision or a
// proposal that does not exist (ProcessConsent answers it), or the consent of this command is already recorded: a retry after a crash skips the gate, since the state at
// the approval was not recorded and a state read now would not be that one.
func (j *commandsJob) receiptGate(ctx context.Context, enrollment owner.State, c commands.Candidate, command commandtext.Command, operation contract.OperationID) (gateOutcome, error) {
	var replayed bool
	revision := contract.ProposalRevisionID("R" + strconv.Itoa(command.Revision))
	view, err := j.reads().receiptView(ctx, c.RepositoryID, func(ctx context.Context, tx governance.Tx) (contract.Proposal, bool, error) {
		proposal, _, err := tx.Proposal(ctx, proposalID(command.Proposal))
		if errors.Is(err, governance.ErrNotFound) {
			return contract.Proposal{}, false, nil
		}
		if err != nil || proposal.Current().Binding().Reference().RevisionID != revision {
			return contract.Proposal{}, false, err
		}
		_, replayed, err = tx.Receipt(ctx, operation)
		return proposal, err == nil, err
	})
	if err != nil || !view.revised || replayed {
		return gateOutcome{}, err
	}
	_, level, err := j.levels(ctx, view.canonical)
	if err != nil || level == domain.ReceiptLevelOff {
		return gateOutcome{}, err
	}
	pull, err := j.observedPull(ctx, c)
	if err != nil {
		return gateOutcome{}, err
	}
	applies, governing, err := j.reads().receiptApplies(ctx, view)
	if err != nil {
		return gateOutcome{}, err
	}
	out := gateOutcome{applies: true, record: execution.ApprovalGate{HeadSHA: pull.HeadSHA, ReceiptLevel: level, RunState: execution.RunNotApplicable}}
	status := domain.RunNotApplicable
	if applies {
		run, err := j.receiptRun(ctx, c, pull.HeadSHA, governing)
		if err != nil {
			return gateOutcome{}, err
		}
		status, out.counts, out.markers = run.status, run.counts, run.markers
		out.record.RunState, out.record.JobID = run.state, run.job.ID
	}
	// Only a required receipt is lifted by an unblock: under the other levels an unavailable receipt is no reason to refuse.
	if status == domain.RunUnavailable && level == domain.ReceiptLevelRequired {
		if out.record.Unblocked, err = j.executions.Unblocked(ctx, out.record.JobID, enrollment.EnrolledAt); err != nil {
			return gateOutcome{}, &opaque{what: "read the unblock of the receipt", cause: err}
		}
	}
	if out.reason, err = level.ApproveGate(status, out.counts, command.Mode, out.record.Unblocked); err != nil {
		return gateOutcome{}, &opaque{what: "judge the receipt", cause: err}
	}
	if out.record.Summary, err = summaryOf(status, out.counts, command.Mode); err != nil {
		return gateOutcome{}, &opaque{what: "summarize the receipt", cause: err}
	}
	return out, nil
}

// summaryOf says whether a complete receipt met the mode of the approve. An empty receipt, one that is not complete and an approve with no mode have no summary.
func summaryOf(status domain.RunStatus, counts domain.ReceiptCounts, mode domain.Mode) (execution.Summary, error) {
	if status != domain.RunComplete || counts.Empty() || mode == domain.ModeNone {
		return execution.SummaryNone, nil
	}
	summary, err := counts.Summarize(mode)
	if err != nil {
		return execution.SummaryNone, err
	}
	if summary.Verdict == domain.VerdictMet {
		return execution.SummaryMet, nil
	}
	return execution.SummaryNotMet, nil
}

// receiptRun reads the receipt job of the head: the newest job that is not cancelled and names the manifest of the version the revision expects. A head with no
// such job counts as pending.
func (j *commandsJob) receiptRun(ctx context.Context, c commands.Candidate, head string, governing artifact.Digest) (receiptRun, error) {
	reads := j.reads()
	job, found, err := reads.gateJob(ctx, c.RepositoryID, c.PullNumber, head, execution.PlanReceipt, governing)
	if err != nil {
		return receiptRun{}, err
	}
	return reads.receipt(ctx, job, found)
}

// refuseApproval records an approve that the receipt gate refused, with its acknowledgment and the approval receipt, in one transaction. The code stays consumed.
func (j *commandsJob) refuseApproval(ctx context.Context, enrollment owner.State, c commands.Candidate, command commandtext.Command, reference string, gate gateOutcome) error {
	ack, err := newReceiptRefusedAck(commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}, c.Comment.CommentID, enrollment.Owner.Login, reference,
		gate.reason, command.Mode, gate.counts, gate.markers)
	if err != nil {
		return &opaque{what: "build the acknowledgment", cause: err}
	}
	return j.written(j.executions.RecordApproval(ctx, j.entry(c, commands.ReceiptRefused, ""), &ack, gate.record))
}

// recordProcessed records an approve or a revoke that ProcessConsent processed. An approve that went through the gate is recorded with its approval receipt.
func (j *commandsJob) recordProcessed(ctx context.Context, c commands.Candidate, operation contract.OperationID, gate gateOutcome) error {
	if !gate.applies {
		return j.record(ctx, c, commands.Processed, operation, nil)
	}
	return j.written(j.executions.RecordApproval(ctx, j.entry(c, commands.Processed, operation), nil, gate.record))
}
