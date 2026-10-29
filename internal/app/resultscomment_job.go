package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The checks job decides the comments of a head (docs/contracts/runtime.md, "The comments of a head"). It fills one domain.Set for the head from the reads that the checks
// use (headNotResponding: the jobs, the policy, whether the receipt applies), asks the Set whether the head waits, whether its runs are final, what holds and what comes
// next, and writes each comment once for its key. A comment is evidence and decides nothing: it is written after the checks, and what it needs of the clock is the
// silence that the execution step read once for the pass (executionOutcome.silent), never another reading.

// headReasonOf is the reason that the Contract check of a head would have before the canonical gate decides, as the Set reads it: the gate acts on a check that would pass,
// and an approval that is missing is what is missing.
func headReasonOf(r checks.Result) domain.HeadReason {
	switch r.Reason {
	case reasonUnchanged, reasonReady:
		return domain.HeadReasonGate
	case reasonApprovalMissing:
		return domain.HeadReasonApprovalMissing
	}
	return domain.HeadReasonOther
}

// headSetOf is the Set of the head that the comments and the counts of an awaiting check read: the Set that headNotResponding returns, with the reason that the head
// would have without the gate. A fork gets no run, so no Set, and neither does a policy that was not read or that names no execution.
func (j *checksJob) headSetOf(ctx context.Context, s *pullState, x *executionRun, jobs headJobs, d *pullDecision) (domain.Set, error) {
	if isFork(s.pull.HeadRepo, s.p.Repo) {
		return domain.Set{}, nil
	}
	set, err := j.headNotResponding(ctx, s, x, jobs)
	if err != nil {
		return domain.Set{}, err
	}
	set.HeadReason = headReasonOf(ungated(d.result))
	return set, nil
}

func (s *pullState) target() commentTarget {
	return commentTarget{RepositoryID: s.p.RepositoryID, Repo: s.p.Repo, Pull: s.pull.Number}
}

// commentsOfHead writes the comments of the head of an open, non-fork pull request of a repository whose canonical declaration names an execution policy: one for a head
// that reads not responding, one for the final sets of its runs, and one for a runner that is back after the owner unblocked it. Each is written once for its key, and a
// failure of one never stops the others.
func (j *checksJob) commentsOfHead(ctx context.Context, s *pullState, executed executionOutcome) error {
	if !s.exec.known || !s.exec.policy.wants() || isFork(s.pull.HeadRepo, s.p.Repo) {
		return nil
	}
	return errors.Join(j.notRespondingComment(ctx, s, executed), j.resultsComments(ctx, s, executed), j.runnerBackComment(ctx, s, executed))
}

// announceOnce writes the comment that build makes, unless the job wrote or found written the key already (the outbox refuses a key twice, which outboxOnce tolerates),
// and remembers the key.
func (j *checksJob) announceOnce(ctx context.Context, repositoryID int64, key string, build func() (governance.OutboxMessage, error)) error {
	if j.announced[key] {
		return nil
	}
	message, err := build()
	if err != nil {
		return err
	}
	if err := outboxOnce(ctx, j.suites, owner.ProjectID(repositoryID), []governance.OutboxMessage{message}); err != nil {
		return err
	}
	j.remember(key)
	return nil
}

func (j *checksJob) remember(key string) {
	if j.announced == nil {
		j.announced = map[string]bool{}
	}
	j.announced[key] = true
}

// notRespondingComment writes the comment of a head that reads not responding: the runner is silent, as the pass read it, and a wanted job waits for it. The key names the
// waiting jobs, so a runner that goes quiet twice during one set of jobs gets one comment.
func (j *checksJob) notRespondingComment(ctx context.Context, s *pullState, executed executionOutcome) error {
	waiting, reading := executed.set.NotResponding(executed.silent)
	if !reading {
		return nil
	}
	p := notRespondingPayload{commentTarget: s.target(), Head: s.pull.HeadSHA, Tier: s.tier, CanonicalJob: waiting.Jobs.Canonical, ReceiptJob: waiting.Jobs.Receipt, Unblock: waiting.Unblock}
	return j.announceOnce(ctx, s.p.RepositoryID, p.key(), func() (governance.OutboxMessage, error) { return newNotRespondingMessage(p) })
}

