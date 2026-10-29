package executiontest

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

// The replacement of a job (docs/contracts/runtime.md, "The early unblock and its hold"): a fresh job that names the job it replaces, and the owner's unblock of that job,
// never both take effect. Whichever comes first wins: the unblock holds the replacement back, or the replacement makes the unblock fail as changed.

// replacing is the job that replaces old, for the enrollment that began at the offset enrolled, created at the offset now.
func replacing(spec execution.Spec, old execution.Job, enrolled, now time.Duration) execution.NewJob {
	return execution.NewJob{Spec: spec, At: at(now), Replaces: old.ID, UnblockedSince: at(enrolled)}
}

// requireHeadJobs requires the jobs of the head and plan of spec to be these, oldest first.
func (e *env) requireHeadJobs(spec execution.Spec, want ...int64) {
	e.t.Helper()
	jobs, err := e.s.HeadJobs(e.ctx, spec.RepositoryID, spec.PullNumber, spec.HeadSHA, spec.Plan)
	e.must(err)
	got := make([]int64, len(jobs))
	for i, job := range jobs {
		got[i] = job.ID
	}
	if !slices.Equal(got, want) {
		e.t.Fatalf("the %s jobs of head %s are %v; want %v", spec.Plan, spec.HeadSHA, got, want)
	}
}

// endedForNoExecutor is a pending job that the sweep ended for no-executor. The sweep ends every pending job, so a caller that needs another one pending makes it after.
func (e *env) endedForNoExecutor(spec execution.Spec) execution.Job {
	e.t.Helper()
	job := e.enqueue(spec, 0)
	e.expire(expireParams(leaseWait, 0))
	job = e.get(job.ID)
	if job.State != execution.StateUnavailable || job.Reason != execution.ReasonNoExecutor {
		e.t.Fatalf("job %d is %s for %q; want it unavailable for no-executor", job.ID, job.State, job.Reason)
	}
	return job
}

// unblocked lifts the job for the comment, created at the offset created, as the owner's command does.
func (e *env) unblocked(comment int64, job execution.Job, created time.Duration) {
	e.t.Helper()
	e.must(e.s.RecordUnblock(e.ctx, ledgerEntry(comment, commands.Unblocked, created), nil, job.HeadSHA, unblockJobs(job), at(created+time.Second)))
}

// rpHeld is the replacement after the unblock: a job that the sweep ended for no-executor and the owner unblocked is not replaced, and the store returns it and writes
// nothing. An unblock from before the enrollment does not hold, and neither does one of a job that ended for executor-not-configured: configuring the executor is what the
// owner wanted.
func rpHeld(e *env) {
	spec := canonicalSpec(1)
	old := e.endedForNoExecutor(spec)
	e.unblocked(1, old, 10*time.Minute)

	got, created, err := e.s.Enqueue(e.ctx, replacing(spec, old, 5*time.Minute, 11*time.Minute))
	e.must(err)
	if created || got.ID != old.ID {
		e.t.Fatalf("a replacement of an unblocked job = job %d, created %v; want job %d returned and nothing created", got.ID, created, old.ID)
	}
	e.requireHeadJobs(spec, old.ID)
	e.requireJob(old)

	// The unblock came after the enrollment began at 10 minutes, which is the creation of its entry: it holds; one microsecond later it is from before the enrollment.
	held, created, err := e.s.Enqueue(e.ctx, replacing(spec, old, 10*time.Minute, 11*time.Minute))
	e.must(err)
	if created || held.ID != old.ID {
		e.t.Fatalf("a replacement for an enrollment that began with the unblock = job %d, created %v; want it held", held.ID, created)
	}
	fresh, created, err := e.s.Enqueue(e.ctx, replacing(spec, old, 10*time.Minute+time.Microsecond, 11*time.Minute))
	e.must(err)
	if !created || fresh.ID == old.ID {
		e.t.Fatalf("a replacement for the next enrollment = job %d, created %v; want a new job", fresh.ID, created)
	}
	e.requireHeadJobs(spec, old.ID, fresh.ID)
	e.requireCurrent(spec, fresh.ID)

	// A job that ended for executor-not-configured is replaced whatever the owner did.
	notConfigured := e.enqueueFinal(canonicalSpec(2), execution.ReasonNotConfigured, 0)
	e.unblocked(2, notConfigured, 10*time.Minute)
	replaced, created, err := e.s.Enqueue(e.ctx, replacing(canonicalSpec(2), notConfigured, 0, 11*time.Minute))
	e.must(err)
	if !created || replaced.ID == notConfigured.ID {
		e.t.Fatalf("a replacement of an unblocked executor-not-configured job = job %d, created %v; want a new job", replaced.ID, created)
	}

	// A job that the owner did not unblock is replaced, and the replacement is a job like any other: another unblock does not carry over to it.
	ended := e.endedForNoExecutor(canonicalSpec(3))
	next, created, err := e.s.Enqueue(e.ctx, replacing(canonicalSpec(3), ended, 0, time.Hour))
	e.must(err)
	if !created || next.ID == ended.ID || next.State != execution.StatePending {
		e.t.Fatalf("a replacement of a job that was not unblocked = %+v, created %v; want a new pending job", next, created)
	}
	e.requireUnblocked(next.ID, 0, false)
}

