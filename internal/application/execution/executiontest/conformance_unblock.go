package executiontest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

// The owner's unblock (docs/contracts/persistence.md, "Changes to pr_commands", RecordUnblock): it lifts the jobs it names, and since M2.1b it ends the
// open ones (pending or leased) for no-executor, but only while each is still as the caller observed it.

// unblockAttempt is a call of RecordUnblock.
type unblockAttempt struct {
	entry commands.Entry
	head  string
	jobs  []execution.UnblockJob
	at    time.Time
}

// unblockJobs are jobs as the caller of an unblock observed them: the state and the lease id they have now.
func unblockJobs(jobs ...execution.Job) []execution.UnblockJob {
	observed := make([]execution.UnblockJob, len(jobs))
	for i, job := range jobs {
		observed[i] = execution.UnblockJob{ID: job.ID, State: job.State, LeaseID: job.LeaseID}
	}
	return observed
}

// observedAs is the job as the caller observed it, with another state and lease id than the job has.
func observedAs(job execution.Job, state execution.State, lease string) execution.UnblockJob {
	return execution.UnblockJob{ID: job.ID, State: state, LeaseID: lease}
}

func (e *env) requireDecided(comment, job int64, want time.Duration) {
	e.t.Helper()
	got, found, err := e.env.UnblockDecided(e.ctx, 10, comment, job)
	e.must(err)
	if !found || !got.Equal(at(want)) {
		e.t.Fatalf("the unblock row of comment %d for job %d was decided at %v (found %v); want %v", comment, job, got, found, at(want))
	}
}

func ubUnblock(e *env) {
	// Two unavailable jobs of one head, one of another head, and a pending one.
	head := sha(1)
	canonical := e.enqueueFinal(canonicalSpec(1), execution.ReasonNotConfigured, 0)
	receipt := e.enqueueFinal(withBase(receiptSpec(1), ""), execution.ReasonNoMergeBase, 0)
	other := e.enqueueFinal(canonicalSpec(2), execution.ReasonNotConfigured, 0)
	pending := e.enqueue(canonicalSpec(3), 0)
	e.requireUnblocked(canonical.ID, 0, false)

	// A refused unblock writes nothing: no entry, no acknowledgment and no row.
	onPull2 := ledgerEntry(0, commands.Unblocked, 0)
	onPull2.PullNumber = 2
	inRepository20 := ledgerEntry(0, commands.Unblocked, 0)
	inRepository20.RepositoryID = 20
	entry := ledgerEntry(0, commands.Unblocked, 0)
	jobs := unblockJobs(canonical)
	refused := map[string]unblockAttempt{
		"a job of another head":                 {entry, head, unblockJobs(other), entry.DecidedAt},
		"a job of another pull":                 {onPull2, head, jobs, onPull2.DecidedAt},
		"a job of another repository":           {inRepository20, head, jobs, inRepository20.DecidedAt},
		"an unknown job":                        {entry, head, []execution.UnblockJob{{ID: 987654, State: execution.StateUnavailable}}, entry.DecidedAt},
		"no job":                                {entry, head, nil, entry.DecidedAt},
		"the same job twice":                    {entry, head, append(unblockJobs(canonical), unblockJobs(canonical)...), entry.DecidedAt},
		"a good job and one of another head":    {entry, head, append(unblockJobs(canonical), unblockJobs(other)...), entry.DecidedAt},
		"a malformed head":                      {entry, "abc", jobs, entry.DecidedAt},
		"another disposition":                   {ledgerEntry(0, commands.Processed, 0), head, jobs, entry.DecidedAt},
		"the disposition of a refusal":          {ledgerEntry(0, commands.UnblockRefused, 0), head, jobs, entry.DecidedAt},
		"no time":                               {entry, head, jobs, time.Time{}},
		"a job observed as complete":            {entry, head, []execution.UnblockJob{observedAs(canonical, execution.StateComplete, "")}, entry.DecidedAt},
		"a job observed as cancelled":           {entry, head, []execution.UnblockJob{observedAs(canonical, execution.StateCancelled, "")}, entry.DecidedAt},
		"a job observed in no state":            {entry, head, []execution.UnblockJob{observedAs(canonical, "", "")}, entry.DecidedAt},
		"a job observed with a malformed lease": {entry, head, []execution.UnblockJob{observedAs(canonical, execution.StateUnavailable, "not a lease")}, entry.DecidedAt},
	}
	comment := int64(100)
	for name, a := range refused {
		comment++
		a.entry.CommentID = comment
		e.t.Run(name, func(t *testing.T) {
			sub := *e
			sub.t = t
			sub.requireErr(sub.s.RecordUnblock(sub.ctx, a.entry, ack("ack-refused"), a.head, a.jobs, a.at), execution.ErrInvalidRequest)
			sub.requireUnblocks(a.entry.CommentID)
			sub.requireNoEntry(a.entry.RepositoryID, a.entry.CommentID)
		})
	}
	e.requireOutbox()
	invalid := ledgerEntry(500, commands.Unblocked, 0)
	invalid.AuthorID = 0
	e.requireErr(e.s.RecordUnblock(e.ctx, invalid, nil, head, unblockJobs(canonical), invalid.DecidedAt), commands.ErrInvalid)

	// An unblock lifts the jobs it names, with its entry and its acknowledgment.
	lifted := ledgerEntry(1, commands.Unblocked, 10*time.Minute)
	e.must(e.s.RecordUnblock(e.ctx, lifted, ack("ack-1"), head, unblockJobs(canonical, receipt), lifted.DecidedAt))
	e.requireEntry(lifted)
	e.requireUnblocks(1, canonical.ID, receipt.ID)
	e.requireOutbox("ack-1")
	// The row binds to the job, not to the head, and honors since: the entry was created at 10 minutes.
	for _, id := range []int64{canonical.ID, receipt.ID} {
		e.requireUnblocked(id, 0, true)
		e.requireUnblocked(id, 10*time.Minute, true)
		e.requireUnblocked(id, 10*time.Minute+time.Microsecond, false)
	}
	for _, id := range []int64{other.ID, pending.ID, 987654} {
		e.requireUnblocked(id, 0, false)
	}
	// A job that was unavailable already is lifted as it is: the unblock changes none.
	e.requireJob(canonical)
	e.requireJob(receipt)
	// A job created later for the same head is another job, and the unblock does not carry over.
	later := e.enqueue(canonicalSpec(1), 20*time.Minute)
	e.requireUnblocked(later.ID, 0, false)

	// The same unblock again is success and writes nothing more; another one for the comment conflicts.
	e.must(e.s.RecordUnblock(e.ctx, lifted, ack("ack-1"), head, unblockJobs(canonical, receipt), lifted.DecidedAt))
	e.requireErr(e.s.RecordUnblock(e.ctx, lifted, nil, head, unblockJobs(canonical), lifted.DecidedAt), commands.ErrConflict)
	differs := lifted
	differs.DecidedAt = differs.DecidedAt.Add(time.Minute)
	e.requireErr(e.s.RecordUnblock(e.ctx, differs, nil, head, unblockJobs(canonical, receipt), differs.DecidedAt), commands.ErrConflict)
	e.requireErr(e.s.RecordUnblock(e.ctx, lifted, nil, head, unblockJobs(canonical, receipt), lifted.DecidedAt.Add(time.Minute)), commands.ErrConflict) // another time
	e.requireUnblocks(1, canonical.ID, receipt.ID)
	e.requireOutbox("ack-1")
}

