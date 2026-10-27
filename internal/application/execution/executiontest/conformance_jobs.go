package executiontest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// parallel runs f for 0 to n-1 at once. f must not stop the test: it reports through the slices it fills.
func parallel(n int, f func(i int)) {
	var ready, done sync.WaitGroup
	ready.Add(n)
	start := make(chan struct{})
	for i := range n {
		done.Go(func() {
			ready.Done()
			<-start
			f(i)
		})
	}
	ready.Wait()
	close(start)
	done.Wait()
}

func expireParams(now, lastSeen time.Duration) execution.ExpireParams {
	return execution.ExpireParams{Now: at(now), LastSeenAt: at(lastSeen), LeaseWait: leaseWait, GitHubWait: githubWait}
}

func (e *env) expire(p execution.ExpireParams) int {
	e.t.Helper()
	n, err := e.s.Expire(e.ctx, p)
	e.must(err)
	return n
}

// leaseWith asks for a job with a job timeout of its own.
func (e *env) leaseWith(now, timeout time.Duration) (execution.Job, bool) {
	e.t.Helper()
	req := e.leaseRequest(now)
	req.JobTimeout = timeout
	job, found, err := e.s.Lease(e.ctx, req, trees)
	e.must(err)
	return job, found
}

// failed is the job after an attempt that failed with a retried reason at the time now, with the failure counted and the backoff given.
func failed(job execution.Job, reason execution.Reason, now, backoff time.Duration) execution.Job {
	job.State, job.Reason, job.Failures = execution.StatePending, reason, job.Failures+1
	job.NotBefore, job.LeaseExpiresAt, job.DeadlineAt, job.GitHubSince = at(now+backoff), time.Time{}, time.Time{}, time.Time{}
	return job
}

// ended is the job after it became final at the time now.
func ended(job execution.Job, state execution.State, reason execution.Reason, now time.Duration) execution.Job {
	job.State, job.Reason, job.FinishedAt = state, reason, at(now)
	job.LeaseExpiresAt, job.DeadlineAt = time.Time{}, time.Time{}
	return job
}

func withBase(s execution.Spec, base string) execution.Spec {
	s.BaseSHA = base
	return s
}

// requireCurrent requires the current job of spec's key to be the job with this id.
func (e *env) requireCurrent(spec execution.Spec, id int64) {
	e.t.Helper()
	got, found, err := e.s.CurrentJob(e.ctx, keyOf(spec))
	e.must(err)
	if !found || got.ID != id {
		e.t.Fatalf("CurrentJob = job %d, found %v; want job %d", got.ID, found, id)
	}
}

func (e *env) requireCounts(pending, leased, complete, unavailable, cancelled int) {
	e.t.Helper()
	got, err := e.s.Counts(e.ctx)
	e.must(err)
	want := map[execution.State]int{execution.StatePending: pending, execution.StateLeased: leased, execution.StateComplete: complete,
		execution.StateUnavailable: unavailable, execution.StateCancelled: cancelled}
	if len(got) != len(want) {
		e.t.Fatalf("Counts = %v; want %v", got, want)
	}
	for state, n := range want {
		if got[state] != n {
			e.t.Fatalf("Counts = %v; want %v", got, want)
		}
	}
}

func jbEnqueue(e *env) {
	spec := receiptSpec(1)
	job := e.enqueue(spec, 3*time.Minute)
	if job.ID <= 0 {
		e.t.Fatalf("job id = %d; want a positive one", job.ID)
	}
	want := execution.Job{Spec: spec, ID: job.ID, Key: keyOf(spec), State: execution.StatePending, NotBefore: at(3 * time.Minute), CreatedAt: at(3 * time.Minute)}
	e.requireJob(want)
	e.requireCurrent(spec, job.ID)
	// A canonical job keeps no paths, and a time keeps the microseconds that PostgreSQL stores.
	canonical := canonicalSpec(2)
	precise := e.enqueue(canonical, 5*time.Minute+789*time.Nanosecond)
	e.requireJob(execution.Job{Spec: canonical, ID: precise.ID, Key: keyOf(canonical), State: execution.StatePending, NotBefore: at(5 * time.Minute), CreatedAt: at(5 * time.Minute)})
	// The longest lists are kept.
	long := receiptSpec(3)
	long.UnapprovedPaths, long.ChangedPaths = nil, nil
	for range 100 {
		long.UnapprovedPaths = append(long.UnapprovedPaths, strings.Repeat("u", 1024))
		long.ChangedPaths = append(long.ChangedPaths, strings.Repeat("c", 1024))
	}
	long.ChangedTotal = 250
	kept := e.enqueue(long, 0)
	e.requireJob(execution.Job{Spec: long, ID: kept.ID, Key: keyOf(long), State: execution.StatePending, NotBefore: at(0), CreatedAt: at(0)})
}

func jbEnqueueIdempotent(e *env) {
	spec := canonicalSpec(1)
	first := e.enqueue(spec, 0)
	again, created, err := e.s.Enqueue(e.ctx, execution.NewJob{Spec: spec, At: at(5 * time.Minute)})
	e.must(err)
	if created || again.ID != first.ID {
		e.t.Fatalf("a second Enqueue = job %d, created %v; want job %d, not created", again.ID, created, first.ID)
	}
	e.requireJob(first) // nothing was rewritten
	// Each line of the key makes another job.
	manifest, declaration := spec, spec
	manifest.GoverningDigest, declaration.DeclarationDigest = digest("other manifest"), digest("other declaration")
	ids := map[int64]string{first.ID: "the first job"}
	for name, s := range map[string]execution.Spec{"head": canonicalSpec(2), "plan": receiptSpec(1), "base": withBase(receiptSpec(1), sha(77)), "manifest": manifest, "declaration": declaration} {
		job := e.enqueue(s, 0)
		if clash := ids[job.ID]; clash != "" {
			e.t.Fatalf("the %s job is job %d, which is %s", name, job.ID, clash)
		}
		ids[job.ID] = name
	}
	// Concurrent calls create one job.
	racing := canonicalSpec(9)
	const callers = 8
	results := make([]execution.Job, callers)
	made := make([]bool, callers)
	failures := make([]error, callers)
	parallel(callers, func(i int) {
		results[i], made[i], failures[i] = e.s.Enqueue(e.ctx, execution.NewJob{Spec: racing, At: at(0)})
	})
	created8 := 0
	for i := range callers {
		e.must(failures[i])
		if made[i] {
			created8++
		}
		if results[i].ID != results[0].ID {
			e.t.Fatalf("callers got jobs %d and %d for one key", results[0].ID, results[i].ID)
		}
	}
	if created8 != 1 {
		e.t.Fatalf("%d callers created the job; want one", created8)
	}
}

