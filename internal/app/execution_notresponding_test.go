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

// pullFourState is the state of pull 4 at the head of the canonical, as the checks job holds it for a head.
func pullFourState() *pullState {
	return &pullState{p: owner.ProjectPolicy{RepositoryID: bootRepoID}, pull: githubsync.PullState{Number: 4, HeadSHA: bootHead}}
}

// readHead asks whether a head reads not responding, with the silence as the caller read it. The clock of the world says the opposite, so that a function that read
// the clock again would answer wrongly.
func (w *cjJobWorld) readHead(x *executionRun, jobs headJobs, silent bool) (domain.Set, domain.JobPair, bool, error) {
	w.t.Helper()
	w.clock = w.check.liveness.LastSeen()
	if !silent {
		w.clock = w.clock.Add(time.Hour)
	}
	return w.check.headNotResponding(w.t.Context(), pullFourState(), x, jobs, silent)
}

// A head reads "not responding" when the runner is silent and at least one wanted job of the head waits for it: the state that the comment of the head and the
// unblock read (docs/contracts/runtime.md, "Not responding", "Definitions"). The wanted jobs are the canonical job when the canonical level is not off, whatever the
// pass decided of the head, and the receipt job when the receipt level is not off and the receipt applies. A job that is not wanted, and a plan with no job yet, wait
// for nothing. The answer names the jobs that wait, 0 for none. The silence is the caller's, read once for the pass: the function never reads the clock.
func TestAHeadReadsNotRespondingWhenTheRunnerIsSilentAndAWantedJobWaitsForIt(t *testing.T) {
	w := gateWorld(t)
	w.execRun() // the canonical job of pull 4 is pending, and stays so
	stored := w.onlyJob(4, bootHead, execution.PlanCanonical)
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
	for name, tc := range map[string]struct {
		x       *executionRun
		silent  bool
		jobs    headJobs
		want    domain.JobPair
		reading bool
	}{
		"the canonical job waits":                      {required, true, headJobs{canonicalPlanned: true, canonical: waits, hasCanonical: true}, domain.JobPair{Canonical: 90}, true},
		"the receipt job waits, the canonical is done": {required, true, headJobs{canonicalPlanned: true, canonical: done, hasCanonical: true, receiptApplies: true, receipt: leased, hasReceipt: true}, domain.JobPair{Receipt: 91}, true},
		"both wait": {required, true, headJobs{canonicalPlanned: true, canonical: waits, hasCanonical: true, receiptApplies: true, receipt: leased, hasReceipt: true}, domain.JobPair{Canonical: 90, Receipt: 91}, true},
		"one waits on GitHub and the other is done": {required, true, headJobs{canonicalPlanned: true, canonical: github, hasCanonical: true, receiptApplies: true, receipt: job(91, execution.StateComplete, ""), hasReceipt: true}, domain.JobPair{}, false},
		"the runner is not silent":                  {required, false, headJobs{canonicalPlanned: true, canonical: waits, hasCanonical: true}, domain.JobPair{}, false},
		"an advisory canonical level is wanted":     {advisory, true, headJobs{canonicalPlanned: true, canonical: waits, hasCanonical: true}, domain.JobPair{Canonical: 90}, true},
		"a canonical level that is off":             {off, true, headJobs{canonical: waits, hasCanonical: true}, domain.JobPair{}, false},
		"a receipt that does not apply":             {required, true, headJobs{canonicalPlanned: true, canonical: done, hasCanonical: true, receipt: leased, hasReceipt: true}, domain.JobPair{}, false},
		"a receipt level that is off":               {canonicalOnly, true, headJobs{canonicalPlanned: true, canonical: done, hasCanonical: true, receiptApplies: true, receipt: leased, hasReceipt: true}, domain.JobPair{}, false},
		"no job yet":                                {required, true, headJobs{canonicalPlanned: true, receiptApplies: true}, domain.JobPair{}, false},
		"a policy that was not read":                {&executionRun{}, true, headJobs{canonicalPlanned: true, canonical: waits, hasCanonical: true}, domain.JobPair{}, false},
		"a policy that names no execution":          {&executionRun{known: true}, true, headJobs{}, domain.JobPair{}, false},
		// The check of this head was decided by another rule, so the pass planned no canonical job and read none, but the job that is still open waits.
		"a canonical job that the pass did not read": {required, true, headJobs{}, domain.JobPair{Canonical: stored.ID}, true},
	} {
		t.Run(name, func(t *testing.T) {
			_, got, reading, err := w.readHead(tc.x, tc.jobs, tc.silent)
			if err != nil || got != tc.want || reading != tc.reading {
				t.Errorf("headNotResponding = %+v, %v, %v; want %+v, %v", got, reading, err, tc.want, tc.reading)
			}
		})
	}
}