// runnerBackComment writes the comment of a head that the owner unblocked and whose runner is seen again, for the jobs of the set that the pass read.
func (j *checksJob) runnerBackComment(ctx context.Context, s *pullState, executed executionOutcome) error {
	p := runnerBackPayload{commentTarget: s.target(), Head: s.pull.HeadSHA, Tier: s.tier}
	if j.announced[p.key()] {
		return nil
	}
	back, err := j.runnerBack(ctx, s, executed.set)
	if err != nil || !back {
		return err
	}
	return j.announceOnce(ctx, s.p.RepositoryID, p.key(), func() (governance.OutboxMessage, error) { return newRunnerBackMessage(p) })
}

// resultsComments writes the results comment of the sets of the head that are final and have something to say. A head that is approved has an earlier set, whose comment
// is written when it is final, and the later set is written unless the leads of both say that nothing needs attention (domain.ResultsSets). The key names the jobs, so each
// set is written once, and the keys that the job decided, written or not, are remembered so that a settled head costs one read of its canonical jobs.
func (j *checksJob) resultsComments(ctx context.Context, s *pullState, executed executionOutcome) error {
	later := executed.set
	earlier, hasEarlier, err := j.earlierSet(ctx, s, later)
	if err != nil {
		return err
	}
	earlierFinal := hasEarlier && earlier.Final() // an earlier set is written when it is final, whether or not the later one is
	if !later.Final() && !earlierFinal {
		return nil
	}
	laterKey, earlierKey := keyOfResults(s.target(), s.pull.HeadSHA, later.Jobs()), keyOfResults(s.target(), s.pull.HeadSHA, earlier.Jobs())
	if (!later.Final() || j.announced[laterKey]) && (!earlierFinal || j.announced[earlierKey]) {
		return nil
	}
	unblocks, err := j.executions.HeadUnblocks(ctx, s.p.RepositoryID, s.pull.Number, s.pull.HeadSHA, s.enrollment.EnrolledAt)
	if err != nil {
		return fmt.Errorf("read the unblocks of the head: %w", err)
	}
	filledLater, filledEarlier := filledSet{Set: later}, filledSet{Set: earlier}
	if later.Final() {
		if filledLater, err = j.fillSet(ctx, later, unblocks); err != nil {
			return err
		}
	}
	if earlierFinal {
		if filledEarlier, err = j.fillSet(ctx, earlier, unblocks); err != nil {
			return err
		}
	}
	for _, set := range domain.ResultsSets(filledEarlier.Set, filledLater.Set) {
		f := filledLater
		if set.Earlier {
			f = filledEarlier
		}
		p, err := resultsPayloadOf(s, executed, f)
		if err != nil {
			return err
		}
		if err := j.announceOnce(ctx, s.p.RepositoryID, p.key(), func() (governance.OutboxMessage, error) { return newResultsMessage(p) }); err != nil {
			return err
		}
	}
	if later.Final() {
		j.remember(laterKey)
	}
	if earlierFinal {
		j.remember(earlierKey)
	}
	return nil
}

