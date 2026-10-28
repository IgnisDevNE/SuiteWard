package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// The early unblock (docs/contracts/runtime.md, "The early unblock and its hold"): while the runner does not respond, /suiteward unblock ends the jobs of the head
// that wait for it (a leased job, or a pending one that does not wait on GitHub) as unavailable for no-executor, and lifts them with the jobs that were unavailable
// already. While the runner responds, a run that waits for it is refused with runner-responding. The store ends a job only if it is still as the command read it.

// silentRunner makes the runner of the world silent when the commands job runs: no executor was seen for a minute.
func (x *cjExecWorld) silentRunner() {
	x.job.liveness = execution.NewLiveness(x.job.now().Add(-time.Minute))
}

// requireEnded requires the job to be unavailable for no-executor, ended at the time of the command, with no results row.
func (x *cjExecWorld) requireEnded(job execution.Job, entry commands.Entry) {
	x.t.Helper()
	got, found, err := x.store.Job(x.t.Context(), job.ID)
	if err != nil || !found {
		x.t.Fatalf("job %d: found %v, error %v", job.ID, found, err)
	}
	if got.State != execution.StateUnavailable || got.Reason != execution.ReasonNoExecutor || !got.FinishedAt.Equal(execution.Stored(entry.DecidedAt)) || got.Failures != 0 {
		x.t.Fatalf("job %d is %s/%s finished at %v with %d failures; want unavailable/no-executor at %v, none counted", job.ID, got.State, got.Reason, got.FinishedAt, got.Failures, entry.DecidedAt)
	}
	if _, stored, err := x.store.Result(x.t.Context(), job.ID); err != nil || stored {
		x.t.Fatalf("job %d has a results row (stored %v, error %v); want none", job.ID, stored, err)
	}
}

// requireUntouched requires the job to be as it was.
func (x *cjExecWorld) requireUntouched(job execution.Job) {
	x.t.Helper()
	got, _, err := x.store.Job(x.t.Context(), job.ID)
	if err != nil || got.State != job.State || got.LeaseID != job.LeaseID || !got.FinishedAt.Equal(job.FinishedAt) {
		x.t.Fatalf("job %d is %+v (error %v); want it as it was: %+v", job.ID, got, err, job)
	}
}

func TestUnblockEndsTheJobsThatWaitForARunnerThatDoesNotRespond(t *testing.T) {
	x := newCjExecWorld(t, "required", "required", true)
	canonical, receipt := x.pending(execution.PlanCanonical, cjHead), x.pending(execution.PlanReceipt, cjHead)
	x.silentRunner()
	x.sync(x.sayWithCode(500, 10, unblockCommand, 0))

	x.mustRun()

	entry := x.requireEntry(500, commands.Unblocked, map[string]any{"plans": []string{"canonical", "receipt"}, "ended": []string{"canonical", "receipt"}})
	x.requireEnded(canonical, entry)
	x.requireEnded(receipt, entry)
	x.requireStepsConsumed(500)
	x.requireUnblocks(500, canonical, receipt)
	for _, job := range []execution.Job{canonical, receipt} {
		if unblocked, err := x.store.Unblocked(t.Context(), job.ID, cjOrigin); err != nil || !unblocked {
			t.Errorf("Unblocked(%d) = %v, %v; want true", job.ID, unblocked, err)
		}
	}
	x.requireNothingLeaked(cjCode(cjAt(10), 0))
}

func TestUnblockEndsALeasedJobAndRefusesItsLateResultAndHeartbeat(t *testing.T) {
	x := newCjExecWorld(t, "required", "off", true)
	pending := x.pending(execution.PlanCanonical, cjHead)
	leased := x.lease() // the executor took the job and was lost: it is leased, and the service has not swept it
	if leased.ID != pending.ID || leased.State != execution.StateLeased {
		t.Fatalf("leased %+v", leased)
	}
	x.silentRunner()
	x.sync(x.sayWithCode(500, 10, unblockCommand, 0))

	x.mustRun()

	entry := x.requireEntry(500, commands.Unblocked, map[string]any{"plans": []string{"canonical"}, "ended": []string{"canonical"}})
	x.requireEnded(leased, entry)
	late := execution.Result{LeaseID: leased.LeaseID, Report: execution.Report{Status: execution.StateUnavailable, Reason: execution.ReasonExecutorError, ExecutorVersion: "0.4.0",
		ExecutorSHA256: leased.Spec.GoverningDigest, StartedAt: cjOrigin, EndedAt: cjOrigin.Add(time.Second), BodyDigest: leased.Spec.DeclarationDigest}}
	if _, err := x.store.Complete(t.Context(), leased.ID, late, x.job.now(), 10*time.Minute); !errors.Is(err, execution.ErrConflict) {
		t.Errorf("the late result of the lease that was ended = %v; want a conflict", err)
	}
	if _, err := x.store.Heartbeat(t.Context(), leased.ID, leased.LeaseID, x.job.now()); !errors.Is(err, execution.ErrLeaseLost) {
		t.Errorf("the late heartbeat of the lease that was ended = %v; want a lost lease", err)
	}
}