// The Set that the head reads from is returned with the answer, so that the comment fills the rest of it (the reason of the head, the earlier job, the counts) and
// reads its Unblock, Holds and Next from the same jobs and levels, never from a rebuilt copy: the canonical job that the pass did not read is in it too.
func TestAHeadReturnsTheSetItReadsNotRespondingFrom(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	stored := w.onlyJob(4, bootHead, execution.PlanCanonical)
	x := &executionRun{known: true, policy: executionPolicy{canonical: domain.CanonicalLevelRequired, receipt: domain.ReceiptLevelBeforeApproval}}
	receipt := execution.Job{ID: 91, State: execution.StateLeased, Spec: execution.Spec{GoverningDigest: stored.GoverningDigest}}

	set, waiting, reading, err := w.readHead(x, headJobs{receiptApplies: true, receipt: receipt, hasReceipt: true}, true)

	if err != nil || !reading || waiting != (domain.JobPair{Canonical: stored.ID, Receipt: 91}) {
		t.Fatalf("headNotResponding = %+v, %v, %v; want both jobs, reading", waiting, reading, err)
	}
	canonicalJob, _ := stored.InSet(false)
	want := domain.Set{CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelBeforeApproval, ReceiptApplies: true, CanonicalJob: canonicalJob,
		ReceiptJob: domain.Job{ID: 91, State: domain.JobLeased, Governing: stored.GoverningDigest}}
	if set != want {
		t.Errorf("the Set = %+v; want %+v", set, want)
	}
	// Asked again by the caller, with the silence it read, the Set gives the same jobs: there is one rule.
	if again, ok := set.NotResponding(true); !ok || again.Jobs != waiting {
		t.Errorf("Set.NotResponding(true) = %+v, %v; want the jobs %+v", again, ok, waiting)
	}
}

// A cancelled job is not one that a head reads: the gates never return one, so it is a broken state and not a job that waits for nothing.
func TestAHeadThatReadsACancelledJobIsAnError(t *testing.T) {
	w := gateWorld(t)
	x := &executionRun{known: true, policy: executionPolicy{canonical: domain.CanonicalLevelRequired, receipt: domain.ReceiptLevelOff}}
	cancelled := execution.Job{ID: 90, State: execution.StateCancelled, Reason: execution.ReasonClosed}
	if set, got, reading, err := w.readHead(x, headJobs{canonicalPlanned: true, canonical: cancelled, hasCanonical: true}, true); err == nil {
		t.Errorf("headNotResponding = %+v, %+v, %v, nil; want an error for the cancelled job", set, got, reading)
	}
}

// One pass reads the silence of the runner once (docs/contracts/runtime.md, "Not responding": the state is a function of the stored jobs, the clock and lastSeenAt):
// whenever the clock crosses the 30 s between the reads that the pass makes, the Contract check and the Receipt check of a head still agree, because both use the one
// reading. The clock is a step: the calls before the crossing read 29.999 s of silence, the rest read 30 s.
func TestAPassReadsTheSilenceOfTheRunnerOnceForEveryCheckOfAHead(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPullFive(execHeadFiles(execDecl, "v2"))
	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.execRun() // the head is ready: its two jobs wait
	lastSeen := w.check.liveness.LastSeen()
	before, after := lastSeen.Add(30*time.Second-time.Millisecond), lastSeen.Add(30*time.Second)
	calls, crossing := 0, 0
	w.check.now = func() time.Time {
		calls++
		if calls <= crossing {
			return before
		}
		return after
	}
	run := func() (contractSilent, receiptSilent bool) {
		calls = 0
		w.mustCheck()
		record, found := w.record(5)
		stored, foundReceipt := w.receipt(5)
		if !found || !foundReceipt {
			t.Fatalf("crossing %d: Contract check found %v, Receipt check found %v", crossing, found, foundReceipt)
		}
		return record.Reason == "canonical-not-responding", stored.Reason == "receipt-not-responding"
	}
	run()
	reads := calls // the clock is read this many times by a pass, wherever the silence is read
	sawNotResponding, sawWaiting := false, false
	for crossing = 0; crossing <= reads; crossing++ {
		contractSilent, receiptSilent := run()
		if contractSilent != receiptSilent {
			t.Fatalf("with the clock crossing 30 s after %d reads, the Contract check says not-responding = %v and the Receipt check says %v", crossing, contractSilent, receiptSilent)
		}
		sawNotResponding, sawWaiting = sawNotResponding || contractSilent, sawWaiting || !contractSilent
	}
	if !sawNotResponding || !sawWaiting {
		t.Errorf("the passes said not-responding: %v, and a plain wait: %v; want both, so that the clock really crossed the limit", sawNotResponding, sawWaiting)
	}
}