// ubEnds is the early unblock of M2.1b: it ends the open jobs it lifts as unavailable for no-executor, at the time of the command, with no results row and no
// failure counted, and a leased job keeps its lease id, so that its late result and its heartbeat are refused.
func ubEnds(e *env) {
	head := sha(1)
	old := canonicalSpec(1)
	old.GoverningDigest = digest("an earlier manifest")
	unavailable := e.enqueueFinal(old, execution.ReasonNotConfigured, 0)
	toLease := e.enqueue(receiptSpec(1), 0)
	pending := e.enqueue(canonicalSpec(1), time.Minute)
	leased := e.mustLease(toLease.ID, 30*time.Second)
	now := 40 * time.Second
	entry := ledgerEntry(1, commands.Unblocked, now-time.Second)

	e.must(e.s.RecordUnblock(e.ctx, entry, ack("ack-1"), head, unblockJobs(unavailable, pending, leased), at(now)))

	e.requireJob(ended(pending, execution.StateUnavailable, execution.ReasonNoExecutor, now))
	e.requireJob(ended(leased, execution.StateUnavailable, execution.ReasonNoExecutor, now)) // the lease id and the time of the lease stay
	e.requireJob(unavailable)
	e.requireNoResult(pending.ID)
	e.requireNoResult(leased.ID)
	e.requireUnblocks(1, unavailable.ID, leased.ID, pending.ID) // in the order of the job ids
	for _, id := range []int64{pending.ID, leased.ID, unavailable.ID} {
		e.requireDecided(1, id, now)
		e.requireUnblocked(id, 0, true)
	}
	// What the command ended is the jobs whose end is the time of their row: the job that was unavailable already ended earlier.
	if got := e.get(unavailable.ID).FinishedAt; got.Equal(at(now)) {
		e.t.Fatalf("the job that was unavailable already has the finish %v of the command", got)
	}
	e.requireCounts(0, 0, 0, 3, 0)
	// The old lease of an ended job: its result is a conflict and its heartbeat a lost lease.
	_, err := e.s.Complete(e.ctx, leased.ID, completeReceipt(leased.LeaseID, "late"), at(now+time.Second), githubWait)
	e.requireErr(err, execution.ErrConflict)
	_, err = e.s.Heartbeat(e.ctx, leased.ID, leased.LeaseID, at(now+time.Second))
	e.requireErr(err, execution.ErrLeaseLost)
	e.requireNoResult(leased.ID)
	// Nothing is left to lease.
	if job, found := e.lease(time.Hour); found {
		e.t.Fatalf("Lease = job %d; want none: every job ended", job.ID)
	}
	// The same command again is success and leaves every job as it was, whatever the observed states are now; the command with another time conflicts.
	e.must(e.s.RecordUnblock(e.ctx, entry, ack("ack-1"), head, unblockJobs(unavailable, pending, leased), at(now)))
	e.requireJob(ended(pending, execution.StateUnavailable, execution.ReasonNoExecutor, now))
	e.requireErr(e.s.RecordUnblock(e.ctx, entry, nil, head, unblockJobs(unavailable, pending, leased), at(now+time.Second)), commands.ErrConflict)
	e.requireOutbox("ack-1")
}

