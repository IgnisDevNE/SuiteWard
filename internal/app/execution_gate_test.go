package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The canonical gate on the SuiteWard / Contract check (docs/contracts/runtime.md, "The canonical gate"): under canonical: required, and only where the
// check would be success, the canonical job of the head decides. These worlds plan the canonical run alone, so that a test leases the job it means.

var gateDecl = execDeclaration("required", "off")

// gateWorld is the default canonical world with canonical: required, whose only open pull request is pull 4 at the canonical's own head.
func gateWorld(t *testing.T) *cjJobWorld {
	t.Helper()
	w := newExecWorld(t, gateDecl)
	w.sync(map[int]string{4: bootHead})
	return w
}

// execPullFive opens pull 5 alone at cjHead2.
func (w *cjJobWorld) execPullFive(files map[string]string) {
	w.t.Helper()
	w.tree(cjHead2, files)
	w.sync(map[int]string{5: cjHead2})
	w.execRun()
}

// unblock records the owner's unblock of the jobs of a head, with the ledger entry its comment would leave.
func (w *cjJobWorld) unblock(pull int, comment int64, head string, jobs ...execution.Job) {
	w.t.Helper()
	e := commands.Entry{RepositoryID: bootRepoID, CommentID: comment, PullNumber: pull, AuthorID: bootOwnerID, CreatedAt: w.clock.Add(30 * time.Second),
		BodyDigest: artifact.Hash([]byte("body")), Disposition: commands.Unblocked, DecidedAt: w.clock.Add(31 * time.Second)}
	observed := make([]execution.UnblockJob, len(jobs))
	for i, job := range jobs {
		observed[i] = execution.UnblockJob{ID: job.ID, State: job.State, LeaseID: job.LeaseID}
	}
	if err := w.jobs.RecordUnblock(w.t.Context(), e, nil, head, observed, e.DecidedAt); err != nil {
		w.t.Fatal(err)
	}
}

// wantExecution requires the `execution` counts of a github_check payload.
func wantExecution(t *testing.T, payload map[string]any, tests, passed, skipped, failed, missing, flaky, excluded int) {
	t.Helper()
	got, ok := payload["execution"].(map[string]any)
	if !ok {
		t.Fatalf("payload %v has no execution counts", payload)
	}
	want := map[string]int{"tests": tests, "passed": passed, "skipped": skipped, "failed": failed, "missing": missing, "flaky": flaky, "excluded": excluded}
	if len(got) != len(want) {
		t.Errorf("execution counts %v, want exactly the keys of %v", got, want)
	}
	for key, n := range want {
		if value, found := got[key]; !found || value != float64(n) {
			t.Errorf("execution %s is %v, want %d (counts %v)", key, value, n, got)
		}
	}
}

func TestChecksHoldAHeadWhileItsCanonicalRunIsPending(t *testing.T) {
	w := gateWorld(t)

	w.execRun()

	w.want(4, checks.InProgress, "canonical-pending", 1)
	payload := w.lastCheckPayload(4)
	if _, found := payload["execution"]; found || payload["unblocked"] != nil {
		t.Errorf("payload %v: a pending run has no counts and no unblocked flag", payload)
	}
	w.execRun()
	w.want(4, checks.InProgress, "canonical-pending", 1) // nothing moved: the same generation
}

func TestChecksPassAHeadOnceItsCanonicalRunCompletedWithNothingBlocking(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	w.want(4, checks.InProgress, "canonical-pending", 1)

	w.finish(w.onlyJob(4, bootHead, execution.PlanCanonical), canonicalTests("pass", "pass", "skipped", "excluded"))
	w.execRun()

	record := w.want(4, checks.Success, "unchanged", 2) // the stored gate reason does not stick: the run completed
	if record.Unblocked {
		t.Error("a completed run is marked unblocked")
	}
	payload := w.lastCheckPayload(4)
	wantExecution(t, payload, 3, 2, 1, 0, 0, 0, 1)
	if payload["unblocked"] != nil {
		t.Errorf("payload %v marks unblocked", payload)
	}
}

func TestChecksPassAHeadWhoseCanonicalRunDeclaresNoTest(t *testing.T) {
	w := gateWorld(t)
	w.execRun()

	w.finish(w.onlyJob(4, bootHead, execution.PlanCanonical), canonicalTests())
	w.execRun()

	w.want(4, checks.Success, "unchanged", 2)
	wantExecution(t, w.lastCheckPayload(4), 0, 0, 0, 0, 0, 0, 0) // "0 tests ran": the counts are there, all zero
}