// rpInvalid is the replacement that names a job it cannot replace: nothing is written.
func rpInvalid(e *env) {
	spec := canonicalSpec(1)
	old := e.endedForNoExecutor(spec)
	other := e.endedForNoExecutor(canonicalSpec(2))
	pending := e.enqueue(canonicalSpec(3), time.Hour)
	cancelled := e.enqueue(canonicalSpec(4), time.Hour)
	_, err := e.s.Cancel(e.ctx, cancelled.ID, execution.ReasonHeadMoved, at(time.Hour+time.Second))
	e.must(err)
	noMergeBase := e.enqueueFinal(withBase(receiptSpec(5), ""), execution.ReasonNoMergeBase, 0)
	otherManifest := spec
	otherManifest.GoverningDigest = digest("an earlier manifest")
	otherDeclaration := spec
	otherDeclaration.DeclarationDigest = digest("another declaration")
	otherPull := spec
	otherPull.PullNumber = 2
	otherRepository := spec
	otherRepository.RepositoryID = 20
	for name, n := range map[string]execution.NewJob{
		"a job that does not exist":           replacing(spec, execution.Job{ID: 987654}, 0, time.Hour),
		"a job of another head":               replacing(spec, other, 0, time.Hour),
		"a job of another pull request":       replacing(otherPull, old, 0, time.Hour),
		"a job of another repository":         replacing(otherRepository, old, 0, time.Hour),
		"a job of the other plan":             replacing(receiptSpec(1), old, 0, time.Hour),
		"a job of another governing manifest": replacing(otherManifest, old, 0, time.Hour),
		"a job of another declaration":        replacing(otherDeclaration, old, 0, time.Hour),
		"a job that is pending":               replacing(canonicalSpec(3), pending, 0, time.Hour),
		"a job that was cancelled":            replacing(canonicalSpec(4), cancelled, 0, time.Hour),
		"a job that ended for no-merge-base":  replacing(receiptSpec(5), noMergeBase, 0, time.Hour),
		"a negative job id":                   replacing(spec, execution.Job{ID: -1}, 0, time.Hour),
		"no enrollment time":                  {Spec: spec, At: at(time.Hour), Replaces: old.ID},
	} {
		e.t.Run(name, func(t *testing.T) {
			sub := *e
			sub.t = t
			_, _, err := sub.s.Enqueue(sub.ctx, n)
			sub.requireErr(err, execution.ErrInvalidRequest)
		})
	}
	e.requireHeadJobs(spec, old.ID)
	e.requireHeadJobs(canonicalSpec(2), other.ID)
	e.requireHeadJobs(canonicalSpec(3), pending.ID)
	e.requireHeadJobs(canonicalSpec(4), cancelled.ID)
	e.requireHeadJobs(receiptSpec(5), noMergeBase.ID)
}

