package executiontest

import (
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

// jbWaits: every operation that takes a wait refuses one that is not positive, as Lease does, and writes nothing.
func jbWaits(e *env) {
	leased := e.enqueue(canonicalSpec(1), 0)
	l := e.mustLease(leased.ID, 0)
	pending := e.enqueue(canonicalSpec(2), time.Hour)
	for name, p := range map[string]execution.ExpireParams{
		"no lease wait":          {Now: at(2 * time.Hour), LastSeenAt: at(0), GitHubWait: githubWait},
		"no GitHub wait":         {Now: at(2 * time.Hour), LastSeenAt: at(0), LeaseWait: leaseWait},
		"a negative lease wait":  {Now: at(2 * time.Hour), LastSeenAt: at(0), LeaseWait: -time.Second, GitHubWait: githubWait},
		"a negative GitHub wait": {Now: at(2 * time.Hour), LastSeenAt: at(0), LeaseWait: leaseWait, GitHubWait: -time.Second},
	} {
		if _, err := e.s.Expire(e.ctx, p); err == nil {
			e.t.Fatalf("%s: Expire succeeded; want ErrInvalidRequest", name)
		} else {
			e.requireErr(err, execution.ErrInvalidRequest)
		}
	}
	for _, wait := range []time.Duration{0, -time.Second} {
		_, err := e.s.Complete(e.ctx, leased.ID, completeCanonical(l.LeaseID, "waits"), at(10*time.Second), wait)
		e.requireErr(err, execution.ErrInvalidRequest)
		_, err = e.s.GitHubFailure(e.ctx, pending.ID, at(2*time.Hour), wait)
		e.requireErr(err, execution.ErrInvalidRequest)
	}
	e.requireUntouched(l)
	e.requireJob(pending)
	// With a positive wait the same calls go through.
	if _, err := e.s.Complete(e.ctx, leased.ID, completeCanonical(l.LeaseID, "waits"), at(10*time.Second), githubWait); err != nil {
		e.t.Fatal(err)
	}
	if state, err := e.s.GitHubFailure(e.ctx, pending.ID, at(2*time.Hour), githubWait); err != nil || state != execution.StatePending {
		e.t.Fatalf("GitHubFailure = %s, %v; want pending", state, err)
	}
}

// jbPastBothBounds: a pending job that is past the GitHub bound and the no-executor wait ends on the GitHub bound, whose cause is
// GitHub's, with the results row of the service; past the no-executor wait alone it ends as no-executor.
func jbPastBothBounds(e *env) {
	both := e.enqueue(canonicalSpec(1), 0)
	if _, err := e.s.GitHubFailure(e.ctx, both.ID, at(time.Minute), githubWait); err != nil {
		e.t.Fatal(err)
	}
	// Nothing was seen since the start, and the first GitHub failure is a whole wait old.
	now := time.Minute + githubWait
	if n := e.expire(expireParams(now, 0)); n != 1 {
		e.t.Fatalf("Expire changed %d jobs; want one", n)
	}
	got := e.get(both.ID)
	if got.State != execution.StateUnavailable || got.Reason != execution.ReasonGitHubUnavailable || !got.FinishedAt.Equal(at(now)) {
		e.t.Fatalf("job past both bounds = %+v; want unavailable for github-unavailable at %v", got, at(now))
	}
	stored, found, err := e.s.Result(e.ctx, both.ID)
	e.must(err)
	if !found || stored.Origin != execution.OriginService || !stored.StartedAt.Equal(at(time.Minute)) || !stored.EndedAt.Equal(at(now)) {
		e.t.Fatalf("results row = %+v, found %v; want the service's, from the first failure to the bound", stored, found)
	}

	// A GitHub failure that is not old does not take the cause from the missing executor.
	late := e.enqueue(canonicalSpec(2), now)
	if _, err := e.s.GitHubFailure(e.ctx, late.ID, at(now), githubWait); err != nil {
		e.t.Fatal(err)
	}
	if n := e.expire(expireParams(now+leaseWait, now)); n != 1 {
		e.t.Fatalf("Expire changed %d jobs; want one", n)
	}
	got = e.get(late.ID)
	if got.State != execution.StateUnavailable || got.Reason != execution.ReasonNoExecutor {
		e.t.Fatalf("job past the no-executor wait only = %+v; want unavailable for no-executor", got)
	}
	e.requireNoResult(late.ID)
}

// rsNanoseconds: a time keeps the microseconds that PostgreSQL stores, in the times of the run as in every other.
func rsNanoseconds(e *env) {
	job := e.enqueue(canonicalSpec(1), 0)
	l := e.mustLease(job.ID, 0)
	r := completeCanonical(l.LeaseID, "nanoseconds")
	r.StartedAt = at(time.Second).Add(123 * time.Nanosecond)
	r.EndedAt = at(8 * time.Second).Add(456 * time.Nanosecond)
	out, err := e.s.Complete(e.ctx, job.ID, r, at(10*time.Second).Add(789*time.Nanosecond), githubWait)
	e.must(err)
	if out.State != execution.StateComplete {
		e.t.Fatalf("outcome = %+v; want complete", out)
	}
	want := r.Report
	want.StartedAt, want.EndedAt = at(time.Second), at(8*time.Second)
	e.requireResult(job.ID, execution.OriginExecutor, 10*time.Second, want)
	// The same body sent again is still the same result.
	again, err := e.s.Complete(e.ctx, job.ID, r, at(time.Hour), githubWait)
	e.must(err)
	if !again.Replayed {
		e.t.Fatalf("outcome of the resend = %+v; want replayed", again)
	}
}