func TestChecksFailAHeadWhoseCanonicalRunHasAFailingMissingOrFlakyTest(t *testing.T) {
	cases := []struct {
		state                  string
		failed, missing, flaky int
	}{{"fail", 1, 0, 0}, {"missing", 0, 1, 0}, {"flaky", 0, 0, 1}}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			w := gateWorld(t)
			w.execRun()

			w.finish(w.onlyJob(4, bootHead, execution.PlanCanonical), canonicalTests("pass", "skipped", c.state, "excluded"))
			w.execRun()

			w.want(4, checks.Failure, "canonical-failing", 2)
			wantExecution(t, w.lastCheckPayload(4), 3, 1, 1, c.failed, c.missing, c.flaky, 1) // an excluded test is not one that ran
		})
	}
}

func TestChecksGateAReadyHeadAndPassItOnceItsRunCompleted(t *testing.T) {
	w := newExecWorld(t, gateDecl)
	w.execPullFive(execHeadFiles(gateDecl, "v2"))
	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.execRun()
	w.want(5, checks.InProgress, "canonical-pending", 2) // ready, and the run of the consented revision has not completed

	jobs := w.headJobs(5, cjHead2, execution.PlanCanonical)
	w.finish(jobs[len(jobs)-1], canonicalTests("pass"))
	w.execRun()

	record := w.want(5, checks.Success, "ready", 3)
	if record.Reference != "P2-R1" || record.Change == "unchanged" || record.Change == "" {
		t.Errorf("record %+v, want the reference and the protected change of the ready head kept", record)
	}
	wantExecution(t, w.lastCheckPayload(5), 1, 1, 0, 0, 0, 0, 0)
}

func TestChecksHoldAHeadWhoseCanonicalRunIsUnavailable(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	w.want(4, checks.InProgress, "canonical-pending", 1)

	w.clock = w.clock.Add(10 * time.Minute) // no executor is seen for the wait: the pending job is unavailable (no-executor)
	w.mustCheck()

	requireFinal(t, w.onlyJob(4, bootHead, execution.PlanCanonical), execution.ReasonNoExecutor)
	w.want(4, checks.InProgress, "canonical-unavailable", 2)
}

func TestChecksHoldAHeadWhoseCanonicalJobWasCreatedForAMissingExecutorToken(t *testing.T) {
	w := gateWorld(t)
	w.check.executorConfigured = false

	w.execRun()

	requireFinal(t, w.onlyJob(4, bootHead, execution.PlanCanonical), execution.ReasonNotConfigured)
	w.want(4, checks.InProgress, "canonical-unavailable", 1)
}

func TestChecksLiftAnUnavailableCanonicalRunOnlyForTheJobTheOwnerUnblocked(t *testing.T) {
	w := gateWorld(t)
	w.check.executorConfigured = false
	w.execRun()
	w.want(4, checks.InProgress, "canonical-unavailable", 1)
	unavailable := w.onlyJob(4, bootHead, execution.PlanCanonical)

	w.unblock(4, 301, bootHead, unavailable)
	w.execRun()

	record := w.want(4, checks.Success, "unchanged", 2)
	if !record.Unblocked {
		t.Error("the lifted check is not marked unblocked")
	}
	if payload := w.lastCheckPayload(4); payload["unblocked"] != true {
		t.Errorf("payload %v, want the publication marked unblocked", payload)
	} else if _, found := payload["execution"]; found {
		t.Errorf("payload %v has counts, but the unavailable run has none", payload)
	}

	w.check.executorConfigured = true // a job created later for the same head is not the one the owner unblocked
	w.execRun()

	w.want(4, checks.InProgress, "canonical-pending", 3)
	if record, _ := w.record(4); record.Unblocked {
		t.Error("a fresh job is lifted by the unblock of the old one")
	}
	if payload := w.lastCheckPayload(4); payload["unblocked"] != nil {
		t.Errorf("payload %v marks unblocked", payload)
	}
}

func TestChecksKeepEveryOtherReasonOverTheCanonicalGate(t *testing.T) {
	w := newExecWorld(t, gateDecl)
	w.execPullFive(execHeadFiles(gateDecl, "v2"))
	w.want(5, checks.InProgress, "approval-missing", 1)
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("fail")) // the run fails, and the approval is still what is missing

	w.execRun()

	w.want(5, checks.InProgress, "approval-missing", 2) // the failing run does not hold the check: the approval is still what is missing, and the check states the counts of the run
	wantExecution(t, w.lastCheckPayload(5), 1, 0, 0, 1, 0, 0, 0)

	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.execRun()
	jobs := w.headJobs(5, cjHead2, execution.PlanCanonical)
	w.finish(jobs[len(jobs)-1], canonicalTests("pass"))
	w.execRun()
	w.want(5, checks.Success, "ready", 4)

	w.entry(5, 302, bootOwnerID, commands.InvalidCode, "") // a wrong code
	w.execRun()

	w.want(5, checks.Failure, "owner-command-rejected", 5)
}

