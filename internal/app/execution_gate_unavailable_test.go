package app

import (
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

// The checks job tells the Contract check why the canonical run did not run (M2.1c, SW-28, part 3; docs/contracts/runtime.md, "The canonical gate"): the payload carries the reason of the
// job for a head that the gate holds as canonical-unavailable, so that the check can say whether the approved tests run on their own, and carries nothing for any other check.
func TestChecksCarryTheReasonOfAnUnavailableCanonicalRunInTheCheck(t *testing.T) {
	t.Run("the test runner did not answer", func(t *testing.T) {
		w := gateWorld(t)
		w.execRun()
		w.clock = w.clock.Add(10 * time.Minute) // no executor is seen for the wait: the pending job is unavailable (no-executor)
		w.mustCheck()

		requireFinal(t, w.onlyJob(4, bootHead, execution.PlanCanonical), execution.ReasonNoExecutor)
		w.want(4, checks.InProgress, "canonical-unavailable", 2)
		wantUnavailable(t, w.lastCheckPayload(4), "no-executor")
	})
	t.Run("no test runner is set up", func(t *testing.T) {
		w := gateWorld(t)
		w.check.executorConfigured = false
		w.execRun()

		requireFinal(t, w.onlyJob(4, bootHead, execution.PlanCanonical), execution.ReasonNotConfigured)
		w.want(4, checks.InProgress, "canonical-unavailable", 1)
		wantUnavailable(t, w.lastCheckPayload(4), "executor-not-configured")
	})
	t.Run("another reason", func(t *testing.T) {
		w := gateWorld(t)
		w.execRun()
		w.finish(w.onlyJob(4, bootHead, execution.PlanCanonical), nil, func(r *execution.Result) {
			r.Report.Status, r.Report.Reason = execution.StateUnavailable, execution.ReasonToolchainMismatch
		})
		w.execRun()

		requireFinal(t, w.onlyJob(4, bootHead, execution.PlanCanonical), execution.ReasonToolchainMismatch)
		w.want(4, checks.InProgress, "canonical-unavailable", 2)
		wantUnavailable(t, w.lastCheckPayload(4), "toolchain-mismatch")
	})
	t.Run("a run that waits has none", func(t *testing.T) {
		w := gateWorld(t)
		w.execRun()

		w.want(4, checks.InProgress, "canonical-pending", 1)
		wantUnavailable(t, w.lastCheckPayload(4), "")
	})
	t.Run("a run that the owner unblocked has none", func(t *testing.T) {
		w := gateWorld(t)
		w.check.executorConfigured = false
		w.execRun()
		w.unblock(4, 301, bootHead, w.onlyJob(4, bootHead, execution.PlanCanonical))
		w.execRun()

		w.want(4, checks.Success, "unchanged", 2)
		wantUnavailable(t, w.lastCheckPayload(4), "")
	})
}

// wantUnavailable requires the `unavailable` reason of a github_check payload, absent for an empty one.
func wantUnavailable(t *testing.T, payload map[string]any, want string) {
	t.Helper()
	got, present := payload["unavailable"]
	if want == "" && present || want != "" && got != want {
		t.Errorf("payload %v, want the unavailable reason %q", payload, want)
	}
}