func jbKeyFreedBy(e *env) {
	// A cancelled job.
	spec := canonicalSpec(1)
	old := e.enqueue(spec, 0)
	changed, err := e.s.Cancel(e.ctx, old.ID, execution.ReasonHeadMoved, at(time.Minute))
	e.must(err)
	if !changed {
		e.t.Fatal("Cancel of a pending job reported no change")
	}
	fresh := e.enqueue(spec, 2*time.Minute)
	e.requireJob(ended(old, execution.StateCancelled, execution.ReasonHeadMoved, time.Minute))
	e.requireCurrent(spec, fresh.ID)

	// A job unavailable for executor-not-configured.
	spec = canonicalSpec(2)
	notConfigured := e.enqueueFinal(spec, execution.ReasonNotConfigured, 0)
	pending := e.enqueue(spec, time.Minute)
	e.requireJob(ended(notConfigured, execution.StateUnavailable, execution.ReasonNotConfigured, 0))
	e.requireCurrent(spec, pending.ID)

	// The other jobs that were created final stay current: no-merge-base and github-unavailable do not make way for a new job.
	for i, reason := range []execution.Reason{execution.ReasonNoMergeBase, execution.ReasonGitHubUnavailable} {
		s := withBase(receiptSpec(10+i), "")
		job := e.enqueueFinal(s, reason, 0)
		same, created, err := e.s.Enqueue(e.ctx, execution.NewJob{Spec: s, Unavailable: reason, At: at(time.Minute)})
		e.must(err)
		if created || same.ID != job.ID {
			e.t.Fatalf("Enqueue over a job unavailable for %s = job %d, created %v; want job %d", reason, same.ID, created, job.ID)
		}
	}
}

func jbKeyFreedByNoExecutor(e *env) {
	spec := canonicalSpec(1)
	old := e.enqueue(spec, 0)
	if n := e.expire(expireParams(leaseWait, 0)); n != 1 {
		e.t.Fatalf("Expire changed %d jobs; want one", n)
	}
	e.requireJob(ended(old, execution.StateUnavailable, execution.ReasonNoExecutor, leaseWait))
	e.requireNoResult(old.ID)
	// The owner unblocked the old job: the unblock stays with it and does not carry over to the fresh job.
	e.must(e.s.RecordUnblock(e.ctx, ledgerEntry(1, commands.Unblocked, 20*time.Minute), nil, spec.HeadSHA, []int64{old.ID}))
	fresh := e.enqueue(spec, 21*time.Minute)
	e.requireCurrent(spec, fresh.ID)
	e.requireJob(ended(old, execution.StateUnavailable, execution.ReasonNoExecutor, leaseWait))
	e.requireUnblocked(old.ID, 20*time.Minute, true)
	e.requireUnblocked(fresh.ID, 0, false)
}

func jbCreatedFinal(e *env) {
	for i, reason := range []execution.Reason{execution.ReasonNoMergeBase, execution.ReasonNotConfigured, execution.ReasonGitHubUnavailable} {
		spec := withBase(receiptSpec(i+1), "")
		job := e.enqueueFinal(spec, reason, 5*time.Minute)
		want := execution.Job{Spec: spec, ID: job.ID, Key: keyOf(spec), State: execution.StateUnavailable, Reason: reason, NotBefore: at(5 * time.Minute), CreatedAt: at(5 * time.Minute), FinishedAt: at(5 * time.Minute)}
		e.requireJob(want)
		e.requireNoResult(job.ID) // a job created final has no results row
	}
	// A canonical job without an executor is created the same way.
	canonical := e.enqueueFinal(canonicalSpec(9), execution.ReasonNotConfigured, 0)
	if canonical.State != execution.StateUnavailable || canonical.BaseSHA != "" {
		e.t.Fatalf("canonical job = %+v; want unavailable with no base", canonical)
	}
	// Nothing is ever leased from them.
	if job, found := e.lease(time.Hour); found {
		e.t.Fatalf("Lease = job %d; want none: every job is final", job.ID)
	}
}