// earlierSet is the set of an approved head that came before the approval: the canonical job that the canonical's manifest governed, taken with the receipt job of the later
// set. ok is false for a head that is not approved and for one with no such job (its first job was cancelled while it waited). A cancelled job is part of no set.
func (j *checksJob) earlierSet(ctx context.Context, s *pullState, later domain.Set) (earlier domain.Set, ok bool, err error) {
	if later.CanonicalLevel == 0 || later.CanonicalLevel == domain.CanonicalLevelOff || later.CanonicalJob.ID == 0 {
		return domain.Set{}, false, nil // no policy read, or no canonical run
	}
	if governing := later.CanonicalJob.Governing; !governing.IsZero() && governing == s.suite.manifest {
		return domain.Set{}, false, nil // a head that is not approved: its job runs the canonical's manifest, so it needs no read. A missing digest is the domain's to refuse
	}
	stored, err := j.executions.HeadJobs(ctx, s.p.RepositoryID, s.pull.Number, s.pull.HeadSHA, execution.PlanCanonical)
	if err != nil {
		return domain.Set{}, false, fmt.Errorf("read the canonical jobs of the head: %w", err)
	}
	var jobs []domain.Job
	for _, job := range stored {
		if inSet, found := job.InSet(false); found {
			jobs = append(jobs, inSet)
		}
	}
	job, ok, err := domain.EarlierCanonical(jobs, later.CanonicalJob, s.suite.manifest)
	if err != nil || !ok {
		return domain.Set{}, false, err
	}
	earlier = later
	earlier.CanonicalJob, earlier.Earlier = job, true
	return earlier, true, nil
}

// filledSet is a Set whose counts, unblocks and weakened count were read, with what its payload needs besides: the marker lines of the receipt and the plans whose wait the
// owner's command ended.
type filledSet struct {
	domain.Set
	markers int
	ended   commandtext.Plans
}

// fillSet reads what the rules of a final set need: the counts of each complete run (the same reads as the gates), whether the owner unblocked each job and whether the
// command ended it, and the weakened count.
func (j *checksJob) fillSet(ctx context.Context, set domain.Set, unblocks []execution.JobUnblock) (filledSet, error) {
	f := filledSet{Set: set}
	wanted := set.Jobs()
	if wanted.Canonical != 0 {
		job, err := j.storedJob(ctx, wanted.Canonical)
		if err != nil {
			return filledSet{}, err
		}
		run, err := j.reads().canonical(ctx, job, true)
		if err != nil {
			return filledSet{}, err
		}
		f.CanonicalCounts = run.counts
		f.CanonicalJob.Unblocked = unblockedBy(unblocks, job.ID)
		f.ended.Canonical = f.CanonicalJob.Unblocked && job.EndedBy(unblocks)
	}
	if wanted.Receipt != 0 {
		job, err := j.storedJob(ctx, wanted.Receipt)
		if err != nil {
			return filledSet{}, err
		}
		run, err := j.reads().receipt(ctx, job, true)
		if err != nil {
			return filledSet{}, err
		}
		f.ReceiptCounts, f.markers = run.counts, run.markers
		f.ReceiptJob.Unblocked = unblockedBy(unblocks, job.ID)
		f.ended.Receipt = f.ReceiptJob.Unblocked && job.EndedBy(unblocks)
	}
	var err error
	f.Weakened, err = j.weakenedOf(ctx, f.Set)
	return f, err
}

func unblockedBy(unblocks []execution.JobUnblock, id int64) bool {
	for _, u := range unblocks {
		if u.JobID == id {
			return true
		}
	}
	return false
}

// storedJob reads a job of a set by its id: a final job of a set exists.
func (j *checksJob) storedJob(ctx context.Context, id int64) (execution.Job, error) {
	job, found, err := j.executions.Job(ctx, id)
	switch {
	case err != nil:
		return execution.Job{}, fmt.Errorf("read job %d: %w", id, err)
	case !found:
		return execution.Job{}, fmt.Errorf("read job %d: it does not exist", id)
	}
	return job, nil
}

