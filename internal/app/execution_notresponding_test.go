package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// A runner that does not respond (docs/contracts/runtime.md, "Not responding"): when no executor was seen for 30 s and a wanted job of the head waits for the
// runner, the Contract check (under canonical: required, where the gate would hold it) says canonical-not-responding in place of canonical-pending and the
// Receipt check says receipt-not-responding in place of receipt-running. Each check reads its own job.

// silentFor moves the clock so that the next run of the checks job, which starts a minute after it, is d after the executor was last seen, without a request
// from it.
func (w *cjJobWorld) silentFor(d time.Duration) {
	w.clock = w.check.liveness.LastSeen().Add(d - time.Minute)
}

// runnerAnswers is a request of the executor at the time of the next run.
func (w *cjJobWorld) runnerAnswers() { w.check.liveness.Saw(w.clock.Add(time.Minute)) }

// leaseJob leases the oldest ready job, which must be job id, at the last sight of the runner: its lease lasts a minute, so the job is still leased while the
// runner has been silent for less.
func (w *cjJobWorld) leaseJob(id int64) execution.Job {
	w.t.Helper()
	req := execution.LeaseRequest{Now: w.check.liveness.LastSeen(), LeaseID: fmt.Sprintf("%032x", id), JobTimeout: time.Hour, GitHubWait: time.Hour}
	leased, found, err := w.jobs.Lease(w.t.Context(), req, func(context.Context, execution.Job) error { return nil })
	if err != nil || !found || leased.ID != id {
		w.t.Fatalf("lease of job %d: got job %d, found %v, error %v", id, leased.ID, found, err)
	}
	return leased
}

// waitOnGitHub makes the job wait on GitHub: it is pending, with the reason github-unavailable.
func (w *cjJobWorld) waitOnGitHub(id int64) {
	w.t.Helper()
	if _, err := w.jobs.GitHubFailure(w.t.Context(), id, w.clock, w.check.executor.GitHubWait); err != nil {
		w.t.Fatal(err)
	}
}

func TestChecksSayThatTheRunnerDoesNotRespondFromThirtySecondsOfSilence(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	w.want(4, checks.InProgress, "canonical-pending", 1)

	w.silentFor(30*time.Second - time.Millisecond)
	w.mustCheck()
	w.want(4, checks.InProgress, "canonical-pending", 1) // a moment short of 30 s

	w.silentFor(30 * time.Second)
	w.mustCheck()
	w.want(4, checks.InProgress, "canonical-not-responding", 2)

	w.runnerAnswers()
	w.mustCheck()
	w.want(4, checks.InProgress, "canonical-pending", 3)
}

func TestChecksSayThatTheRunnerDoesNotRespondForALeasedJobAndForOneQueuedAgainAfterALostLease(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	job := w.onlyJob(4, bootHead, execution.PlanCanonical)
	w.leaseJob(job.ID)

	w.silentFor(45 * time.Second)
	w.mustCheck()

	if got := w.jobByID(job.ID); got.State != execution.StateLeased {
		t.Fatalf("the job is %s, want leased", got.State)
	}
	w.want(4, checks.InProgress, "canonical-not-responding", 2)

	w.silentFor(2 * time.Minute) // the lease ran out: the job is queued again with its backoff
	w.mustCheck()
	if got := w.jobByID(job.ID); got.State != execution.StatePending || got.Reason != execution.ReasonLeaseExpired {
		t.Fatalf("the job is %s/%s, want pending/lease-expired", got.State, got.Reason)
	}
	w.want(4, checks.InProgress, "canonical-not-responding", 2)
}

func TestChecksKeepAJobThatWaitsOnGitHubPendingWhateverTheSilenceOfTheRunner(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	w.waitOnGitHub(w.onlyJob(4, bootHead, execution.PlanCanonical).ID)

	w.silentFor(45 * time.Second)
	w.mustCheck()

	w.want(4, checks.InProgress, "canonical-pending", 1) // it waits for GitHub, which has its own bound
}