func jbEnqueueInvalid(e *env) {
	canonical := func(n *execution.NewJob) { n.Spec = canonicalSpec(1) }
	paths := func(n int, size int) []string {
		list := make([]string, n)
		for i := range list {
			list[i] = strings.Repeat("p", size)
		}
		return list
	}
	bad := map[string]func(*execution.NewJob){
		"no repository":                                func(n *execution.NewJob) { n.RepositoryID = 0 },
		"no pull":                                      func(n *execution.NewJob) { n.PullNumber = 0 },
		"a pull past int32":                            func(n *execution.NewJob) { n.PullNumber = 1 << 31 },
		"no plan":                                      func(n *execution.NewJob) { n.Plan = "" },
		"an unknown plan":                              func(n *execution.NewJob) { n.Plan = "both" },
		"another profile":                              func(n *execution.NewJob) { n.Profile = "python" },
		"no profile":                                   func(n *execution.NewJob) { n.Profile = "" },
		"a short head":                                 func(n *execution.NewJob) { n.HeadSHA = sha(1)[:39] },
		"an upper-case head":                           func(n *execution.NewJob) { n.HeadSHA = strings.Repeat("A", 40) },
		"a head that is not hexadecimal":               func(n *execution.NewJob) { n.HeadSHA = strings.Repeat("g", 40) },
		"a receipt without a base":                     func(n *execution.NewJob) { n.BaseSHA = "" },
		"a malformed base":                             func(n *execution.NewJob) { n.BaseSHA = sha(5)[:20] },
		"a canonical job with a base":                  func(n *execution.NewJob) { canonical(n); n.BaseSHA = sha(5) },
		"a created-final receipt with a base":          func(n *execution.NewJob) { n.Unavailable = execution.ReasonNoMergeBase },
		"no governing digest":                          func(n *execution.NewJob) { n.GoverningDigest = artifact.Digest{} },
		"no declaration digest":                        func(n *execution.NewJob) { n.DeclarationDigest = artifact.Digest{} },
		"no governing source":                          func(n *execution.NewJob) { n.GoverningSource = "" },
		"a version of zero":                            func(n *execution.NewJob) { n.GoverningSource = "V0" },
		"a lower-case version":                         func(n *execution.NewJob) { n.GoverningSource = "v1" },
		"a proposal without a revision":                func(n *execution.NewJob) { n.GoverningSource = "P1" },
		"no canonical level":                           func(n *execution.NewJob) { n.CanonicalLevel = 0 },
		"no receipt level":                             func(n *execution.NewJob) { n.ReceiptLevel = 0 },
		"an unknown receipt level":                     func(n *execution.NewJob) { n.ReceiptLevel = 9 },
		"a canonical job with the canonical level off": func(n *execution.NewJob) { canonical(n); n.CanonicalLevel = domain.CanonicalLevelOff },
		"a receipt job with the receipt level off":     func(n *execution.NewJob) { n.ReceiptLevel = domain.ReceiptLevelOff },
		"a canonical job with unapproved paths":        func(n *execution.NewJob) { canonical(n); n.UnapprovedPaths = []string{"a_test.go"} },
		"a canonical job with changed paths":           func(n *execution.NewJob) { canonical(n); n.ChangedPaths = []string{"a_test.go"} },
		"101 unapproved paths":                         func(n *execution.NewJob) { n.UnapprovedPaths = paths(101, 1) },
		"101 changed paths":                            func(n *execution.NewJob) { n.ChangedPaths = paths(101, 1) },
		"an unapproved path of 1025 bytes":             func(n *execution.NewJob) { n.UnapprovedPaths = paths(1, 1025) },
		"a changed path of 1025 bytes":                 func(n *execution.NewJob) { n.ChangedPaths = paths(1, 1025) },
		"a negative changed total":                     func(n *execution.NewJob) { n.ChangedTotal = -1 },
		"an unavailable reason of the executor":        func(n *execution.NewJob) { n.BaseSHA, n.Unavailable = "", execution.ReasonExecutorError },
		"an unavailable reason of the lease":           func(n *execution.NewJob) { n.BaseSHA, n.Unavailable = "", execution.ReasonLeaseExpired },
		"an unavailable reason of no executor":         func(n *execution.NewJob) { n.BaseSHA, n.Unavailable = "", execution.ReasonNoExecutor },
		"a cancel reason as the unavailable reason":    func(n *execution.NewJob) { n.BaseSHA, n.Unavailable = "", execution.ReasonClosed },
		"no creation time":                             func(n *execution.NewJob) { n.At = time.Time{} },
	}
	requireInvalid(e, func() execution.NewJob { return execution.NewJob{Spec: receiptSpec(1), At: at(0)} }, bad, func(n execution.NewJob) error {
		_, _, err := e.s.Enqueue(e.ctx, n)
		return err
	})
	e.requireCounts(0, 0, 0, 0, 0)
}

func jbLeaseGrant(e *env) {
	job := e.enqueue(canonicalSpec(1), 0)
	leased, found := e.lease(2 * time.Minute)
	if !found || leased.ID != job.ID {
		e.t.Fatalf("Lease = job %d, found %v; want job %d", leased.ID, found, job.ID)
	}
	want := job
	want.State, want.Attempts, want.LeaseID = execution.StateLeased, 1, e.leaseID(1)
	want.LeasedAt, want.LeaseExpiresAt, want.DeadlineAt = at(2*time.Minute), at(2*time.Minute+leaseTTL), at(2*time.Minute+jobTimeout)
	if d := diff(leased, want, "leased job"); d != "" {
		e.t.Fatal(d)
	}
	e.requireJob(want)
	if _, found := e.lease(2*time.Minute + time.Second); found {
		e.t.Fatal("a leased job whose lease is valid was leased again")
	}

	// A job timeout shorter than the lease caps the lease at the deadline. The first job's lease has expired by then, which Lease applies.
	short := e.enqueue(canonicalSpec(2), 3*time.Minute)
	got, found := e.leaseWith(4*time.Minute, 30*time.Second)
	if !found || got.ID != short.ID || !got.LeaseExpiresAt.Equal(at(4*time.Minute+30*time.Second)) || !got.DeadlineAt.Equal(at(4*time.Minute+30*time.Second)) {
		e.t.Fatalf("Lease with a 30 s timeout = job %d, expires %v, deadline %v; want job %d, both at %v", got.ID, got.LeaseExpiresAt, got.DeadlineAt, short.ID, at(4*time.Minute+30*time.Second))
	}

	// The request is checked.
	for name, req := range map[string]execution.LeaseRequest{
		"no time":          {LeaseID: e.leaseID(100), JobTimeout: jobTimeout, GitHubWait: githubWait},
		"a short id":       {Now: at(0), LeaseID: "abc", JobTimeout: jobTimeout, GitHubWait: githubWait},
		"an upper-case id": {Now: at(0), LeaseID: strings.Repeat("A", 32), JobTimeout: jobTimeout, GitHubWait: githubWait},
		"no timeout":       {Now: at(0), LeaseID: e.leaseID(101), GitHubWait: githubWait},
		"no GitHub wait":   {Now: at(0), LeaseID: e.leaseID(102), JobTimeout: jobTimeout},
	} {
		_, _, err := e.s.Lease(e.ctx, req, trees)
		if !errors.Is(err, execution.ErrInvalidRequest) {
			e.t.Fatalf("%s: Lease error = %v; want ErrInvalidRequest", name, err)
		}
	}
}

func jbLeaseOrder(e *env) {
	a := e.enqueue(canonicalSpec(1), 2*time.Minute)
	b := e.enqueue(canonicalSpec(2), time.Minute)
	c := e.enqueue(canonicalSpec(3), time.Minute) // created with b: the lower id goes first
	if _, found := e.lease(30 * time.Second); found {
		e.t.Fatal("Lease before every not_before granted a job")
	}
	e.mustLease(b.ID, 2*time.Minute)
	e.mustLease(c.ID, 2*time.Minute)
	e.mustLease(a.ID, 2*time.Minute)
	if _, found := e.lease(2 * time.Minute); found {
		e.t.Fatal("Lease with every job leased granted a job")
	}
	for _, j := range []execution.Job{a, b, c} {
		if _, err := e.s.Cancel(e.ctx, j.ID, execution.ReasonObsolete, at(2*time.Minute)); err != nil {
			e.t.Fatal(err)
		}
	}
	// An older job in its backoff does not hold back a younger one, and goes first again when the backoff is over.
	d := e.enqueue(canonicalSpec(4), 3*time.Minute)
	f := e.enqueue(canonicalSpec(5), 4*time.Minute)
	l := e.mustLease(d.ID, 5*time.Minute)
	_, err := e.s.Complete(e.ctx, d.ID, unavailableResult(l.LeaseID, execution.ReasonExecutorError, "d"), at(5*time.Minute), githubWait)
	e.must(err)
	e.mustLease(f.ID, 5*time.Minute+time.Second)
	e.mustLease(d.ID, 5*time.Minute+firstBackoff)
}

