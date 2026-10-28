package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
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
// unblock read (docs/contracts/runtime.md, "Not responding"). A job that the head does not want, and a plan with no job yet, wait for nothing.
func TestAHeadReadsNotRespondingWhenTheRunnerIsSilentAndAWantedJobWaitsForIt(t *testing.T) {
	start := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	waits, github := execution.Job{State: execution.StatePending}, execution.Job{State: execution.StatePending, Reason: execution.ReasonGitHubUnavailable}
	leased, done := execution.Job{State: execution.StateLeased}, execution.Job{State: execution.StateComplete}
	for name, tc := range map[string]struct {
		silence time.Duration
		jobs    headJobs
		want    bool
	}{
		"the canonical job waits":                      {30 * time.Second, headJobs{canonicalPlanned: true, canonical: waits, hasCanonical: true}, true},
		"the receipt job waits, the canonical is done": {30 * time.Second, headJobs{canonicalPlanned: true, canonical: done, hasCanonical: true, receiptApplies: true, receipt: leased, hasReceipt: true}, true},
		"one waits on GitHub and the other is done":    {time.Hour, headJobs{canonicalPlanned: true, canonical: github, hasCanonical: true, receiptApplies: true, receipt: done, hasReceipt: true}, false},
		"the runner is not silent yet":                 {30*time.Second - time.Millisecond, headJobs{canonicalPlanned: true, canonical: waits, hasCanonical: true}, false},
		"a canonical job that the head does not want":  {time.Hour, headJobs{canonical: waits, hasCanonical: true}, false},
		"a receipt that does not apply":                {time.Hour, headJobs{receipt: waits, hasReceipt: true}, false},
		"no job yet":                                   {time.Hour, headJobs{canonicalPlanned: true, receiptApplies: true}, false},
	} {
		j := &checksJob{liveness: execution.NewLiveness(start), now: func() time.Time { return start.Add(tc.silence) }}
		if got := j.headNotResponding(tc.jobs); got != tc.want {
			t.Errorf("%s: headNotResponding = %v, want %v", name, got, tc.want)
		}
	}
}