// ubChanged is the race that the observed states close: a job that is no longer as the caller saw it ends nothing and writes nothing, and a set with one such
// job ends none of its jobs.
func ubChanged(e *env) {
	n := 0
	comment := int64(0)
	pendingJob := func(spec execution.Spec) execution.Job { return e.enqueue(spec, 0) }
	leasedJob := func(spec execution.Spec) execution.Job { return e.mustLease(e.enqueue(spec, 0).ID, time.Second) }
	unavailableJob := func(spec execution.Spec) execution.Job { return e.enqueueFinal(spec, execution.ReasonNotConfigured, 0) }
	completedJob := func(spec execution.Spec) execution.Job {
		job := leasedJob(spec)
		_, err := e.s.Complete(e.ctx, job.ID, completeCanonical(job.LeaseID, "body"+spec.HeadSHA), at(2*time.Second), githubWait)
		e.must(err)
		return e.get(job.ID)
	}
	// A lease request that found GitHub unreadable leaves the job pending, with its lease id, and waiting for GitHub: it no longer waits for the runner.
	githubWaitJob := func(spec execution.Spec) execution.Job {
		job := e.enqueue(spec, 0)
		_, _, err := e.s.Lease(e.ctx, e.leaseRequest(time.Second), func(context.Context, execution.Job) error { return execution.ErrGitHubUnavailable })
		e.requireErr(err, execution.ErrGitHubUnavailable)
		return job
	}
	cancelledJob := func(spec execution.Spec) execution.Job {
		job := e.enqueue(spec, 0)
		_, err := e.s.Cancel(e.ctx, job.ID, execution.ReasonHeadMoved, at(time.Second))
		e.must(err)
		return e.get(job.ID)
	}
	otherLease := e.newLeaseID()
	// The jobs that are leased or completed come first: a lease takes the oldest ready job, so a pending job left behind would be taken instead.
	changed := []struct {
		name     string
		make     func(execution.Spec) execution.Job
		observed func(execution.Job) execution.UnblockJob
	}{
		{"a pending job that was leased", leasedJob, func(j execution.Job) execution.UnblockJob { return observedAs(j, execution.StatePending, "") }},
		{"a leased job that another lease holds", leasedJob, func(j execution.Job) execution.UnblockJob { return observedAs(j, execution.StateLeased, otherLease) }},
		{"a leased job observed with no lease", leasedJob, func(j execution.Job) execution.UnblockJob { return observedAs(j, execution.StateLeased, "") }},
		{"a job that completed", completedJob, func(j execution.Job) execution.UnblockJob { return observedAs(j, execution.StatePending, "") }},
		{"a job that completed, observed as leased", completedJob, func(j execution.Job) execution.UnblockJob { return observedAs(j, execution.StateLeased, j.LeaseID) }},
		{"a pending job that waits for GitHub now", githubWaitJob, func(j execution.Job) execution.UnblockJob { return observedAs(j, execution.StatePending, j.LeaseID) }},
		{"a pending job that is unavailable now", pendingJob, func(j execution.Job) execution.UnblockJob { return observedAs(j, execution.StateUnavailable, "") }},
		{"an unavailable job that is pending now", unavailableJob, func(j execution.Job) execution.UnblockJob { return observedAs(j, execution.StatePending, "") }},
		{"a job that was cancelled", cancelledJob, func(j execution.Job) execution.UnblockJob { return observedAs(j, execution.StatePending, "") }},
	}
	for _, c := range changed {
		n++
		head := sha(n)
		before := c.make(canonicalSpec(n))
		before = e.get(before.ID)
		comment++
		entry := ledgerEntry(comment, commands.Unblocked, time.Minute)
		e.t.Run(c.name, func(t *testing.T) {
			sub := *e
			sub.t = t
			sub.requireErr(sub.s.RecordUnblock(sub.ctx, entry, ack("ack-changed"), head, []execution.UnblockJob{c.observed(before)}, entry.DecidedAt), execution.ErrJobChanged)
			sub.requireJob(before)
			sub.requireUnblocks(entry.CommentID)
			sub.requireNoEntry(entry.RepositoryID, entry.CommentID)
		})
	}
	e.requireOutbox()

	// A set with one job that changed ends none of its jobs: the good job of the head stays pending.
	n++
	head := sha(n)
	good := e.enqueue(canonicalSpec(n), 0)
	gone := canonicalSpec(n)
	gone.GoverningDigest = digest("an earlier manifest")
	changedJob := e.enqueueFinal(gone, execution.ReasonNotConfigured, 0)
	comment++
	entry := ledgerEntry(comment, commands.Unblocked, time.Minute)
	e.requireErr(e.s.RecordUnblock(e.ctx, entry, ack("ack-set"), head,
		[]execution.UnblockJob{observedAs(good, execution.StatePending, ""), observedAs(changedJob, execution.StatePending, "")}, entry.DecidedAt), execution.ErrJobChanged)
	e.requireJob(good)
	e.requireUnblocks(entry.CommentID)
	e.requireNoEntry(entry.RepositoryID, entry.CommentID)
	e.requireOutbox()
}