func jbLeaseConcurrent(e *env) {
	const jobs, callers = 4, 8
	for i := range jobs {
		e.enqueue(canonicalSpec(i+1), 0)
	}
	ids := make([]string, callers)
	for i := range ids {
		ids[i] = e.newLeaseID()
	}
	got := make([]execution.Job, callers)
	found := make([]bool, callers)
	failures := make([]error, callers)
	parallel(callers, func(i int) {
		got[i], found[i], failures[i] = e.s.Lease(e.ctx, execution.LeaseRequest{Now: at(time.Minute), LeaseID: ids[i], JobTimeout: jobTimeout, GitHubWait: githubWait}, trees)
	})
	seen := map[int64]bool{}
	for i := range callers {
		e.must(failures[i])
		if !found[i] {
			continue
		}
		if seen[got[i].ID] {
			e.t.Fatalf("job %d was leased twice", got[i].ID)
		}
		seen[got[i].ID] = true
	}
	if len(seen) != jobs {
		e.t.Fatalf("%d jobs were leased; want %d", len(seen), jobs)
	}
}

func jbLeaseExpiry(e *env) {
	job := e.enqueue(canonicalSpec(1), 0)
	leased := e.mustLease(job.ID, 0)
	// Until the lease expires the job is leased and nothing is ready.
	if _, found := e.lease(leaseTTL - time.Microsecond); found {
		e.t.Fatal("Lease granted a job whose lease had not expired")
	}
	e.requireJob(leased)
	// At the expiry the attempt counts as a failure and the job waits 30 s.
	if _, found := e.lease(leaseTTL); found {
		e.t.Fatal("Lease granted the job it had just queued again")
	}
	e.requireJob(failed(leased, execution.ReasonLeaseExpired, leaseTTL, firstBackoff))
	if _, found := e.lease(leaseTTL + firstBackoff - time.Microsecond); found {
		e.t.Fatal("Lease granted a job before its not_before")
	}
	second := e.mustLease(job.ID, leaseTTL+firstBackoff)
	if second.Attempts != 2 || second.LeaseID == leased.LeaseID || second.Failures != 1 || second.Reason != execution.ReasonLeaseExpired {
		e.t.Fatalf("second lease = %+v; want attempt 2 with a new lease id, one failure and the last reason kept", second)
	}
	// The second failure waits 2 minutes, and the third is final.
	t2 := leaseTTL + firstBackoff + leaseTTL
	e.lease(t2)
	e.requireJob(failed(second, execution.ReasonLeaseExpired, t2, secondBackoff))
	third := e.mustLease(job.ID, t2+secondBackoff)
	t3 := t2 + secondBackoff + leaseTTL
	e.lease(t3)
	want := ended(third, execution.StateUnavailable, execution.ReasonLeaseExpired, t3)
	want.Failures = 3
	e.requireJob(want)
	e.requireNoResult(job.ID) // a lost executor reported nothing
	if _, found := e.lease(24 * time.Hour); found {
		e.t.Fatal("Lease granted a final job")
	}

	// A lease that ran past its deadline is a timeout.
	slow := e.enqueue(canonicalSpec(2), time.Hour)
	l, found := e.leaseWith(time.Hour, 90*time.Second)
	if !found || l.ID != slow.ID {
		e.t.Fatalf("Lease = job %d, found %v; want job %d", l.ID, found, slow.ID)
	}
	_, err := e.s.Heartbeat(e.ctx, slow.ID, l.LeaseID, at(time.Hour+30*time.Second))
	e.must(err)
	e.lease(time.Hour + 90*time.Second)
	after := e.get(slow.ID)
	if after.Reason != execution.ReasonTimeout || after.Failures != 1 || after.State != execution.StatePending {
		e.t.Fatalf("job after its deadline = %+v; want pending with the reason timeout and one failure", after)
	}
}

func jbLeaseNextCandidate(e *env) {
	a := e.enqueue(canonicalSpec(1), 0)
	b := e.enqueue(canonicalSpec(2), time.Minute)
	leased := e.mustLease(a.ID, 0)
	e.mustLease(b.ID, leaseTTL+time.Second) // a expired: it counted a failure and the call went on to b
	e.requireJob(failed(leased, execution.ReasonLeaseExpired, leaseTTL+time.Second, firstBackoff))
}

