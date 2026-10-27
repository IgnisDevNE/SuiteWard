package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
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

// unblock decides a /suiteward unblock whose code was verified and consumed, on the pull request as the synchronization observed it: a fork, a pull request that
// is not open, and a head with nothing unavailable are refused (the code stays consumed), and otherwise the unavailable jobs of the head are lifted.
func (j *commandsJob) unblock(ctx context.Context, enrollment owner.State, c commands.Candidate, pull githubsync.PullState) error {
	switch {
	case isFork(pull.HeadRepo, c.Repo):
		return j.refuse(ctx, enrollment, c, commands.UnblockRefused, reasonFork)
	case !pull.NotOpenSince.IsZero():
		return j.refuse(ctx, enrollment, c, commands.UnblockRefused, reasonNotOpen)
	}
	jobs, plans, err := j.liftable(ctx, c, pull.HeadSHA)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		return j.refuse(ctx, enrollment, c, commands.UnblockRefused, reasonNotUnavailable)
	}
	ack, err := newUnblockedAck(commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}, c.Comment.CommentID, enrollment.Owner.Login, plans)
	if err != nil {
		return &opaque{what: "build the acknowledgment", cause: err}
	}
	ids := make([]int64, len(jobs))
	for i, job := range jobs {
		ids[i] = job.ID
	}
	return j.written(j.executions.RecordUnblock(ctx, j.entry(c, commands.Unblocked, ""), &ack, pull.HeadSHA, ids))
}

// liftable lists the jobs of the head that an unblock lifts, and their plans: the canonical job when the canonical level is not off, and the receipt job when the
// receipt level is not off and the receipt applies to the revision of the pull request, each when it is the job of its plan now and is unavailable.
func (j *commandsJob) liftable(ctx context.Context, c commands.Candidate, head string) (jobs []execution.Job, plans commandtext.Plans, err error) {
	view, err := j.receiptView(ctx, c.RepositoryID, func(ctx context.Context, tx governance.Tx) (contract.Proposal, bool, error) {
		id, found, err := tx.ChangeProposalFor(ctx, carrierID(c.RepositoryID, c.PullNumber))
		if err != nil || !found {
			return contract.Proposal{}, false, err
		}
		proposal, _, err := tx.Proposal(ctx, id)
		return proposal, err == nil, err
	})
	if err != nil {
		return nil, commandtext.Plans{}, err
	}
	canonicalLevel, receiptLevel, err := j.levels(ctx, view.canonical)
	if err != nil {
		return nil, commandtext.Plans{}, err
	}
	if canonicalLevel != domain.CanonicalLevelOff {
		job, found, err := j.newestJob(ctx, c, head, execution.PlanCanonical, func(execution.Job) bool { return true })
		if err != nil {
			return nil, commandtext.Plans{}, err
		}
		if found && job.State == execution.StateUnavailable {
			jobs, plans.Canonical = append(jobs, job), true
		}
	}
	if receiptLevel == domain.ReceiptLevelOff {
		return jobs, plans, nil
	}
	applies, governing, err := j.receiptApplies(ctx, view)
	if err != nil || !applies {
		return jobs, plans, err
	}
	job, found, err := j.newestJob(ctx, c, head, execution.PlanReceipt, func(job execution.Job) bool { return job.GoverningDigest == governing })
	if err != nil {
		return nil, commandtext.Plans{}, err
	}
	if found && job.State == execution.StateUnavailable {
		jobs, plans.Receipt = append(jobs, job), true
	}
	return jobs, plans, nil
}