func TestChecksGateOnlyUnderRequired(t *testing.T) {
	for _, level := range []string{"advisory", "off"} {
		t.Run(level, func(t *testing.T) {
			decl := execDeclaration(level, "off")
			w := newExecWorld(t, decl)
			w.sync(map[int]string{4: bootHead})
			w.execRun()
			w.want(4, checks.Success, "unchanged", 1)
			if level == "off" {
				w.noJobs(4, bootHead, execution.PlanCanonical)
				return
			}

			w.finish(w.onlyJob(4, bootHead, execution.PlanCanonical), canonicalTests("fail", "missing")) // a failing advisory run changes nothing
			w.execRun()

			w.want(4, checks.Success, "unchanged", 1)
			payload := w.lastCheckPayload(4)
			if _, found := payload["execution"]; found || payload["unblocked"] != nil {
				t.Errorf("payload %v of an advisory check carries counts or the unblocked flag", payload)
			}
		})
	}
}

func TestChecksStateTheCanonicalRunInTheSameRunThatSweptIt(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	job := w.onlyJob(4, bootHead, execution.PlanCanonical)
	if _, found, err := w.jobs.Lease(t.Context(), execution.LeaseRequest{Now: w.clock, LeaseID: fmt.Sprintf("%032x", 7), JobTimeout: time.Hour, GitHubWait: time.Hour},
		func(context.Context, execution.Job) error { return nil }); err != nil || !found {
		t.Fatalf("lease: found %v, error %v", found, err)
	}
	w.want(4, checks.InProgress, "canonical-pending", 1)

	w.clock = w.clock.Add(2 * time.Minute) // the lease ran out: the job is queued again, and it reads pending
	w.execRun()

	if got := w.jobByID(job.ID); got.State != execution.StatePending || got.Failures != 1 {
		t.Fatalf("job %+v, want pending after one failure", got)
	}
	w.want(4, checks.InProgress, "canonical-pending", 1)
}

// unreadableContents is a content store that cannot be read.
type unreadableContents struct{}

func (unreadableContents) Read(context.Context, artifact.Digest) ([]byte, error) {
	return nil, errors.New("the content store is down")
}

func TestChecksNeverDecideASuccessWhileTheExecutionPolicyCannotBeRead(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	w.want(4, checks.InProgress, "canonical-pending", 1)
	w.tree(cjHead2, execHeadFiles(gateDecl, "v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2}) // a protected pull request writes a proposal: the Suite moves, and pull 4 is decided again
	w.execRun()
	w.finish(w.onlyJob(4, bootHead, execution.PlanCanonical), canonicalTests("pass")) // the run is complete: the check would pass
	w.want(4, checks.InProgress, "canonical-pending", 1)

	good := w.check.contents
	w.check.contents = unreadableContents{} // the canonical declaration, which holds the policy, cannot be read
	w.clock = w.clock.Add(time.Minute)
	if err := w.check.run(t.Context()); err == nil {
		t.Fatal("the run succeeded while the policy could not be read")
	}

	w.want(4, checks.InProgress, "canonical-pending", 1) // the stored check stays, and no success is published
	w.check.contents = good
	w.execRun()
	w.want(4, checks.Success, "unchanged", 2)
}

func TestChecksNeverEndALeasedJobOnTheGitHubBound(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	job := w.onlyJob(4, bootHead, execution.PlanCanonical)
	if _, err := w.jobs.GitHubFailure(t.Context(), job.ID, w.clock, w.check.executor.GitHubWait); err != nil { // the first GitHub failure of the job
		t.Fatal(err)
	}
	w.clock = w.clock.Add(2 * time.Minute) // its backoff passed
	leased, found, err := w.jobs.Lease(t.Context(), execution.LeaseRequest{Now: w.clock, LeaseID: fmt.Sprintf("%032x", 9), JobTimeout: time.Hour, GitHubWait: time.Hour},
		func(context.Context, execution.Job) error { return nil })
	if err != nil || !found || leased.ID != job.ID {
		t.Fatalf("lease: job %d, found %v, error %v", leased.ID, found, err)
	}

	for range 40 { // longer than the wait, with an executor that keeps its lease alive
		for _, offset := range []time.Duration{20 * time.Second, 40 * time.Second} { // a lease lasts a minute, and a run is a minute after the last
			if _, err := w.jobs.Heartbeat(t.Context(), job.ID, leased.LeaseID, w.clock.Add(offset)); err != nil {
				t.Fatal(err)
			}
		}
		w.execRun()
	}

	if got := w.jobByID(job.ID); got.State != execution.StateLeased {
		t.Fatalf("the job is %s/%s, want it leased: the GitHub bound never ends a leased job", got.State, got.Reason)
	}
}
