package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// This file is the lifecycle of a job as pure functions (docs/contracts/runtime.md, "Job lifecycle"). The stores load a job, apply
// one of them and write the result, so that the in-memory store and the PostgreSQL store cannot disagree about a rule. Every time
// they take is at the stored precision (see Stored).

// Stored is a time at the microsecond precision that PostgreSQL stores, in UTC.
func Stored(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// Final reports whether the state is one that a job never leaves.
func (s State) Final() bool {
	return s == StateComplete || s == StateUnavailable || s == StateCancelled
}

// Retried reports whether an attempt that failed with the reason is run again: it counts a failure and the job is final at the third.
func (r Reason) Retried() bool {
	switch r {
	case ReasonExecutorError, ReasonSandboxUnavailable, ReasonArchiveUnavailable, ReasonLeaseExpired, ReasonTimeout, ReasonFetchFailed:
		return true
	}
	return false
}

// executorSet reports whether an executor may give the reason in an unavailable result.
func (r Reason) executorSet() bool {
	switch r {
	case ReasonExecutorError, ReasonSandboxUnavailable, ReasonArchiveUnavailable, ReasonGitHubUnavailable, ReasonFetchFailed, ReasonInstallFailed,
		ReasonResourceLimit, ReasonToolchainMismatch, ReasonSourceMismatch, ReasonTooLarge, ReasonProfileUnsupported:
		return true
	}
	return false
}

// createdFinal reports whether a job may be created already unavailable for the reason.
func (r Reason) createdFinal() bool {
	return r == ReasonNoMergeBase || r == ReasonNotConfigured || r == ReasonGitHubUnavailable
}

// cancelReason reports whether the reason is one of a cancelled job.
func (r Reason) cancelReason() bool {
	return r == ReasonHeadMoved || r == ReasonClosed || r == ReasonObsolete
}

// Key is the key of the job: the SHA-256 of six lines, each ended by a newline, in lower-case hexadecimal: the plan, the profile, the
// head SHA, the base SHA (empty for a canonical job and for a job created without one), the governing manifest digest and the digest
// of the canonical declaration.
func (s Spec) Key() string {
	h := sha256.New()
	for _, line := range []string{string(s.Plan), s.Profile, s.HeadSHA, s.BaseSHA, s.GoverningDigest.String(), s.DeclarationDigest.String()} {
		h.Write([]byte(line + "\n")) // a hash never fails to write
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Current reports whether the job holds its key: it is not cancelled, and not unavailable for executor-not-configured or no-executor,
// which a fresh job replaces once an executor is configured or seen.
func (j Job) Current() bool {
	if j.State == StateCancelled {
		return false
	}
	return j.State != StateUnavailable || (j.Reason != ReasonNotConfigured && j.Reason != ReasonNoExecutor)
}

// Job is the job that n creates, without its id.
func (n NewJob) Job() Job {
	at := Stored(n.At)
	j := Job{Spec: n.Spec, Key: n.Key(), State: StatePending, NotBefore: at, CreatedAt: at}
	if n.Unavailable != "" {
		j.State, j.Reason, j.FinishedAt = StateUnavailable, n.Unavailable, at
	}
	return j
}

// Ready reports whether a lease may be granted for the job: pending with its not_before reached.
func (j Job) Ready(now time.Time) bool { return j.State == StatePending && !now.Before(j.NotBefore) }

// LeaseExpired reports whether the lease of a leased job has run out.
func (j Job) LeaseExpired(now time.Time) bool {
	return j.State == StateLeased && !now.Before(j.LeaseExpiresAt)
}

// CheckLease is ErrLeaseLost unless the job is leased with this lease and the lease has not expired.
func (j Job) CheckLease(leaseID string, now time.Time) error {
	if j.State != StateLeased || j.LeaseID != leaseID || !now.Before(j.LeaseExpiresAt) {
		return ErrLeaseLost
	}
	return nil
}

func earlier(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// Granted is the job after a lease for the request: one more attempt, a new lease id, and an expiry that never passes the deadline.
func (j Job) Granted(req LeaseRequest) Job {
	now := Stored(req.Now)
	j.State = StateLeased
	j.Attempts++
	j.LeaseID, j.LeasedAt = req.LeaseID, now
	j.DeadlineAt = Stored(now.Add(req.JobTimeout))
	j.LeaseExpiresAt = earlier(Stored(now.Add(LeaseTTL)), j.DeadlineAt)
	return j
}

// Heartbeated is the job with its lease extended by a heartbeat, or ErrLeaseLost.
func (j Job) Heartbeated(leaseID string, now time.Time) (Job, error) {
	if err := j.CheckLease(leaseID, now); err != nil {
		return j, err
	}
	j.LeaseExpiresAt = earlier(Stored(now.Add(LeaseTTL)), j.DeadlineAt)
	return j, nil
}

// ended is the job after it became final.
func (j Job) ended(state State, reason Reason, now time.Time) Job {
	j.State, j.Reason, j.FinishedAt = state, reason, now
	j.LeaseExpiresAt, j.DeadlineAt = time.Time{}, time.Time{}
	return j
}

// Cancelled is the job after its binding was no longer wanted.
func (j Job) Cancelled(reason Reason, now time.Time) Job { return j.ended(StateCancelled, reason, now) }

// failedAttempt is the job after an attempt that an executor lost or failed with a retried reason: it counts a failure, which is not
// GitHub's and so clears the first GitHub failure. The job waits 30 s after the first failure and 2 minutes after the second, and is
// unavailable with the reason at the third.
func (j Job) failedAttempt(reason Reason, now time.Time) Job {
	j.Failures++
	j.GitHubSince = time.Time{}
	if j.Failures >= MaxFailures {
		return j.ended(StateUnavailable, reason, now)
	}
	backoff := 30 * time.Second
	if j.Failures > 1 {
		backoff = 2 * time.Minute
	}
	j.State, j.Reason, j.NotBefore = StatePending, reason, Stored(now.Add(backoff))
	j.LeaseExpiresAt, j.DeadlineAt = time.Time{}, time.Time{}
	return j
}

// LeaseLapsed is the leased job after its lease ran out with no result: a failure for lease-expired, or for timeout when it ran past
// its deadline.
func (j Job) LeaseLapsed(now time.Time) Job {
	if !now.Before(j.DeadlineAt) {
		return j.failedAttempt(ReasonTimeout, now)
	}
	return j.failedAttempt(ReasonLeaseExpired, now)
}

// githubBoundReached reports whether the first GitHub failure of the job is at least a whole wait old.
func (j Job) githubBoundReached(now time.Time, wait time.Duration) bool {
	return !j.GitHubSince.IsZero() && !now.Before(j.GitHubSince.Add(wait))
}

// githubFailed is the job after a read for it failed: no failure and no attempt is counted, the job is pending with the reason
// github-unavailable and waits a minute, and the first failure is kept.
func (j Job) githubFailed(now time.Time) Job {
	if j.GitHubSince.IsZero() {
		j.GitHubSince = now
	}
	j.State, j.Reason, j.NotBefore = StatePending, ReasonGitHubUnavailable, Stored(now.Add(GitHubBackoff))
	j.LeaseExpiresAt, j.DeadlineAt = time.Time{}, time.Time{}
	return j
}

// TooLarge is the ready job after prepare found its commit too large to lease: unavailable at once, with no attempt. The service writes
// no results row for it.
func (j Job) TooLarge(now time.Time) Job { return j.ended(StateUnavailable, ReasonTooLarge, now) }

// AfterGitHubFailure is the pending job after a read for it failed, and whether the failure ended it on the GitHub bound, in which
// case the service writes the results row (ServiceResult).
func (j Job) AfterGitHubFailure(now time.Time, wait time.Duration) (next Job, serviceRow bool) {
	next = j.githubFailed(now)
	if next.githubBoundReached(now, wait) {
		return next.ended(StateUnavailable, ReasonGitHubUnavailable, now), true
	}
	return next, false
}

// ServiceResult is the results row of a job that the service ended on the GitHub bound while it was pending: no executor, image or
// runtime took part, and it runs from the first failure to the bound.
func ServiceResult(j Job, now time.Time) StoredResult {
	return StoredResult{JobID: j.ID, Origin: OriginService, ReceivedAt: now,
		Report: Report{Status: StateUnavailable, Reason: ReasonGitHubUnavailable, StartedAt: j.GitHubSince, EndedAt: now}}
}

// Settlement is what a result does to a leased job: the job afterwards, and whether the result is stored (a results row of origin
// executor, with the tests of a complete one). A result that queues the job again stores nothing.
type Settlement struct {
	Job   Job
	Store bool
}

// Settle applies a valid result of an executor to the leased job. A result clears the first GitHub failure, except that a
// give-back as github-unavailable records one and queues the job again, or, a whole wait after the first, is the final state.
func (j Job) Settle(r Result, now time.Time, githubWait time.Duration) Settlement {
	switch {
	case r.Status == StateComplete:
		next := j.ended(StateComplete, "", now)
		next.GitHubSince = time.Time{}
		return Settlement{Job: next, Store: true}
	case r.Reason == ReasonGitHubUnavailable && j.githubBoundReached(now, githubWait):
		return Settlement{Job: j.ended(StateUnavailable, ReasonGitHubUnavailable, now), Store: true}
	case r.Reason == ReasonGitHubUnavailable:
		return Settlement{Job: j.githubFailed(now)}
	case r.Reason.Retried():
		next := j.failedAttempt(r.Reason, now)
		return Settlement{Job: next, Store: next.State == StateUnavailable}
	}
	next := j.ended(StateUnavailable, r.Reason, now)
	next.GitHubSince = time.Time{}
	return Settlement{Job: next, Store: true}
}

// Swept is the open job after the sweep (Store.Expire): a lease that ran out counts a failure; then, when no executor was seen for
// the wait, a pending job is unavailable for no-executor; otherwise a pending job whose reason is github-unavailable is unavailable
// for github-unavailable once its first failure is a whole wait old, and the service writes its results row. A leased job is never
// ended by the GitHub bound.
func (j Job) Swept(p ExpireParams) (next Job, changed, serviceRow bool) {
	now := Stored(p.Now)
	next = j
	if next.LeaseExpired(now) {
		next, changed = next.LeaseLapsed(now), true
	}
	if next.State != StatePending {
		return next, changed, false
	}
	switch {
	case !now.Before(Stored(p.LastSeenAt).Add(p.LeaseWait)):
		return next.ended(StateUnavailable, ReasonNoExecutor, now), true, false
	case next.Reason == ReasonGitHubUnavailable && next.githubBoundReached(now, p.GitHubWait):
		return next.ended(StateUnavailable, ReasonGitHubUnavailable, now), true, true
	}
	return next, changed, false
}