// newestJob is the newest job of the head for the plan that is not cancelled and that keep accepts. It counts a job created for executor-not-configured, which a
// fresh job replaces once an executor is configured: until the checks job makes that one, it is the job of its plan.
func (j *commandsJob) newestJob(ctx context.Context, c commands.Candidate, head string, plan execution.Plan, keep func(execution.Job) bool) (execution.Job, bool, error) {
	jobs, err := j.executions.HeadJobs(ctx, c.RepositoryID, c.PullNumber, head, plan) // oldest first
	if err != nil {
		return execution.Job{}, false, &opaque{what: "read the jobs of the head", cause: err}
	}
	for _, job := range slices.Backward(jobs) {
		if job.State != execution.StateCancelled && keep(job) {
			return job, true, nil
		}
	}
	return execution.Job{}, false, nil
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
	canonicalLevel, err := domain.ParseCanonicalLevel(policy.Canonical)
	if err != nil {
		return 0, 0, &opaque{what: "read the canonical level", cause: err}
	}
	receiptLevel, err := domain.ParseReceiptLevel(policy.Receipt)
	if err != nil {
		return 0, 0, &opaque{what: "read the receipt level", cause: err}
	}
	return canonicalLevel, receiptLevel, nil
}

// receiptView is what the receipt of a revision is decided on, read in one unit of work.
type receiptView struct {
	canonical contract.CanonicalSnapshot // the Suite's; with no current version before the first one
	revised   bool                       // the revision that was looked for exists
	binding   contract.ApprovalBinding   // of that revision
	expected  artifact.Manifest          // of the canonical version that it expects; zero for a revision that expects none
}

// receiptView reads the Suite's canonical and, when it has a current version, the revision of the proposal that find returns (it reports false for none) and the
// manifest of the canonical version that the revision expects. A project with no Suite has nothing to read: the view is empty.
func (j *commandsJob) receiptView(ctx context.Context, repositoryID int64, find func(context.Context, governance.Tx) (contract.Proposal, bool, error)) (receiptView, error) {
	var view receiptView
	err := j.suites.Do(ctx, owner.ProjectID(repositoryID), defaultSuite, func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		view.canonical = state.Canonical
		if _, current := state.Canonical.Suite().CurrentVersionID(); !current {
			return nil // no policy before the first version, so nothing to decide
		}
		proposal, found, err := find(ctx, tx)
		if err != nil || !found {
			return err
		}
		view.binding, view.revised = proposal.Current().Binding(), true
		version := view.binding.ExpectedCanonical()
		if version == "" {
			return nil
		}
		historical, found, err := tx.Version(ctx, version)
		if err != nil {
			return err
		}
		if !found { // a binding names a version that was promoted: its absence is stored state that is broken, not a missing Suite
			return fmt.Errorf("the canonical version %s that %s expects is not stored", version, view.binding.Reference().RevisionID)
		}
		view.expected = historical.Version().Manifest()
		return nil
	})
	if errors.Is(err, governance.ErrNotFound) {
		return receiptView{}, nil
	}
	if err != nil {
		return receiptView{}, &opaque{what: "read the revision", cause: err}
	}
	return view, nil
}

// receiptApplies reports whether the receipt of the revision applies: whether it adds or changes a file that the include of the canonical version it expects
// matches and the exclude does not (docs/contracts/runtime.md, "Applicable and not applicable"). A revision that expects no canonical, a baseline, has nothing to
// be added to or changed against. governing is the digest of the manifest of that version, which the receipt jobs of the revision name.
func (j *commandsJob) receiptApplies(ctx context.Context, view receiptView) (applies bool, governing artifact.Digest, err error) {
	if !view.revised || view.expected.IsZero() {
		return false, artifact.Digest{}, nil
	}
	declaration := scope.NoDeclaration()
	if i := slices.IndexFunc(view.expected.Entries(), func(e artifact.Entry) bool { return e.Path == scope.DeclarationPath }); i >= 0 {
		raw, err := j.contents.Read(ctx, view.expected.Entries()[i].Content)
		if err != nil {
			return false, artifact.Digest{}, &opaque{what: "read the declaration of the version", cause: err}
		}
		if declaration, err = scope.ParseDeclaration(raw); err != nil {
			return false, artifact.Digest{}, &opaque{what: "parse the declaration of the version", cause: err}
		}
	}
	revision, err := j.revisionManifest(ctx, view.binding.ManifestDigest())
	if err != nil {
		return false, artifact.Digest{}, err
	}
	files, err := domain.EvaluatedFiles(revision, view.expected, domain.Selection{Include: declaration.Include(), Exclude: declaration.Exclude()})
	if err != nil {
		return false, artifact.Digest{}, &opaque{what: "list the files of the receipt", cause: err}
	}
	return len(files) > 0, view.expected.Digest(), nil
}

