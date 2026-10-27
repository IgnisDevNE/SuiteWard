package executiontest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

// leasePrepared asks for a job at the time now, with a GitHub wait of its own, and reads the trees with prepare.
func (e *env) leasePrepared(now, wait time.Duration, prepare execution.Prepare) (execution.Job, bool, error) {
	e.t.Helper()
	req := e.leaseRequest(now)
	req.GitHubWait = wait
	return e.s.Lease(e.ctx, req, prepare)
}

// recorder is a Prepare that notes the jobs it was given and answers for each by its id.
type recorder struct {
	seen    []int64
	answers map[int64]error
}

func (r *recorder) prepare(_ context.Context, job execution.Job) error {
	r.seen = append(r.seen, job.ID)
	return r.answers[job.ID]
}

func (e *env) requireSeen(r *recorder, want ...int64) {
	e.t.Helper()
	if fmt.Sprint(r.seen) != fmt.Sprint(want) {
		e.t.Fatalf("prepare was given jobs %v; want %v", r.seen, want)
	}
}

func jbLeasePrepare(e *env) {
	// A ready job is given to prepare as it is, before it is leased, and nil grants the lease.
	job := e.enqueue(receiptSpec(1), 0)
	var given execution.Job
	got, found, err := e.leasePrepared(time.Minute, githubWait, func(_ context.Context, j execution.Job) error {
		given = j
		return nil
	})
	e.must(err)
	if !found || got.ID != job.ID {
		e.t.Fatalf("Lease = job %d, found %v; want job %d", got.ID, found, job.ID)
	}
	if d := diff(given, job, "the job given to prepare"); d != "" {
		e.t.Fatal(d)
	}
	if got.State != execution.StateLeased || got.Attempts != 1 {
		e.t.Fatalf("leased job = %+v; want leased on its first attempt", got)
	}
	// prepare is required.
	_, _, err = e.s.Lease(e.ctx, e.leaseRequest(2*time.Minute), nil)
	e.requireErr(err, execution.ErrInvalidRequest)
	e.requireJob(got)
}

func jbLeasePrepareGitHub(e *env) {
	a := e.enqueue(canonicalSpec(1), 0)
	b := e.enqueue(canonicalSpec(2), 0)
	unreadable := &recorder{answers: map[int64]error{a.ID: fmt.Errorf("the tree of the head: %w", execution.ErrGitHubUnavailable)}}
	// The failure is recorded as GitHubFailure records it: no attempt, no failure, a wait of a minute, and the call goes no further.
	_, found, err := e.leasePrepared(time.Minute, githubWait, unreadable.prepare)
	e.requireErr(err, execution.ErrGitHubUnavailable)
	if found {
		e.t.Fatal("Lease found a job although the read failed")
	}
	e.requireSeen(unreadable, a.ID)
	want := a
	want.Reason, want.GitHubSince, want.NotBefore = execution.ReasonGitHubUnavailable, at(time.Minute), at(time.Minute+githubBackoff)
	e.requireJob(want)
	e.requireJob(b) // the next job was not touched
	// The job waits a minute, and the next one goes first meanwhile.
	e.mustLease(b.ID, time.Minute+githubBackoff-time.Second)
	retry := e.mustLease(a.ID, time.Minute+githubBackoff)
	if retry.Attempts != 1 || retry.Failures != 0 || !retry.GitHubSince.Equal(at(time.Minute)) || retry.Reason != execution.ReasonGitHubUnavailable {
		e.t.Fatalf("the job leased after the failure = %+v; want its first attempt, no failure and the first GitHub failure kept", retry)
	}
}