// weakenedOf is the number of weakened tests of a set (docs/contracts/runtime.md, "The weakened signal"): the tests that the receipt marks changed whose state in the
// canonical run is FAIL, MISSING or FLAKY, joined on package and name. It is known only when both runs are wanted and complete and the canonical job has the governing
// manifest of the receipt job, and is otherwise 0, which is never an absence. A complete run with no governing manifest is an error (domain.ErrNoGoverningDigest).
func (j *checksJob) weakenedOf(ctx context.Context, set domain.Set) (int, error) {
	wanted := set.Jobs()
	canonical, receipt := set.CanonicalJob, set.ReceiptJob
	if wanted.Canonical == 0 || wanted.Receipt == 0 || canonical.State != domain.JobComplete || receipt.State != domain.JobComplete {
		return 0, nil
	}
	if !canonical.Governing.IsZero() && !receipt.Governing.IsZero() && canonical.Governing != receipt.Governing { // the head is approved: no counterpart, and nothing to read
		return 0, nil
	}
	storedReceipt, err := j.executions.Tests(ctx, receipt.ID)
	if err != nil {
		return 0, fmt.Errorf("read the tests of job %d: %w", receipt.ID, err)
	}
	storedCanonical, err := j.executions.Tests(ctx, canonical.ID)
	if err != nil {
		return 0, fmt.Errorf("read the tests of job %d: %w", canonical.ID, err)
	}
	receiptTests := make([]domain.ReceiptTest, len(storedReceipt))
	for i, t := range storedReceipt {
		receiptTests[i] = domain.ReceiptTest{Package: t.Package, Name: t.Name, Changed: t.Kind == execution.KindChanged}
	}
	canonicalTests := make([]domain.CanonicalTest, len(storedCanonical))
	for i, t := range storedCanonical {
		state, known := canonicalStates[t.State]
		if !known {
			return 0, fmt.Errorf("read the tests of job %d: it stores the test state %q, which is no state of its plan", canonical.ID, t.State)
		}
		canonicalTests[i] = domain.CanonicalTest{Package: t.Package, Name: t.Name, State: state}
	}
	tests, _, err := domain.Weakened(domain.WeakenedRun{Status: domain.RunComplete, Governing: receipt.Governing}, domain.WeakenedRun{Status: domain.RunComplete, Governing: canonical.Governing},
		receiptTests, canonicalTests)
	return len(tests), err
}

// resultsPayloadOf is the payload of the results comment of a filled set: the jobs, the runs with their counts or their reasons, the weakened count, the unblocks and what
// the command ended, and the next step. The mode of the approve is named only when the next step is the approve and the check names it too.
func resultsPayloadOf(s *pullState, executed executionOutcome, f filledSet) (resultsPayload, error) {
	next, err := f.Next()
	if err != nil {
		return resultsPayload{}, fmt.Errorf("decide the next step of the results: %w", err)
	}
	pair := f.Jobs()
	p := resultsPayload{commentTarget: s.target(), Head: s.pull.HeadSHA, Tier: s.tier, CanonicalJob: pair.Canonical, ReceiptJob: pair.Receipt, Reference: executed.reference,
		CanonicalLevel: f.CanonicalLevel.String(), ReceiptLevel: f.ReceiptLevel.String(), Weakened: f.Weakened, Next: next.String(),
		ApproveMode: executed.approveMode && next == domain.NextApprove, ApprovalMissing: f.HeadReason == domain.HeadReasonApprovalMissing,
		Unblocked: planNames(commandtext.Plans{Canonical: pair.Canonical != 0 && f.CanonicalJob.Unblocked, Receipt: pair.Receipt != 0 && f.ReceiptJob.Unblocked}), Ended: planNames(f.ended)}
	if pair.Canonical != 0 {
		p.Canonical = &resultsCanonical{Status: runComplete}
		if f.CanonicalJob.State == domain.JobComplete {
			p.Canonical.Counts = checkCountsOf(f.CanonicalCounts)
		} else {
			p.Canonical.Status, p.Canonical.Reason = runUnavailable, f.CanonicalJob.Reason
		}
	}
	if pair.Receipt != 0 {
		p.Receipt = &resultsReceipt{Status: runComplete}
		if c := f.ReceiptCounts; f.ReceiptJob.State == domain.JobComplete {
			p.Receipt.Counts = &receiptCheckCounts{Detects: c.Detects, Inert: c.Inert, NewAPI: c.NewAPI, Fails: c.Fails, Missing: c.Missing, Flaky: c.Flaky, Skipped: c.Skipped,
				Excluded: c.Excluded, Markers: f.markers, Unparsed: c.Unparsed}
		} else {
			p.Receipt.Status, p.Receipt.Reason = runUnavailable, f.ReceiptJob.Reason
		}
	}
	return p, nil
}