func jbRetriedReasons(e *env) {
	t := time.Duration(0)
	for i, reason := range []execution.Reason{execution.ReasonExecutorError, execution.ReasonSandboxUnavailable, execution.ReasonArchiveUnavailable, execution.ReasonFetchFailed} {
		job := e.enqueue(canonicalSpec(i+1), t)
		leased := e.mustLease(job.ID, t)
		out, err := e.s.Complete(e.ctx, job.ID, unavailableResult(leased.LeaseID, reason, "first "+string(reason)), at(t+10*time.Second), githubWait)
		e.must(err)
		if out.State != execution.StatePending || out.Replayed {
			e.t.Fatalf("%s: outcome = %+v; want pending", reason, out)
		}
		e.requireJob(failed(leased, reason, t+10*time.Second, firstBackoff))
		e.requireNoResult(job.ID)
		_, err = e.s.Cancel(e.ctx, job.ID, execution.ReasonObsolete, at(t+time.Minute)) // out of the way of the next one
		e.must(err)
		t += time.Hour
	}

	// The third failed attempt is final, and its result is stored.
	job := e.enqueue(canonicalSpec(9), t)
	var last execution.Result
	for attempt, backoff := range []time.Duration{firstBackoff, secondBackoff, 0} {
		leased := e.mustLease(job.ID, t)
		last = unavailableResult(leased.LeaseID, execution.ReasonExecutorError, "attempt "+string(rune('a'+attempt)))
		out, err := e.s.Complete(e.ctx, job.ID, last, at(t+time.Second), githubWait)
		e.must(err)
		switch {
		case backoff > 0 && out.State == execution.StatePending:
			e.requireJob(failed(leased, execution.ReasonExecutorError, t+time.Second, backoff))
			t += time.Second + backoff
		case backoff == 0 && out.State == execution.StateUnavailable:
			want := ended(leased, execution.StateUnavailable, execution.ReasonExecutorError, t+time.Second)
			want.Failures = 3
			e.requireJob(want)
		default:
			e.t.Fatalf("attempt %d: outcome %+v", attempt+1, out)
		}
	}
	stored, found, err := e.s.Result(e.ctx, job.ID)
	e.must(err)
	if !found || stored.Origin != execution.OriginExecutor || stored.Status != execution.StateUnavailable || stored.Reason != execution.ReasonExecutorError || stored.BodyDigest != last.BodyDigest {
		e.t.Fatalf("result of the third failure = %+v, found %v; want the executor row of the last attempt", stored, found)
	}

	// A reason that is not retried is final at once, and is not a failure.
	for i, reason := range []execution.Reason{execution.ReasonInstallFailed, execution.ReasonResourceLimit, execution.ReasonToolchainMismatch,
		execution.ReasonSourceMismatch, execution.ReasonTooLarge, execution.ReasonProfileUnsupported} {
		t += time.Hour
		job := e.enqueue(canonicalSpec(20+i), t)
		leased := e.mustLease(job.ID, t)
		out, err := e.s.Complete(e.ctx, job.ID, unavailableResult(leased.LeaseID, reason, "final "+string(reason)), at(t+time.Second), githubWait)
		e.must(err)
		if out.State != execution.StateUnavailable {
			e.t.Fatalf("%s: outcome = %+v; want unavailable", reason, out)
		}
		e.requireJob(ended(leased, execution.StateUnavailable, reason, t+time.Second))
		if stored, found, err := e.s.Result(e.ctx, job.ID); err != nil || !found || stored.Reason != reason || stored.Origin != execution.OriginExecutor {
			e.t.Fatalf("%s: result = %+v, found %v, error %v; want its executor row", reason, stored, found, err)
		}
	}
}

func jbGitHubFailure(e *env) {
	job := e.enqueue(canonicalSpec(1), 0)
	state, err := e.s.GitHubFailure(e.ctx, job.ID, at(time.Minute), githubWait)
	e.must(err)
	if state != execution.StatePending {
		e.t.Fatalf("state after a GitHub failure = %s; want pending", state)
	}
	want := job
	want.Reason, want.GitHubSince, want.NotBefore = execution.ReasonGitHubUnavailable, at(time.Minute), at(time.Minute+githubBackoff)
	e.requireJob(want) // no failure and no attempt
	if _, found := e.lease(time.Minute + githubBackoff - time.Microsecond); found {
		e.t.Fatal("Lease granted a job inside the GitHub backoff")
	}
	// The first failure is kept, and the job waits again.
	_, err = e.s.GitHubFailure(e.ctx, job.ID, at(3*time.Minute), githubWait)
	e.must(err)
	want.NotBefore = at(3*time.Minute + githubBackoff)
	e.requireJob(want)
	// The job is final only once the first failure is a whole wait old.
	state, err = e.s.GitHubFailure(e.ctx, job.ID, at(time.Minute+githubWait-time.Microsecond), githubWait)
	e.must(err)
	if state != execution.StatePending {
		e.t.Fatalf("state a microsecond before the wait = %s; want pending", state)
	}
	state, err = e.s.GitHubFailure(e.ctx, job.ID, at(time.Minute+githubWait), githubWait)
	e.must(err)
	if state != execution.StateUnavailable {
		e.t.Fatalf("state at the wait = %s; want unavailable", state)
	}
	want = ended(want, execution.StateUnavailable, execution.ReasonGitHubUnavailable, time.Minute+githubWait)
	want.NotBefore = at(time.Minute + githubWait + githubBackoff)
	e.requireJob(want)
	// Its results row is the service's: no executor, image or runtime took part.
	stored, found, err := e.s.Result(e.ctx, job.ID)
	e.must(err)
	wantResult := execution.StoredResult{JobID: job.ID, Origin: execution.OriginService, ReceivedAt: at(time.Minute + githubWait),
		Report: execution.Report{Status: execution.StateUnavailable, Reason: execution.ReasonGitHubUnavailable, StartedAt: at(time.Minute), EndedAt: at(time.Minute + githubWait)}}
	if !found {
		e.t.Fatal("the job that ended on the GitHub bound has no results row")
	}
	if d := diff(stored, wantResult, "result"); d != "" {
		e.t.Fatal(d)
	}
	// A job that is final, leased or unknown records no failure.
	_, err = e.s.GitHubFailure(e.ctx, job.ID, at(time.Hour), githubWait)
	e.requireErr(err, execution.ErrInvalidRequest)
	leased := e.enqueue(canonicalSpec(2), 2*time.Hour)
	e.mustLease(leased.ID, 2*time.Hour)
	_, err = e.s.GitHubFailure(e.ctx, leased.ID, at(2*time.Hour), githubWait)
	e.requireErr(err, execution.ErrInvalidRequest)
	_, err = e.s.GitHubFailure(e.ctx, 987654, at(2*time.Hour), githubWait)
	e.requireErr(err, execution.ErrNotFound)
}

