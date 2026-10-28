package app

import (
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

// The hold (docs/contracts/runtime.md, "The early unblock and its hold"): the recovery rule does not replace a no-executor job that has an unblock row,
// early or late, so the runner that comes back runs nothing for the head and the next push runs as usual.

// noExecutorWorld is pull 5 whose jobs ended for no-executor after the wait, with no executor ever seen.
func noExecutorWorld(t *testing.T) (w *cjJobWorld, canonical, receipt execution.Job) {
	t.Helper()
	w = newExecWorld(t, execDecl)
	w.tree(cjHead2, execHeadFiles(execDecl, "v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.mustCheck()
	w.clock = w.clock.Add(10 * time.Minute)
	w.mustCheck() // no executor for the wait: the pending jobs are final
	canonical, receipt = w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	requireFinal(t, canonical, execution.ReasonNoExecutor)
	requireFinal(t, receipt, execution.ReasonNoExecutor)
	return w, canonical, receipt
}

func TestChecksDoNotReplaceANoExecutorJobUnblockedAfterTheWaitWhenTheRunnerComesBack(t *testing.T) {
	w, canonical, receipt := noExecutorWorld(t)
	w.unblock(5, 500, cjHead2, canonical, receipt)

	w.check.liveness.Saw(canonical.FinishedAt.Add(time.Hour)) // a real executor request, later than the end of both jobs
	w.mustCheck()

	if got := w.headJobs(5, cjHead2, execution.PlanCanonical); len(got) != 1 {
		t.Errorf("%d canonical jobs after the runner came back, want the unblocked one alone", len(got))
	}
	if got := w.headJobs(5, cjHead2, execution.PlanReceipt); len(got) != 1 {
		t.Errorf("%d receipt jobs after the runner came back, want the unblocked one alone", len(got))
	}
}

func TestChecksDoNotReplaceAJobTheEarlyUnblockEndedWhenTheRunnerComesBack(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.tree(cjHead2, execHeadFiles(execDecl, "v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.mustCheck() // the jobs of pull 5 are pending, and no executor is seen
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	w.unblock(5, 500, cjHead2, canonical, receipt) // the owner's early unblock ends both for no-executor
	requireFinal(t, w.jobByID(canonical.ID), execution.ReasonNoExecutor)
	requireFinal(t, w.jobByID(receipt.ID), execution.ReasonNoExecutor)

	w.execRun() // the runner is back: an executor request later than the end of both jobs

	if got := w.headJobs(5, cjHead2, execution.PlanCanonical); len(got) != 1 {
		t.Errorf("%d canonical jobs after the runner came back, want the unblocked one alone", len(got))
	}
	if got := w.headJobs(5, cjHead2, execution.PlanReceipt); len(got) != 1 {
		t.Errorf("%d receipt jobs after the runner came back, want the unblocked one alone", len(got))
	}
}

func TestChecksRunAnotherHeadAfterAnUnblockedNoExecutorJobHeldItsBinding(t *testing.T) {
	w, canonical, receipt := noExecutorWorld(t)
	w.unblock(5, 500, cjHead2, canonical, receipt)
	w.tree(cjHead3, execHeadFiles(execDecl, "v3"))
	w.sync(map[int]string{4: bootHead, 5: cjHead3}) // a push

	w.execRun()

	if got := w.onlyJob(5, cjHead3, execution.PlanCanonical); got.State != execution.StatePending {
		t.Errorf("the canonical job of the next push is %s, want pending: it runs as usual", got.State)
	}
}

// A job that ended for executor-not-configured is replaced once a token is configured, whatever the owner did: fixing the configuration is what the owner wanted.
func TestChecksStillReplaceAnUnblockedExecutorNotConfiguredJobOnceATokenIsConfigured(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.check.executorConfigured = false
	w.execPull(execHeadFiles(execDecl, "v2"))
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	w.unblock(5, 500, cjHead2, canonical, receipt)

	w.check.executorConfigured = true
	w.execRun()

	if got := w.headJobs(5, cjHead2, execution.PlanCanonical); len(got) != 2 || got[1].State != execution.StatePending {
		t.Errorf("canonical jobs %+v, want a fresh pending job next to the unblocked one", got)
	}
	if got := w.headJobs(5, cjHead2, execution.PlanReceipt); len(got) != 2 || got[1].State != execution.StatePending {
		t.Errorf("receipt jobs %+v, want a fresh pending job next to the unblocked one", got)
	}
}
