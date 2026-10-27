package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// A wait that keeps the stored check of a head sends it again when the lost-publication rule advances its generation (a quarter of an hour after its message failed), and
// the builder refuses a canonical-failing check without the counts of its run (commandtext.checkRequired), which failed the step of every run until the wait ended. A
// required success lost its counts the same way. The restore reads them again from the job the gate reads.

// testsFailOnce is an execution store whose first read of the tests of a job fails, as a store that is down for a moment.
type testsFailOnce struct {
	execution.Store
	failed bool
}

func (s *testsFailOnce) Tests(ctx context.Context, jobID int64) ([]execution.Test, error) {
	if !s.failed {
		s.failed = true
		return nil, errors.New("the store is down")
	}
	return s.Store.Tests(ctx, jobID)
}

// completedGate is the gate world after the canonical run of pull 4 completed with the tests and the check said what the gate decided.
func completedGate(t *testing.T, tests ...string) *cjJobWorld {
	t.Helper()
	w := gateWorld(t)
	w.execRun()
	w.finish(w.onlyJob(4, bootHead, execution.PlanCanonical), canonicalTests(tests...))
	w.execRun()
	return w
}

// loseCheck marks the message of the current check of pull as failed, and lets the cooldown of the lost-publication rule pass. It returns the record whose message is lost.
func (w *cjJobWorld) loseCheck(pull int) checks.Record {
	w.t.Helper()
	record, _ := w.record(pull)
	if err := w.memory.FailMessage(w.t.Context(), fmt.Sprintf("check:%d:%d:%d", bootRepoID, pull, record.Generation)); err != nil {
		w.t.Fatal(err)
	}
	w.clock = w.clock.Add(checks.RepublishCooldown)
	return record
}

// restorePath is a way for the run to restore the stored check of pull 4 instead of deciding it.
type restorePath struct {
	name    string
	failure bool // the run fails: the policy or the step cannot be read, as against a wait for GitHub
	arm     func(w *cjJobWorld)
}

var restorePaths = []restorePath{
	{"the policy cannot be read", true, func(w *cjJobWorld) { w.check.contents = unreadableContents{} }},
	{"the step of execution fails", true, func(w *cjJobWorld) { w.check.executions = &testsFailOnce{Store: w.jobs} }},
	{"the tree of the head cannot be read", false, func(w *cjJobWorld) {
		w.tree(cjHead2, execHeadFiles(gateDecl, "v2"))
		w.sync(map[int]string{4: bootHead, 5: cjHead2})
		w.execRun()             // a protected pull request writes a proposal: the Suite moved, and pull 4 is decided again by the next run
		w.check.unchanged = nil // as after a restart: the head is not remembered as unchanged, so its tree is read
		w.reader.repos[bootRepo].treeErr[bootHead] = errors.New("github: 502")
	}},
}

// runRestored runs the checks job once and requires the failure that the path says, and that the check was built: a builder that refuses the restored check fails
// the pull request's step on top of it.
func (w *cjJobWorld) runRestored(failure bool) {
	w.t.Helper()
	w.check.liveness.Saw(w.clock.Add(time.Minute))
	err := w.runChecks()
	if (err != nil) != failure {
		w.t.Fatalf("the run returned %v, want a failure: %v\nlogs: %s", err, failure, w.logs)
	}
	if err != nil && strings.Contains(err.Error(), "publish the check") {
		w.t.Fatalf("the restored check could not be built: %v", err)
	}
}

func TestChecksSendAgainTheCountsOfACanonicalFailingCheckTheyRestore(t *testing.T) {
	for _, path := range restorePaths {
		t.Run(path.name, func(t *testing.T) {
			w := completedGate(t, "pass", "skipped", "fail", "excluded")
			w.want(4, checks.Failure, "canonical-failing", 2)
			path.arm(w)
			lost := w.loseCheck(4)

			w.runRestored(path.failure)

			w.want(4, checks.Failure, "canonical-failing", lost.Generation+1) // the lost message went out again
			wantExecution(t, w.lastCheckPayload(4), 3, 1, 1, 1, 0, 0, 1)
		})
	}
}

// Only a policy that was read says that the check carries counts, so a success that is restored while the policy cannot be read goes out as it was stored.
func TestChecksSendAgainTheCountsOfARequiredSuccessTheyRestore(t *testing.T) {
	for _, path := range restorePaths[1:] {
		t.Run(path.name, func(t *testing.T) {
			w := completedGate(t, "pass", "skipped", "excluded")
			w.want(4, checks.Success, "unchanged", 2)
			path.arm(w)
			lost := w.loseCheck(4)

			w.runRestored(path.failure)

			w.want(4, checks.Success, "unchanged", lost.Generation+1)
			wantExecution(t, w.lastCheckPayload(4), 2, 1, 1, 0, 0, 0, 1)
		})
	}
}

// A canonical-failing check whose counts cannot be read goes out as the check of a head with no decision, so that the builder never refuses it and the head always has a check.
func TestChecksSayAssessmentPendingForACanonicalFailingCheckWhoseCountsCannotBeRead(t *testing.T) {
	w := completedGate(t, "pass", "fail")
	w.check.executions = headJobsFail{w.jobs} // the step fails, and so does the read of the counts
	lost := w.loseCheck(4)

	w.runRestored(true)

	w.want(4, checks.InProgress, "assessment-pending", lost.Generation+1)
	if _, found := w.lastCheckPayload(4)["execution"]; found {
		t.Error("an assessment-pending check carries counts")
	}
}

// A required success that is restored while the policy cannot be read goes out as it was stored: the policy cannot say that the check carries counts, so it has none, and the
// lost message is sent again under the next generation all the same.
func TestChecksSendAgainARequiredSuccessTheyRestoreWhileThePolicyCannotBeRead(t *testing.T) {
	w := completedGate(t, "pass", "skipped", "excluded")
	w.want(4, checks.Success, "unchanged", 2)
	restorePaths[0].arm(w)
	lost := w.loseCheck(4)

	w.runRestored(restorePaths[0].failure)

	w.want(4, checks.Success, "unchanged", lost.Generation+1)
	if _, found := w.lastCheckPayload(4)["execution"]; found {
		t.Errorf("payload %v carries counts that a policy that was not read cannot vouch for", w.lastCheckPayload(4))
	}
}

// A run that is stopping while the counts are read does not restore a canonical-failing check as undecided: the read fails because of the stop, which says nothing of the
// counts, so the check keeps its reason and the stop is what the job returns.
func TestChecksKeepACanonicalFailingCheckWhenTheRunIsStoppedWhileItsCountsAreRead(t *testing.T) {
	w := completedGate(t, "pass", "fail")
	record, _ := w.record(4)
	w.check.executions = headJobsFail{w.jobs}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s := &pullState{p: owner.ProjectPolicy{RepositoryID: bootRepoID}, pull: githubsync.PullState{Number: 4, HeadSHA: bootHead}}
	x := &executionRun{known: true, policy: executionPolicy{canonical: domain.CanonicalLevelRequired}}
	d := pullDecision{wait: true, result: record.Result}
	var executed executionOutcome

	err := w.check.restoreCounts(ctx, s, x, &d, &executed)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("restoreCounts = %v, want the stop of the run", err)
	}
	if d.result.Reason != reasonCanonicalFailing {
		t.Errorf("the check reads %s/%s, want canonical-failing kept", d.result.State, d.result.Reason)
	}
}