func jbGitHubLeased(e *env) {
	const wait = 2 * time.Minute
	params := func(now time.Duration) execution.ExpireParams {
		p := expireParams(now, now) // an executor was just seen
		p.GitHubWait = wait
		return p
	}
	leased := e.enqueue(canonicalSpec(1), 0)
	pending := e.enqueue(canonicalSpec(2), 0) // created with the first, which is older by id: it is the one leased
	for _, j := range []execution.Job{leased, pending} {
		_, err := e.s.GitHubFailure(e.ctx, j.ID, at(time.Minute), wait)
		e.must(err)
	}
	// A successful lease does not clear the first failure, and the reason stays.
	l := e.mustLease(leased.ID, 2*time.Minute)
	if !l.GitHubSince.Equal(at(time.Minute)) || l.Reason != execution.ReasonGitHubUnavailable {
		e.t.Fatalf("leased job = since %v, reason %q; want %v and github-unavailable", l.GitHubSince, l.Reason, at(time.Minute))
	}
	_, err := e.s.Heartbeat(e.ctx, leased.ID, l.LeaseID, at(2*time.Minute+30*time.Second))
	e.must(err)
	// At the wait the pending job ends, and the leased one, whose lease is valid, is left alone.
	if n := e.expire(params(3 * time.Minute)); n != 1 {
		e.t.Fatalf("Expire changed %d jobs; want only the pending one", n)
	}
	got := e.get(pending.ID)
	if got.State != execution.StateUnavailable || got.Reason != execution.ReasonGitHubUnavailable || !got.FinishedAt.Equal(at(3*time.Minute)) {
		e.t.Fatalf("pending job after the bound = %+v; want unavailable for github-unavailable at 3 minutes", got)
	}
	stored, found, err := e.s.Result(e.ctx, pending.ID)
	e.must(err)
	if !found || stored.Origin != execution.OriginService || !stored.StartedAt.Equal(at(time.Minute)) || !stored.EndedAt.Equal(at(3*time.Minute)) {
		e.t.Fatalf("results row of the bound = %+v, found %v; want the service's, from the first failure to the bound", stored, found)
	}
	if after := e.get(leased.ID); after.State != execution.StateLeased || !after.GitHubSince.Equal(at(time.Minute)) {
		e.t.Fatalf("leased job after Expire = %+v; want it still leased with its first failure", after)
	}
	// When its lease expires that is a failure that is not GitHub's, which clears the first failure.
	if n := e.expire(params(3*time.Minute + 30*time.Second)); n != 1 {
		e.t.Fatalf("Expire changed %d jobs; want the leased one", n)
	}
	after := e.get(leased.ID)
	if after.State != execution.StatePending || after.Reason != execution.ReasonLeaseExpired || !after.GitHubSince.IsZero() || after.Failures != 1 {
		e.t.Fatalf("job after its lease expired = %+v; want pending, lease-expired, one failure and no first GitHub failure", after)
	}
	// A pending job whose reason is another one is not ended by the bound.
	if n := e.expire(params(time.Hour)); n != 0 {
		e.t.Fatalf("Expire changed %d jobs; want none", n)
	}
}

func jbGitHubGiveBack(e *env) {
	const wait = 100 * time.Second
	job := e.enqueue(canonicalSpec(1), 0)
	first := e.mustLease(job.ID, 0)
	out, err := e.s.Complete(e.ctx, job.ID, unavailableResult(first.LeaseID, execution.ReasonGitHubUnavailable, "first"), at(10*time.Second), wait)
	e.must(err)
	if out.State != execution.StatePending {
		e.t.Fatalf("outcome of a GitHub give-back = %+v; want pending", out)
	}
	queued := first
	queued.State, queued.Reason, queued.GitHubSince, queued.NotBefore = execution.StatePending, execution.ReasonGitHubUnavailable, at(10*time.Second), at(10*time.Second+githubBackoff)
	queued.LeaseExpiresAt, queued.DeadlineAt = time.Time{}, time.Time{}
	e.requireJob(queued) // no failure was counted
	e.requireNoResult(job.ID)

	second := e.mustLease(job.ID, 70*time.Second)
	if !second.GitHubSince.Equal(at(10 * time.Second)) {
		e.t.Fatalf("a lease cleared the first failure: %v", second.GitHubSince)
	}
	// Once the first failure is a wait old, the give-back is the job's final state, as an executor row.
	r := unavailableResult(second.LeaseID, execution.ReasonGitHubUnavailable, "second")
	out, err = e.s.Complete(e.ctx, job.ID, r, at(115*time.Second), wait)
	e.must(err)
	if out.State != execution.StateUnavailable || out.Replayed {
		e.t.Fatalf("outcome of the give-back past the wait = %+v; want unavailable", out)
	}
	e.requireJob(ended(second, execution.StateUnavailable, execution.ReasonGitHubUnavailable, 115*time.Second))
	e.requireResult(job.ID, execution.OriginExecutor, 115*time.Second, r.Report)
	// Sent again, the same body gets the stored outcome; another body conflicts.
	out, err = e.s.Complete(e.ctx, job.ID, r, at(time.Hour), wait)
	e.must(err)
	if out.State != execution.StateUnavailable || !out.Replayed {
		e.t.Fatalf("outcome of the resend = %+v; want unavailable, replayed", out)
	}
	_, err = e.s.Complete(e.ctx, job.ID, unavailableResult(second.LeaseID, execution.ReasonGitHubUnavailable, "other"), at(time.Hour), wait)
	e.requireErr(err, execution.ErrConflict)
}

func jbNoExecutor(e *env) {
	old := e.enqueue(canonicalSpec(1), 0)
	waiting := e.enqueue(canonicalSpec(2), 0)
	late := e.enqueue(canonicalSpec(3), 9*time.Minute)
	backoff := e.enqueue(canonicalSpec(4), 9*time.Minute)
	// An executor was seen a minute ago: no job ends however old it is.
	if n := e.expire(expireParams(10*time.Minute, 9*time.Minute)); n != 0 {
		e.t.Fatalf("Expire changed %d jobs with an executor seen a minute ago; want none", n)
	}
	e.requireJob(old)
	_, err := e.s.GitHubFailure(e.ctx, backoff.ID, at(13*time.Minute), githubWait)
	e.must(err)
	leased := e.mustLease(old.ID, 13*time.Minute+30*time.Second)
	// Seen a whole wait ago: every pending job ends, whatever its reason or backoff; a leased job with a valid lease does not.
	if n := e.expire(expireParams(9*time.Minute+leaseWait, 9*time.Minute)); n != 3 {
		e.t.Fatalf("Expire changed %d jobs; want the three pending ones", n)
	}
	for _, j := range []execution.Job{waiting, late} {
		e.requireJob(ended(j, execution.StateUnavailable, execution.ReasonNoExecutor, 9*time.Minute+leaseWait))
		e.requireNoResult(j.ID)
	}
	got := e.get(backoff.ID)
	if got.State != execution.StateUnavailable || got.Reason != execution.ReasonNoExecutor {
		e.t.Fatalf("job in its backoff = %+v; want unavailable for no-executor", got)
	}
	e.requireJob(leased)
	if n := e.expire(expireParams(9*time.Minute+leaseWait, 9*time.Minute)); n != 0 {
		e.t.Fatalf("a second Expire changed %d jobs; want none", n)
	}
}