func TestChecksDoNotChangeTheContractCheckOfAnAdvisoryOrOffCanonicalRunWhenTheRunnerIsSilent(t *testing.T) {
	for _, level := range []string{"advisory", "off"} {
		t.Run(level, func(t *testing.T) {
			w := newExecWorld(t, execDeclaration(level, "off"))
			w.sync(map[int]string{4: bootHead})
			w.execRun()
			w.want(4, checks.Success, "unchanged", 1)

			w.silentFor(45 * time.Second)
			w.mustCheck()

			w.want(4, checks.Success, "unchanged", 1)
		})
	}
}

func TestChecksKeepTheApprovalMissingReasonOverTheRunnerThatDoesNotRespond(t *testing.T) {
	w := newExecWorld(t, gateDecl)
	w.execPullFive(execHeadFiles(gateDecl, "v2"))
	w.want(5, checks.InProgress, "approval-missing", 1)

	w.silentFor(45 * time.Second)
	w.mustCheck()

	w.want(5, checks.InProgress, "approval-missing", 1)
}

// The stored reason is a gate reason like the others: it is not the check that the head would have without the gate, and a pass that cannot read the
// policy keeps it.
func TestChecksPassAHeadWhoseRunnerDidNotRespondOnceTheRunCompletes(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	w.silentFor(45 * time.Second)
	w.mustCheck()
	w.want(4, checks.InProgress, "canonical-not-responding", 2)

	w.runnerAnswers()
	w.finish(w.onlyJob(4, bootHead, execution.PlanCanonical), canonicalTests("pass"))
	w.mustCheck()

	w.want(4, checks.Success, "unchanged", 3)
}

func TestChecksKeepTheNotRespondingCheckWhileThePolicyCannotBeRead(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	w.silentFor(45 * time.Second)
	w.mustCheck()
	w.want(4, checks.InProgress, "canonical-not-responding", 2)

	good := w.check.contents
	w.check.contents = unreadableContents{}
	w.silentFor(50 * time.Second)
	if err := w.check.run(t.Context()); err == nil {
		t.Fatal("the run succeeded while the policy could not be read")
	}

	w.want(4, checks.InProgress, "canonical-not-responding", 2) // the stored check stays
	w.check.contents = good
}

func TestChecksReadTheReceiptJobAloneForTheReceiptCheckWhenTheRunnerDoesNotRespond(t *testing.T) {
	for _, tc := range []struct {
		level  string
		offers bool
	}{{"required", true}, {"before-approval", true}, {"advisory", false}} {
		t.Run(tc.level, func(t *testing.T) {
			w := receiptWorld(t, tc.level)
			w.wantReceipt(execution.CheckInProgress, "receipt-running", 1)

			w.silentFor(30*time.Second - time.Millisecond)
			w.mustCheck()
			w.wantReceipt(execution.CheckInProgress, "receipt-running", 1)

			w.silentFor(30 * time.Second)
			w.mustCheck()
			w.wantReceipt(execution.CheckInProgress, "receipt-not-responding", 2)
			payload := w.lastReceiptPayload()
			if offered := payload["offersUnblock"] == true; offered != tc.offers {
				t.Errorf("payload %v: the unblock is offered = %v, want %v: the receipt gate refuses an approve only under required and before-approval", payload, offered, tc.offers)
			}
			if payload["reason"] != "receipt-not-responding" || payload["state"] != "in_progress" {
				t.Errorf("payload %v, want in_progress/receipt-not-responding", payload)
			}

			w.runnerAnswers()
			w.mustCheck()
			w.wantReceipt(execution.CheckInProgress, "receipt-running", 3)
			if payload := w.lastReceiptPayload(); payload["offersUnblock"] != nil {
				t.Errorf("payload %v offers the unblock of a receipt that is running", payload)
			}
		})
	}
}

