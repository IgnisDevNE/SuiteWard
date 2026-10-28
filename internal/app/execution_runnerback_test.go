package app

import (
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// The runner is back (docs/contracts/runtime.md, "The early unblock and its hold", "The runner is back"): the current head has a job with an unblock row, the reason
// no-executor and an end earlier than the last real executor request. The comment of that state is the work of the comments' task; this is the fact it reads.

// pull5 is the state of pull request 5 at cjHead2, as the checks job reads it.
func (w *cjJobWorld) pull5(t *testing.T) *pullState {
	t.Helper()
	return &pullState{enrollment: owner.State{EnrolledAt: bootTime}, p: owner.ProjectPolicy{RepositoryID: bootRepoID}, pull: githubsync.PullState{Number: 5, HeadSHA: cjHead2}}
}

func (w *cjJobWorld) wantRunnerBack(t *testing.T, want bool, why string) {
	t.Helper()
	got, err := w.check.runnerBack(t.Context(), w.pull5(t))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("runnerBack = %v, want %v: %s", got, want, why)
	}
}

func TestTheRunnerIsBackWhenAnUnblockedNoExecutorJobEndedBeforeTheLastRealRequest(t *testing.T) {
	w, canonical, receipt := noExecutorWorld(t)
	w.wantRunnerBack(t, false, "the job is not unblocked")
	w.unblock(5, 500, cjHead2, canonical, receipt)
	w.wantRunnerBack(t, false, "no executor was seen since the job ended: the startup clock is none")

	w.check.liveness.Saw(canonical.FinishedAt)
	w.wantRunnerBack(t, false, "a request at the very time the job ended is not later")

	w.check.liveness.Saw(canonical.FinishedAt.Add(time.Second))
	w.wantRunnerBack(t, true, "the runner answered after the unblocked job ended")
}

func TestTheRunnerIsBackAfterAnEarlyUnblockToo(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.tree(cjHead2, execHeadFiles(execDecl, "v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.mustCheck()
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	w.unblock(5, 500, cjHead2, canonical, receipt) // ends both for no-executor, at the time of the command
	w.wantRunnerBack(t, false, "the runner has not been heard since")

	w.execRun() // the runner is back: a request later than the end of both jobs

	w.wantRunnerBack(t, true, "the runner answered after the unblock ended the jobs")
}

func TestTheRunnerIsNotBackForAJobThatEndedForAnotherReasonOrBelongsToAnotherHead(t *testing.T) {
	t.Run("a job created without an executor token", func(t *testing.T) {
		w := newExecWorld(t, execDecl)
		w.check.executorConfigured = false
		w.execPull(execHeadFiles(execDecl, "v2"))
		w.unblock(5, 500, cjHead2, w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt))
		w.check.liveness.Saw(w.clock.Add(time.Hour))
		w.wantRunnerBack(t, false, "the jobs ended for executor-not-configured, and a token is what fixes that")
	})
	t.Run("a job of another head", func(t *testing.T) {
		w, canonical, receipt := noExecutorWorld(t)
		w.unblock(5, 500, cjHead2, canonical, receipt)
		w.check.liveness.Saw(canonical.FinishedAt.Add(time.Hour))
		other := w.pull5(t)
		other.pull.HeadSHA = cjHead3 // the head moved
		got, err := w.check.runnerBack(t.Context(), other)
		if err != nil || got {
			t.Errorf("runnerBack of a head that has no job = %v, %v; want false", got, err)
		}
	})
}