func jbHeartbeat(e *env) {
	job := e.enqueue(canonicalSpec(1), 0)
	l, found := e.leaseWith(0, 100*time.Second) // the deadline is at 100 s
	if !found || l.ID != job.ID {
		e.t.Fatalf("Lease = job %d, found %v; want job %d", l.ID, found, job.ID)
	}
	expires, err := e.s.Heartbeat(e.ctx, job.ID, l.LeaseID, at(30*time.Second))
	e.must(err)
	if !expires.Equal(at(90 * time.Second)) {
		e.t.Fatalf("Heartbeat at 30 s = %v; want %v", expires, at(90*time.Second))
	}
	want := l
	want.LeaseExpiresAt = at(90 * time.Second)
	e.requireJob(want)
	// Never beyond the deadline.
	expires, err = e.s.Heartbeat(e.ctx, job.ID, l.LeaseID, at(80*time.Second))
	e.must(err)
	if !expires.Equal(at(100 * time.Second)) {
		e.t.Fatalf("Heartbeat at 80 s = %v; want the deadline %v", expires, at(100*time.Second))
	}
	// The lease is lost to another lease id, and at its expiry.
	_, err = e.s.Heartbeat(e.ctx, job.ID, e.newLeaseID(), at(81*time.Second))
	e.requireErr(err, execution.ErrLeaseLost)
	_, err = e.s.Heartbeat(e.ctx, job.ID, l.LeaseID, at(100*time.Second))
	e.requireErr(err, execution.ErrLeaseLost)
	want.LeaseExpiresAt = at(100 * time.Second)
	e.requireJob(want)
	_, err = e.s.Cancel(e.ctx, job.ID, execution.ReasonObsolete, at(101*time.Second))
	e.must(err)

	// It is lost on a job that was cancelled, queued again after a failed attempt, finished, never leased, and unknown.
	cancelled := e.enqueue(canonicalSpec(2), time.Hour)
	cl := e.mustLease(cancelled.ID, time.Hour)
	_, err = e.s.Cancel(e.ctx, cancelled.ID, execution.ReasonClosed, at(time.Hour))
	e.must(err)
	_, err = e.s.Heartbeat(e.ctx, cancelled.ID, cl.LeaseID, at(time.Hour+time.Second))
	e.requireErr(err, execution.ErrLeaseLost)
	next := e.enqueue(canonicalSpec(3), 2*time.Hour)
	nl := e.mustLease(next.ID, 2*time.Hour)
	_, err = e.s.Complete(e.ctx, next.ID, unavailableResult(nl.LeaseID, execution.ReasonExecutorError, "x"), at(2*time.Hour), githubWait)
	e.must(err)
	_, err = e.s.Heartbeat(e.ctx, next.ID, nl.LeaseID, at(2*time.Hour+time.Second))
	e.requireErr(err, execution.ErrLeaseLost)
	_, err = e.s.Cancel(e.ctx, next.ID, execution.ReasonObsolete, at(2*time.Hour+time.Second))
	e.must(err)
	done := e.enqueue(canonicalSpec(4), 3*time.Hour)
	dl := e.mustLease(done.ID, 3*time.Hour)
	_, err = e.s.Complete(e.ctx, done.ID, completeCanonical(dl.LeaseID, "done"), at(3*time.Hour), githubWait)
	e.must(err)
	_, err = e.s.Heartbeat(e.ctx, done.ID, dl.LeaseID, at(3*time.Hour+time.Second))
	e.requireErr(err, execution.ErrLeaseLost)
	never := e.enqueue(canonicalSpec(5), 4*time.Hour)
	_, err = e.s.Heartbeat(e.ctx, never.ID, dl.LeaseID, at(4*time.Hour))
	e.requireErr(err, execution.ErrLeaseLost)
	_, err = e.s.Heartbeat(e.ctx, 987654, dl.LeaseID, at(4*time.Hour))
	e.requireErr(err, execution.ErrNotFound)
}

func jbCancelAndCounts(e *env) {
	a := e.enqueue(canonicalSpec(1), 0)
	b := e.enqueue(canonicalSpec(2), 0)
	c := e.enqueue(canonicalSpec(3), 0)
	leased := e.mustLease(a.ID, 0)
	e.requireCounts(2, 1, 0, 0, 0)
	changed, err := e.s.Cancel(e.ctx, b.ID, execution.ReasonHeadMoved, at(time.Minute))
	e.must(err)
	if !changed {
		e.t.Fatal("Cancel of a pending job reported no change")
	}
	changed, err = e.s.Cancel(e.ctx, a.ID, execution.ReasonClosed, at(time.Minute))
	e.must(err)
	if !changed {
		e.t.Fatal("Cancel of a leased job reported no change")
	}
	e.requireJob(ended(leased, execution.StateCancelled, execution.ReasonClosed, time.Minute))
	// A final job is left as it was, whatever the reason.
	changed, err = e.s.Cancel(e.ctx, b.ID, execution.ReasonObsolete, at(2*time.Minute))
	e.must(err)
	if changed {
		e.t.Fatal("Cancel of a final job reported a change")
	}
	e.requireJob(ended(b, execution.StateCancelled, execution.ReasonHeadMoved, time.Minute))
	_, err = e.s.Cancel(e.ctx, c.ID, execution.ReasonNoExecutor, at(2*time.Minute))
	e.requireErr(err, execution.ErrInvalidRequest)
	_, err = e.s.Cancel(e.ctx, 987654, execution.ReasonClosed, at(2*time.Minute))
	e.requireErr(err, execution.ErrNotFound)
	e.requireCounts(1, 0, 0, 0, 2)
	if n := e.expire(expireParams(time.Hour, 0)); n != 1 {
		e.t.Fatalf("Expire changed %d jobs; want the pending one", n)
	}
	e.requireCounts(0, 0, 0, 1, 2)
}