// revisionManifest reads the manifest of a revision from the content store, and checks that it is the one the digest names.
func (j *commandsJob) revisionManifest(ctx context.Context, digest artifact.Digest) (artifact.Manifest, error) {
	raw, err := j.contents.Read(ctx, digest)
	if err != nil {
		return artifact.Manifest{}, &opaque{what: "read the manifest of the revision", cause: err}
	}
	manifest, err := artifact.ParseManifest(raw)
	if err == nil && manifest.Digest() != digest {
		err = fmt.Errorf("%w: the entries are those of %s", artifact.ErrInvalidEncoding, manifest.Digest())
	}
	if err != nil {
		return artifact.Manifest{}, &opaque{what: "parse the manifest of the revision", cause: err}
	}
	return manifest, nil
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
	view, err := j.receiptView(ctx, c.RepositoryID, func(ctx context.Context, tx governance.Tx) (contract.Proposal, bool, error) {
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
	applies, governing, err := j.receiptApplies(ctx, view)
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
	job, found, err := j.newestJob(ctx, c, head, execution.PlanReceipt, func(job execution.Job) bool { return job.GoverningDigest == governing })
	if err != nil {
		return receiptRun{}, err
	}
	run := receiptRun{job: job}
	switch {
	case !found:
		run.status, run.state = domain.RunPending, execution.RunNone
	case job.State == execution.StatePending || job.State == execution.StateLeased:
		run.status, run.state = domain.RunPending, execution.RunPending
	case job.State == execution.StateUnavailable:
		run.status, run.state = domain.RunUnavailable, execution.RunUnavailable
	case job.State == execution.StateComplete:
		run.status, run.state = domain.RunComplete, execution.RunComplete
		run.counts, run.markers, err = j.receiptCounts(ctx, job.ID)
	default:
		err = &opaque{what: "read the receipt job", cause: fmt.Errorf("job %d is %s", job.ID, job.State)}
	}
	return run, err
}

// receiptStates are the states of a stored test, which the store keeps in lower case, by name.
var receiptStates = func() map[string]domain.ReceiptState {
	states := map[string]domain.ReceiptState{}
	for s := domain.ReceiptExcluded; s <= domain.ReceiptInert; s++ {
		states[strings.ToLower(s.String())] = s
	}
	return states
}()

// receiptCounts counts the states of the tests of a complete receipt and its changed test files that do not parse, which block like a failing test, and the marker
// lines it saw.
func (j *commandsJob) receiptCounts(ctx context.Context, jobID int64) (domain.ReceiptCounts, int, error) {
	tests, err := j.executions.Tests(ctx, jobID)
	if err != nil {
		return domain.ReceiptCounts{}, 0, &opaque{what: "read the tests of the receipt", cause: err}
	}
	states := make([]domain.ReceiptState, len(tests))
	for i, test := range tests {
		state, known := receiptStates[test.State]
		if !known {
			return domain.ReceiptCounts{}, 0, &opaque{what: "read the tests of the receipt", cause: fmt.Errorf("the stored state of a test is %q", test.State)}
		}
		states[i] = state
	}
	counts, err := domain.CountReceipt(states)
	if err != nil {
		return domain.ReceiptCounts{}, 0, &opaque{what: "count the tests of the receipt", cause: err}
	}
	result, found, err := j.executions.Result(ctx, jobID)
	if err != nil {
		return domain.ReceiptCounts{}, 0, &opaque{what: "read the result of the receipt", cause: err}
	}
	if !found { // a complete job always has its results row
		return domain.ReceiptCounts{}, 0, &opaque{what: "read the result of the receipt", cause: fmt.Errorf("job %d is complete and has no results row", jobID)}
	}
	counts.Unparsed = len(result.Unparsed)
	return counts, len(result.Markers), nil
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