// ubLeaseRace is the unblock against a lease: a lease that holds a pending job while it reads GitHub makes the unblock wait, and the unblock then finds the new
// lease and ends nothing; and, whichever of the two comes first, a pending job is never both leased and ended.
func ubLeaseRace(e *env) {
	job := e.enqueue(canonicalSpec(1), 0)
	observed := unblockJobs(job)
	holding, release, leased := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, _, err := e.s.Lease(e.ctx, e.leaseRequest(time.Second), func(context.Context, execution.Job) error {
			close(holding)
			<-release
			return nil
		})
		leased <- err
	}()
	<-holding
	entry := ledgerEntry(1, commands.Unblocked, time.Minute)
	unblocked := make(chan error, 1)
	go func() {
		unblocked <- e.s.RecordUnblock(e.ctx, entry, ack("ack-1"), job.HeadSHA, observed, entry.DecidedAt)
	}()
	time.Sleep(100 * time.Millisecond) // the unblock is waiting for the job, or has not started: either way it must not end a job that a lease holds
	close(release)
	e.must(<-leased)
	e.requireErr(<-unblocked, execution.ErrJobChanged)
	got := e.get(job.ID)
	if got.State != execution.StateLeased || got.Attempts != 1 {
		e.t.Fatalf("job = %+v; want it leased by the lease that held it", got)
	}
	e.requireUnblocks(1)
	e.requireNoEntry(entry.RepositoryID, entry.CommentID)

	// Raced from the start, exactly one of the two wins each pending job.
	const rounds = 12
	for i := range rounds {
		next := e.enqueue(canonicalSpec(10+i), time.Minute)
		entry := ledgerEntry(int64(10+i), commands.Unblocked, time.Minute)
		var found bool
		var leaseErr, unblockErr error
		parallel(2, func(k int) {
			if k == 0 {
				_, found, leaseErr = e.s.Lease(e.ctx, e.leaseRequest(2*time.Minute), trees)
				return
			}
			unblockErr = e.s.RecordUnblock(e.ctx, entry, nil, next.HeadSHA, unblockJobs(next), entry.DecidedAt)
		})
		e.must(leaseErr)
		now := e.get(next.ID)
		switch {
		case found && errors.Is(unblockErr, execution.ErrJobChanged) && now.State == execution.StateLeased:
		case !found && unblockErr == nil && now.State == execution.StateUnavailable && now.Reason == execution.ReasonNoExecutor:
		default:
			e.t.Fatalf("round %d: lease found %v, unblock = %v, job %+v; want one winner: leased and refused, or ended and not leased", i, found, unblockErr, now)
		}
	}
}