// Each check reads its own job: a canonical job that waits on GitHub is canonical-pending although the receipt job waits for the runner, and the reverse.
func TestChecksReadEachJobForItsOwnCheckWhenOnlyOneWaitsForTheRunner(t *testing.T) {
	for name, tc := range map[string]struct {
		githubPlan                    execution.Plan
		contractReason, receiptReason string
	}{
		"the canonical job waits on GitHub": {execution.PlanCanonical, "canonical-pending", "receipt-not-responding"},
		"the receipt job waits on GitHub":   {execution.PlanReceipt, "canonical-not-responding", "receipt-running"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newExecWorld(t, execDecl)
			w.execPullFive(execHeadFiles(execDecl, "v2"))
			w.consent(contract.ApproveConsent, 201, 2, 1, 5)
			w.execRun() // the head is ready: its jobs wait
			jobs := w.headJobs(5, cjHead2, tc.githubPlan)
			w.waitOnGitHub(jobs[len(jobs)-1].ID)

			w.silentFor(45 * time.Second)
			w.mustCheck()

			if record, found := w.record(5); !found || record.State != checks.InProgress || record.Reason != tc.contractReason {
				t.Errorf("Contract check %+v (found %v), want in_progress/%s", record, found, tc.contractReason)
			}
			if record, found := w.receipt(5); !found || record.Reason != tc.receiptReason {
				t.Errorf("Receipt check %+v (found %v), want %s", record, found, tc.receiptReason)
			}
		})
	}
}