func jbReads(e *env) {
	e.requireCounts(0, 0, 0, 0, 0)
	a := e.enqueue(canonicalSpec(1), 0)
	r := e.enqueue(receiptSpec(1), 0) // the same head, the other plan
	b := e.enqueue(canonicalSpec(2), 0)
	if _, err := e.s.Cancel(e.ctx, a.ID, execution.ReasonHeadMoved, at(time.Minute)); err != nil {
		e.t.Fatal(err)
	}
	again := e.enqueue(canonicalSpec(1), 2*time.Minute)
	heads := func(repository int64, pull int, head string, plan execution.Plan, want ...int64) {
		e.t.Helper()
		got, err := e.s.HeadJobs(e.ctx, repository, pull, head, plan)
		e.must(err)
		ids := make([]int64, len(got))
		for i, j := range got {
			ids[i] = j.ID
		}
		if len(ids) != len(want) {
			e.t.Fatalf("HeadJobs(%d, %d, %s, %s) = jobs %v; want %v", repository, pull, head[:6], plan, ids, want)
		}
		for i := range ids {
			if ids[i] != want[i] {
				e.t.Fatalf("HeadJobs(%d, %d, %s, %s) = jobs %v; want %v", repository, pull, head[:6], plan, ids, want)
			}
		}
	}
	heads(10, 1, sha(1), execution.PlanCanonical, a.ID, again.ID) // every job of the head, oldest first
	heads(10, 1, sha(1), execution.PlanReceipt, r.ID)
	heads(10, 1, sha(2), execution.PlanCanonical, b.ID)
	heads(10, 1, sha(2), execution.PlanReceipt)
	heads(10, 2, sha(1), execution.PlanCanonical)
	heads(20, 1, sha(1), execution.PlanCanonical)
	e.requireCurrent(canonicalSpec(1), again.ID)
	if got, found, err := e.s.CurrentJob(e.ctx, keyOf(canonicalSpec(9))); err != nil || found {
		e.t.Fatalf("CurrentJob of an unknown key = %+v, found %v, error %v; want none", got, found, err)
	}
	if got, found, err := e.s.Job(e.ctx, 987654); err != nil || found {
		e.t.Fatalf("Job of an unknown id = %+v, found %v, error %v; want none", got, found, err)
	}
	e.requireCounts(3, 0, 0, 0, 1)
}

func jbOpenJobs(e *env) {
	other := func(s execution.Spec) execution.Spec { s.RepositoryID = 20; return s }
	leased := e.enqueue(canonicalSpec(1), 0)
	pending := e.enqueue(receiptSpec(1), time.Second)
	cancelled := e.enqueue(canonicalSpec(2), time.Second)
	e.enqueueFinal(withBase(receiptSpec(3), ""), execution.ReasonNotConfigured, time.Second)
	elsewhere := e.enqueue(other(canonicalSpec(5)), time.Second) // the key holds no repository, so another head
	e.mustLease(leased.ID, 2*time.Second)
	if _, err := e.s.Cancel(e.ctx, cancelled.ID, execution.ReasonHeadMoved, at(3*time.Second)); err != nil {
		e.t.Fatal(err)
	}
	open := func(repository int64, want ...int64) {
		e.t.Helper()
		got, err := e.s.OpenJobs(e.ctx, repository)
		e.must(err)
		if len(got) != len(want) {
			e.t.Fatalf("OpenJobs(%d) = %d jobs; want jobs %v", repository, len(got), want)
		}
		for i, job := range got {
			if job.ID != want[i] {
				e.t.Fatalf("OpenJobs(%d)[%d] = job %d; want job %d", repository, i, job.ID, want[i])
			}
			if diff := diffValues(reflect.ValueOf(job), reflect.ValueOf(e.get(want[i])), "job"); diff != "" {
				e.t.Fatalf("OpenJobs(%d) job %d differs from the stored job: %s", repository, job.ID, diff)
			}
		}
	}
	open(10, leased.ID, pending.ID) // a leased job is open; a cancelled one and one created unavailable are not
	open(20, elsewhere.ID)
	open(30)
	if _, err := e.s.OpenJobs(e.ctx, 0); !errors.Is(err, execution.ErrInvalidRequest) {
		e.t.Fatalf("OpenJobs of repository 0: error = %v; want ErrInvalidRequest", err)
	}
}

func jbCanceled(e *env) {
	job := e.enqueue(canonicalSpec(1), 0)
	ctx, cancel := context.WithCancel(e.ctx)
	cancel()
	calls := map[string]func() error{
		"Enqueue": func() error {
			_, _, err := e.s.Enqueue(ctx, execution.NewJob{Spec: canonicalSpec(2), At: at(0)})
			return err
		},
		"Lease": func() error {
			_, _, err := e.s.Lease(ctx, execution.LeaseRequest{Now: at(0), LeaseID: e.leaseID(1), JobTimeout: jobTimeout, GitHubWait: githubWait}, trees)
			return err
		},
		"Heartbeat": func() error { _, err := e.s.Heartbeat(ctx, job.ID, e.leaseID(1), at(0)); return err },
		"Complete": func() error {
			_, err := e.s.Complete(ctx, job.ID, completeCanonical(e.leaseID(1), "x"), at(0), githubWait)
			return err
		},
		"GitHubFailure": func() error { _, err := e.s.GitHubFailure(ctx, job.ID, at(0), githubWait); return err },
		"Cancel":        func() error { _, err := e.s.Cancel(ctx, job.ID, execution.ReasonClosed, at(0)); return err },
		"Expire":        func() error { _, err := e.s.Expire(ctx, expireParams(time.Hour, 0)); return err },
		"Job":           func() error { _, _, err := e.s.Job(ctx, job.ID); return err },
		"CurrentJob":    func() error { _, _, err := e.s.CurrentJob(ctx, job.Key); return err },
		"HeadJobs":      func() error { _, err := e.s.HeadJobs(ctx, 10, 1, sha(1), execution.PlanCanonical); return err },
		"Result":        func() error { _, _, err := e.s.Result(ctx, job.ID); return err },
		"Tests":         func() error { _, err := e.s.Tests(ctx, job.ID); return err },
		"Counts":        func() error { _, err := e.s.Counts(ctx); return err },
		"Unblocked":     func() error { _, err := e.s.Unblocked(ctx, job.ID, at(0)); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, context.Canceled) {
			e.t.Fatalf("%s with a canceled context: error = %v; want context.Canceled", name, err)
		}
	}
	e.requireJob(job) // nothing changed
	e.requireCounts(1, 0, 0, 0, 0)
}