// rpUnblockAfter is the unblock after the replacement: the replacement of a job makes the unblock that observed it fail as changed, with nothing written, whichever
// reason the job ended for and whatever merge base the replacement has. A replacement that was cancelled since, and a job of another binding, do not replace it.
func rpUnblockAfter(e *env) {
	comment := int64(0)
	requireChanged := func(job execution.Job) {
		e.t.Helper()
		comment++
		entry := ledgerEntry(comment, commands.Unblocked, time.Minute)
		e.requireErr(e.s.RecordUnblock(e.ctx, entry, ack("ack-changed"), job.HeadSHA, unblockJobs(job), entry.DecidedAt), execution.ErrJobChanged)
		e.requireUnblocks(entry.CommentID)
		e.requireNoEntry(entry.RepositoryID, entry.CommentID)
		e.requireJob(job)
	}
	replace := func(spec execution.Spec, old execution.Job) execution.Job {
		e.t.Helper()
		fresh, created, err := e.s.Enqueue(e.ctx, replacing(spec, old, 0, time.Hour))
		if err != nil || !created || fresh.ID == old.ID {
			e.t.Fatalf("the replacement of job %d = job %d, created %v, error %v; want a new job", old.ID, fresh.ID, created, err)
		}
		return fresh
	}

	// A job that the sweep ended for no-executor, and one that ended for executor-not-configured: each is replaced, and the unblock of the old job is a change.
	ended := e.endedForNoExecutor(canonicalSpec(2))
	replace(canonicalSpec(2), ended)
	requireChanged(ended)
	spec := canonicalSpec(1)
	old := e.enqueueFinal(spec, execution.ReasonNotConfigured, 0)
	fresh := replace(spec, old)
	requireChanged(old)

	// A receipt job is replaced with the merge base of its time, which its key holds: the replacement has another key than the job it replaces.
	receipt := e.enqueueFinal(withBase(receiptSpec(1), ""), execution.ReasonNotConfigured, 0)
	if replaced := replace(receiptSpec(1), receipt); replaced.Key == receipt.Key {
		e.t.Fatalf("the replacement of a receipt job has the key %s of the job it replaces; want another one", replaced.Key)
	}
	requireChanged(receipt)

	// A replacement that was cancelled replaces nothing: the gates read the old job again, and the owner may unblock it.
	_, err := e.s.Cancel(e.ctx, fresh.ID, execution.ReasonObsolete, at(2*time.Hour))
	e.must(err)
	comment++
	entry := ledgerEntry(comment, commands.Unblocked, 3*time.Hour)
	e.must(e.s.RecordUnblock(e.ctx, entry, nil, old.HeadSHA, unblockJobs(old), entry.DecidedAt))
	e.requireUnblocks(entry.CommentID, old.ID)

	// A newer job of another binding (another governing manifest) is not a replacement: the unblock of the older job still takes effect.
	third := canonicalSpec(3)
	third.GoverningDigest = digest("an earlier manifest")
	earlier := e.enqueueFinal(third, execution.ReasonNotConfigured, 0)
	e.enqueue(canonicalSpec(3), time.Minute)
	comment++
	entry = ledgerEntry(comment, commands.Unblocked, 3*time.Hour)
	e.must(e.s.RecordUnblock(e.ctx, entry, nil, earlier.HeadSHA, unblockJobs(earlier), entry.DecidedAt))
	e.requireUnblocks(entry.CommentID, earlier.ID)
}

// rpRace is the unblock against the replacement, raced from the start: exactly one of the two takes effect for each job that the sweep ended for no-executor. The unblock
// wins and the replacement is held, or the replacement wins and the unblock fails as changed.
func rpRace(e *env) {
	const rounds = 12
	for i := range rounds {
		spec := canonicalSpec(10 + i)
		old := e.endedForNoExecutor(spec)
		entry := ledgerEntry(int64(10+i), commands.Unblocked, time.Minute)
		var replacement execution.Job
		var created bool
		var replaceErr, unblockErr error
		parallel(2, func(k int) {
			if k == 0 {
				replacement, created, replaceErr = e.s.Enqueue(e.ctx, replacing(spec, old, 0, time.Hour))
				return
			}
			unblockErr = e.s.RecordUnblock(e.ctx, entry, nil, spec.HeadSHA, unblockJobs(old), entry.DecidedAt)
		})
		e.must(replaceErr)
		switch {
		case !created && replacement.ID == old.ID && unblockErr == nil:
			e.requireUnblocks(entry.CommentID, old.ID)
			e.requireHeadJobs(spec, old.ID)
		case created && replacement.ID != old.ID && errors.Is(unblockErr, execution.ErrJobChanged):
			e.requireUnblocks(entry.CommentID)
			e.requireHeadJobs(spec, old.ID, replacement.ID)
		default:
			e.t.Fatalf("round %d: replacement = job %d (created %v), unblock = %v; want one winner: the unblock and a held replacement, or a replacement and a changed unblock", i, replacement.ID, created, unblockErr)
		}
	}
}