func TestUnblockLiftsWhatIsUnavailableAndEndsWhatWaitsWhenTheRunnerDoesNotRespond(t *testing.T) {
	x := newCjExecWorld(t, "required", "required", true)
	canonical := x.unavailable(execution.PlanCanonical, cjHead, execution.ReasonNotConfigured)
	receipt := x.pending(execution.PlanReceipt, cjHead)
	x.silentRunner()
	x.sync(x.sayWithCode(500, 10, unblockCommand, 0))

	x.mustRun()

	entry := x.requireEntry(500, commands.Unblocked, map[string]any{"plans": []string{"canonical", "receipt"}, "ended": []string{"receipt"}})
	x.requireUntouched(canonical) // it was unavailable already: lifted as it is
	x.requireEnded(receipt, entry)
	x.requireUnblocks(500, canonical, receipt)
}

// A run that waits for a runner that responds is no reason to end it: the refusal is runner-responding, the code stays consumed, and the job runs as before.
func TestUnblockIsRefusedWhileTheRunnerRespondsAndAJobWaitsForIt(t *testing.T) {
	for name, jobs := range map[string]func(x *cjExecWorld) []execution.Job{
		"a pending job": func(x *cjExecWorld) []execution.Job {
			return []execution.Job{x.pending(execution.PlanCanonical, cjHead)}
		},
		"a leased job": func(x *cjExecWorld) []execution.Job {
			x.pending(execution.PlanCanonical, cjHead)
			return []execution.Job{x.lease()}
		},
		"a pending job and a complete one": func(x *cjExecWorld) []execution.Job {
			done := x.receiptJob(nil, 0, nil) // first: a lease takes the oldest pending job
			return []execution.Job{done, x.pending(execution.PlanCanonical, cjHead)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := newCjExecWorld(t, "required", "required", true)
			held := jobs(x)
			x.sync(x.sayWithCode(500, 10, unblockCommand, 0))

			x.mustRun()

			x.requireEntry(500, commands.UnblockRefused, map[string]any{"reason": "runner-responding"})
			x.requireStepsConsumed(500)
			x.requireUnblocks(500)
			for _, job := range held {
				x.requireUntouched(job)
			}
			if _, found, err := x.ledger.LatestOwnerCommand(t.Context(), cjID2, cjPull, cjOwnerID, 0); err != nil || found {
				t.Errorf("LatestOwnerCommand found %v, error %v; a refused unblock is no command for the check", found, err)
			}
		})
	}
}

// A pending job that waits on GitHub waits for GitHub, which has its own bound, and not for the runner: there is nothing to end.
func TestUnblockDoesNotEndAJobThatWaitsOnGitHub(t *testing.T) {
	x := newCjExecWorld(t, "required", "off", true)
	job := x.pending(execution.PlanCanonical, cjHead)
	if _, err := x.store.GitHubFailure(t.Context(), job.ID, x.job.now(), 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	waiting, _, _ := x.store.Job(t.Context(), job.ID)
	x.silentRunner()
	x.sync(x.sayWithCode(500, 10, unblockCommand, 0))

	x.mustRun()

	x.requireEntry(500, commands.UnblockRefused, map[string]any{"reason": "not-unavailable"})
	x.requireUntouched(waiting)
	x.requireUnblocks(500)
}

// racingStore leases the first job and brings the runner back just before the first unblock reaches the store: the runner that came back took the job that the
// command had read as pending.
type racingStore struct {
	execution.Store
	before func()
}

func (s *racingStore) RecordUnblock(ctx context.Context, e commands.Entry, ack *governance.OutboxMessage, head string, jobs []execution.UnblockJob, at time.Time) error {
	if s.before != nil {
		run := s.before
		s.before = nil
		run()
	}
	return s.Store.RecordUnblock(ctx, e, ack, head, jobs, at)
}

// The race: the command reads the silence and the pending job, a runner that came back leases the job, and the store refuses the unblock with ErrJobChanged. The run
// fails as a ledger failure, so the command is tried again; the code is verified again (it is consumed already, by the same comment) and the retry reads the runner
// that responds: runner-responding. No job of a live runner is ever ended.
func TestUnblockNeverEndsAJobThatARunnerThatCameBackTookAndTheRetryIsRefused(t *testing.T) {
	x := newCjExecWorld(t, "required", "off", true)
	pending := x.pending(execution.PlanCanonical, cjHead)
	x.silentRunner()
	x.job.executions = &racingStore{Store: x.store, before: func() {
		x.lease()
		x.job.liveness.Saw(x.job.now())
	}}
	x.sync(x.sayWithCode(500, 10, unblockCommand, 0))

	if _, err := x.job.run(t.Context()); !errors.Is(err, execution.ErrJobChanged) {
		t.Fatalf("the first run = %v; want it to fail with the job that changed", err)
	}
	x.requireNoEntry(cjID2, 500)
	x.requireUnblocks(500)
	if got, _, _ := x.store.Job(t.Context(), pending.ID); got.State != execution.StateLeased {
		t.Fatalf("the job is %s; want it leased by the runner that came back", got.State)
	}

	x.mustRun() // the retry

	x.requireEntry(500, commands.UnblockRefused, map[string]any{"reason": "runner-responding"})
	x.requireUnblocks(500)
	if got, _, _ := x.store.Job(t.Context(), pending.ID); got.State != execution.StateLeased {
		t.Fatalf("the job is %s after the retry; want it still leased", got.State)
	}
	for _, source := range x.owners.consumed {
		if source != commentSource(500) {
			t.Errorf("a step was consumed for %q; want only the unblock's own", source)
		}
	}
}