// A head reads "not responding" when the runner is silent and at least one wanted job of the head waits for it: the state that the comment of the head and the
// unblock read (docs/contracts/runtime.md, "Not responding", "Definitions"). The wanted jobs are the canonical job when the canonical level is not off, whatever the
// pass decided of the head, and the receipt job when the receipt level is not off and the receipt applies. A job that is not wanted, and a plan with no job yet, wait
// for nothing. The answer names the jobs that wait, 0 for none.
func TestAHeadReadsNotRespondingWhenTheRunnerIsSilentAndAWantedJobWaitsForIt(t *testing.T) {
	w := gateWorld(t)
	w.execRun() // the canonical job of pull 4 is pending, and stays so
	stored := w.onlyJob(4, bootHead, execution.PlanCanonical)
	s := &pullState{p: owner.ProjectPolicy{RepositoryID: bootRepoID}, pull: githubsync.PullState{Number: 4, HeadSHA: bootHead}}
	policy := func(canonical domain.CanonicalLevel, receipt domain.ReceiptLevel) *executionRun {
		return &executionRun{known: true, policy: executionPolicy{canonical: canonical, receipt: receipt}}
	}
	required, advisory, off := policy(domain.CanonicalLevelRequired, domain.ReceiptLevelRequired), policy(domain.CanonicalLevelAdvisory, domain.ReceiptLevelOff), policy(domain.CanonicalLevelOff, domain.ReceiptLevelRequired)
	canonicalOnly := policy(domain.CanonicalLevelRequired, domain.ReceiptLevelOff)
	job := func(id int64, state execution.State, reason execution.Reason) execution.Job {
		return execution.Job{ID: id, State: state, Reason: reason, Spec: execution.Spec{GoverningDigest: stored.GoverningDigest}}
	}
	waits, leased := job(90, execution.StatePending, ""), job(91, execution.StateLeased, "")
	github, done := job(90, execution.StatePending, execution.ReasonGitHubUnavailable), job(90, execution.StateComplete, "")
	silent, tooSoon := 30*time.Second, 30*time.Second-time.Millisecond
	for name, tc := range map[string]struct {
		x       *executionRun
		silence time.Duration
		jobs    headJobs
		want    domain.JobPair
		reading bool
	}{
		"the canonical job waits":                      {required, silent, headJobs{canonicalPlanned: true, canonical: waits, hasCanonical: true}, domain.JobPair{Canonical: 90}, true},
		"the receipt job waits, the canonical is done": {required, silent, headJobs{canonicalPlanned: true, canonical: done, hasCanonical: true, receiptApplies: true, receipt: leased, hasReceipt: true}, domain.JobPair{Receipt: 91}, true},
		"both wait": {required, silent, headJobs{canonicalPlanned: true, canonical: waits, hasCanonical: true, receiptApplies: true, receipt: leased, hasReceipt: true}, domain.JobPair{Canonical: 90, Receipt: 91}, true},
		"one waits on GitHub and the other is done": {required, time.Hour, headJobs{canonicalPlanned: true, canonical: github, hasCanonical: true, receiptApplies: true, receipt: job(91, execution.StateComplete, ""), hasReceipt: true}, domain.JobPair{}, false},
		"the runner is not silent yet":              {required, tooSoon, headJobs{canonicalPlanned: true, canonical: waits, hasCanonical: true}, domain.JobPair{}, false},
		"an advisory canonical level is wanted":     {advisory, silent, headJobs{canonicalPlanned: true, canonical: waits, hasCanonical: true}, domain.JobPair{Canonical: 90}, true},
		"a canonical level that is off":             {off, time.Hour, headJobs{canonical: waits, hasCanonical: true}, domain.JobPair{}, false},
		"a receipt that does not apply":             {required, time.Hour, headJobs{canonicalPlanned: true, canonical: done, hasCanonical: true, receipt: leased, hasReceipt: true}, domain.JobPair{}, false},
		"a receipt level that is off":               {canonicalOnly, time.Hour, headJobs{canonicalPlanned: true, canonical: done, hasCanonical: true, receiptApplies: true, receipt: leased, hasReceipt: true}, domain.JobPair{}, false},
		"no job yet":                                {required, time.Hour, headJobs{canonicalPlanned: true, receiptApplies: true}, domain.JobPair{}, false},
		"a policy that was not read":                {&executionRun{}, time.Hour, headJobs{canonicalPlanned: true, canonical: waits, hasCanonical: true}, domain.JobPair{}, false},
		"a policy that names no execution":          {&executionRun{known: true}, time.Hour, headJobs{}, domain.JobPair{}, false},
		// The check of this head was decided by another rule, so the pass planned no canonical job and read none, but the job that is still open waits.
		"a canonical job that the pass did not read": {required, silent, headJobs{}, domain.JobPair{Canonical: stored.ID}, true},
	} {
		t.Run(name, func(t *testing.T) {
			w.clock = w.check.liveness.LastSeen().Add(tc.silence)
			got, reading, err := w.check.headNotResponding(t.Context(), s, tc.x, tc.jobs)
			if err != nil || got != tc.want || reading != tc.reading {
				t.Errorf("headNotResponding = %+v, %v, %v; want %+v, %v", got, reading, err, tc.want, tc.reading)
			}
		})
	}
}

// A cancelled job is not one that a head reads: the gates never return one, so it is a broken state and not a job that waits for nothing.
func TestAHeadThatReadsACancelledJobIsAnError(t *testing.T) {
	w := gateWorld(t)
	s := &pullState{p: owner.ProjectPolicy{RepositoryID: bootRepoID}, pull: githubsync.PullState{Number: 4, HeadSHA: bootHead}}
	x := &executionRun{known: true, policy: executionPolicy{canonical: domain.CanonicalLevelRequired, receipt: domain.ReceiptLevelOff}}
	cancelled := execution.Job{ID: 90, State: execution.StateCancelled, Reason: execution.ReasonClosed}
	w.clock = w.check.liveness.LastSeen().Add(time.Hour)
	if got, reading, err := w.check.headNotResponding(t.Context(), s, x, headJobs{canonicalPlanned: true, canonical: cancelled, hasCanonical: true}); err == nil {
		t.Errorf("headNotResponding = %+v, %v, nil; want an error for the cancelled job", got, reading)
	}
}