func jbLeasePrepareGitHubBound(e *env) {
	const wait = 2 * time.Minute
	job := e.enqueue(canonicalSpec(1), 0)
	failing := func(context.Context, execution.Job) error { return execution.ErrGitHubUnavailable }
	_, _, err := e.leasePrepared(10*time.Minute, wait, failing)
	e.requireErr(err, execution.ErrGitHubUnavailable)
	if got := e.get(job.ID); got.State != execution.StatePending || !got.GitHubSince.Equal(at(10*time.Minute)) {
		e.t.Fatalf("job after the first failure = %+v; want pending with the first failure at 10 minutes", got)
	}
	// A failure a whole wait after the first ends the job, with the service's results row, and the call still fails.
	_, _, err = e.leasePrepared(12*time.Minute, wait, failing)
	e.requireErr(err, execution.ErrGitHubUnavailable)
	got := e.get(job.ID)
	if got.State != execution.StateUnavailable || got.Reason != execution.ReasonGitHubUnavailable || !got.FinishedAt.Equal(at(12*time.Minute)) || got.Attempts != 0 {
		e.t.Fatalf("job at the bound = %+v; want unavailable for github-unavailable at 12 minutes with no attempt", got)
	}
	stored, found, err := e.s.Result(e.ctx, job.ID)
	e.must(err)
	if !found || stored.Origin != execution.OriginService || !stored.StartedAt.Equal(at(10*time.Minute)) || !stored.EndedAt.Equal(at(12*time.Minute)) {
		e.t.Fatalf("results row = %+v, found %v; want the service's, from the first failure to the bound", stored, found)
	}
}

func jbLeasePrepareTooLarge(e *env) {
	a := e.enqueue(canonicalSpec(1), 0)
	b := e.enqueue(canonicalSpec(2), time.Minute)
	c := e.enqueue(canonicalSpec(3), 2*time.Minute)
	tooLarge := fmt.Errorf("50,001 tree entries: %w", execution.ErrTooLarge)
	r := &recorder{answers: map[int64]error{a.ID: tooLarge, c.ID: tooLarge}}
	// The job that is too large is final at once and consumes no attempt, and the call goes on to the next one.
	got, found, err := e.leasePrepared(3*time.Minute, githubWait, r.prepare)
	e.must(err)
	if !found || got.ID != b.ID {
		e.t.Fatalf("Lease = job %d, found %v; want job %d", got.ID, found, b.ID)
	}
	e.requireSeen(r, a.ID, b.ID)
	e.requireJob(ended(a, execution.StateUnavailable, execution.ReasonTooLarge, 3*time.Minute))
	e.requireNoResult(a.ID)
	// When every ready job is too large, nothing is leased and nothing is an error.
	if _, found, err := e.leasePrepared(3*time.Minute+30*time.Second, githubWait, r.prepare); err != nil || found {
		e.t.Fatalf("Lease = found %v, error %v; want none", found, err)
	}
	e.requireJob(ended(c, execution.StateUnavailable, execution.ReasonTooLarge, 3*time.Minute+30*time.Second))
	// A job that is too large keeps its key, which is current.
	same, created, err := e.s.Enqueue(e.ctx, execution.NewJob{Spec: canonicalSpec(1), At: at(5 * time.Minute)})
	e.must(err)
	if created || same.ID != a.ID {
		e.t.Fatalf("Enqueue over a job that is too large = job %d, created %v; want job %d", same.ID, created, a.ID)
	}
}

func jbLeasePrepareAbort(e *env) {
	x := e.enqueue(canonicalSpec(1), 0)
	leased := e.mustLease(x.ID, 0) // its lease expires at 60 s
	y := e.enqueue(canonicalSpec(2), 30*time.Second)
	boom := errors.New("the tree cannot be read")
	r := &recorder{answers: map[int64]error{y.ID: boom}}
	// An expired lease is not given to prepare. Any other failure of prepare ends the call with nothing written, the expiry that came
	// first in the same call included.
	_, found, err := e.leasePrepared(leaseTTL+time.Second, githubWait, r.prepare)
	e.requireErr(err, boom)
	if found {
		e.t.Fatal("Lease found a job although prepare failed")
	}
	e.requireSeen(r, y.ID)
	e.requireJob(leased)
	e.requireJob(y)
	// Nothing was held back: the same call succeeds once prepare does.
	ok := &recorder{}
	got, found, err := e.leasePrepared(leaseTTL+2*time.Second, githubWait, ok.prepare)
	e.must(err)
	if !found || got.ID != y.ID {
		e.t.Fatalf("Lease = job %d, found %v; want job %d", got.ID, found, y.ID)
	}
	e.requireJob(failed(leased, execution.ReasonLeaseExpired, leaseTTL+2*time.Second, firstBackoff))
}
