package app

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The checks job writes the comments of a head (docs/contracts/runtime.md, "The comments of a head"): the results comment once for each final set of runs, the
// not-responding comment once for each set of waiting jobs and the runner-back comment once for an unblocked head. It reads the policy from the canonical declaration,
// so a repository with no profile gets none.

func resultsKeyOf(pull int, head string, canonical, receipt int64) string {
	return fmt.Sprintf("results:%d:%d:%s:%d-%d", bootRepoID, pull, head, canonical, receipt)
}

func notRespondingKeyOf(pull int, head string, canonical, receipt int64) string {
	return fmt.Sprintf("not-responding:%d:%d:%s:%d-%d", bootRepoID, pull, head, canonical, receipt)
}

func runnerBackKeyOf(pull int, head string) string {
	return fmt.Sprintf("runner-back:%d:%d:%s", bootRepoID, pull, head)
}

// wantComments requires the comments of a kind that the job wrote, by key, oldest first.
func (w *cjJobWorld) wantComments(prefix string, keys ...string) {
	w.t.Helper()
	if got := w.scopeKeys(prefix); !slices.Equal(got, keys) {
		w.t.Fatalf("the %s comments are %v, want %v\nlogs: %s", prefix, got, keys, w.logs)
	}
}

// commentPayload is the payload of the comment written under key.
func (w *cjJobWorld) commentPayload(key string) map[string]any {
	w.t.Helper()
	for _, m := range w.suites.messages {
		if m.Key == key {
			return payloadOf(w.t, m)
		}
	}
	w.t.Fatalf("no comment %s among the %d messages", key, len(w.suites.messages))
	return nil
}

func wantPayload(t *testing.T, payload map[string]any, want map[string]any) {
	t.Helper()
	for field, value := range want {
		if got, found := payload[field]; !found || fmt.Sprint(got) != fmt.Sprint(value) {
			t.Errorf("payload field %s is %v (present %v), want %v\npayload: %v", field, got, found, value, payload)
		}
	}
}

// wantRunCounts requires the counts of a run of a results payload.
func wantRunCounts(t *testing.T, run map[string]any, want map[string]any) {
	t.Helper()
	counts, ok := run["counts"].(map[string]any)
	if !ok {
		t.Fatalf("the run %v has no counts", run)
	}
	if len(counts) != len(want) {
		t.Errorf("counts %v, want exactly the keys of %v", counts, want)
	}
	wantPayload(t, counts, want)
}

func wantAbsent(t *testing.T, payload map[string]any, fields ...string) {
	t.Helper()
	for _, field := range fields {
		if got, found := payload[field]; found {
			t.Errorf("payload field %s is %v, want none\npayload: %v", field, got, payload)
		}
	}
}

func TestChecksWriteOneResultsCommentWhenTheRunsOfAHeadAreFinalAndNoneWhileOneRuns(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	job := w.onlyJob(4, bootHead, execution.PlanCanonical)
	w.wantComments("results:")

	w.finish(job, canonicalTests("pass", "fail"))
	w.execRun()

	key := resultsKeyOf(4, bootHead, job.ID, 0)
	w.wantComments("results:", key)
	payload := w.commentPayload(key)
	wantPayload(t, payload, map[string]any{"repositoryId": bootRepoID, "repo": bootRepo, "pull": 4, "head": bootHead, "tier": "isolated", "canonicalJob": job.ID, "receiptJob": 0,
		"reference": "", "canonicalLevel": "required", "receiptLevel": "off", "weakened": 0, "next": "fix"})
	wantPayload(t, payload["canonical"].(map[string]any), map[string]any{"status": "complete"})
	wantRunCounts(t, payload["canonical"].(map[string]any), map[string]any{"tests": 2, "passed": 1, "skipped": 0, "failed": 1, "missing": 0, "flaky": 0, "excluded": 0})
	wantAbsent(t, payload, "receipt", "unblocked", "ended", "approveMode")

	w.execRun() // a pass that finds the same set writes nothing
	w.execRun()
	w.wantComments("results:", key)
}

// A restart forgets what the job wrote, so it tries the key again: the outbox refuses it, and nothing is written twice.
func TestChecksWriteNoResultsCommentTwiceAfterARestart(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	w.finish(w.onlyJob(4, bootHead, execution.PlanCanonical), canonicalTests("pass"))
	w.execRun()
	if len(w.scopeKeys("results:")) != 1 {
		t.Fatalf("results comments %v, want one", w.scopeKeys("results:"))
	}

	w.check.announced = nil
	w.execRun()

	if got := w.scopeKeys("results:"); len(got) != 1 {
		t.Errorf("results comments after a restart %v, want the one", got)
	}
}

func TestChecksWriteAnotherResultsCommentForAFreshJobAfterTheRunnerReturned(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	first := w.onlyJob(4, bootHead, execution.PlanCanonical)

	w.clock = w.clock.Add(10 * time.Minute) // no executor for the wait: the job is unavailable
	w.mustCheck()
	w.wantComments("results:", resultsKeyOf(4, bootHead, first.ID, 0))
	unavailable := w.commentPayload(resultsKeyOf(4, bootHead, first.ID, 0))
	wantPayload(t, unavailable["canonical"].(map[string]any), map[string]any{"status": "unavailable", "reason": "no-executor"})
	wantPayload(t, unavailable, map[string]any{"next": "unblock"})
	wantAbsent(t, unavailable["canonical"].(map[string]any), "counts")

	w.runnerAnswers() // the recovery rule replaces the job with a fresh one
	w.mustCheck()
	jobs := w.headJobs(4, bootHead, execution.PlanCanonical)
	if len(jobs) != 2 {
		t.Fatalf("%d canonical jobs after the runner returned, want a fresh one", len(jobs))
	}
	w.wantComments("results:", resultsKeyOf(4, bootHead, first.ID, 0)) // the fresh job runs: nothing is final

	w.finish(jobs[1], canonicalTests("pass"))
	w.execRun()

	w.wantComments("results:", resultsKeyOf(4, bootHead, first.ID, 0), resultsKeyOf(4, bootHead, jobs[1].ID, 0))
}

func TestChecksWriteNoResultsCommentWhereNoRunIsWanted(t *testing.T) {
	for name, decl := range map[string]string{
		"both levels off": execDeclaration("off", "off"),
		"the receipt alone, where it does not apply": execDeclaration("off", "advisory"),
	} {
		t.Run(name, func(t *testing.T) {
			w := newExecWorld(t, decl)
			w.sync(map[int]string{4: bootHead})
			w.execRun()
			w.execRun()

			w.wantComments("results:")
			w.wantComments("not-responding:")
		})
	}
	t.Run("an advisory canonical run is wanted", func(t *testing.T) {
		w := newExecWorld(t, execDeclaration("advisory", "off"))
		w.sync(map[int]string{4: bootHead})
		w.execRun()
		job := w.onlyJob(4, bootHead, execution.PlanCanonical)
		w.finish(job, canonicalTests("pass"))
		w.execRun()

		w.wantComments("results:", resultsKeyOf(4, bootHead, job.ID, 0))
		wantPayload(t, w.commentPayload(resultsKeyOf(4, bootHead, job.ID, 0)), map[string]any{"canonicalLevel": "advisory", "receiptLevel": "off", "next": "none"})
	})
	t.Run("no policy at all", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.sync(map[int]string{4: bootHead})
		w.execRun()
		w.wantComments("results:")
	})
}

func TestChecksWriteNoCommentForAForkOrAClosedPullRequest(t *testing.T) {
	t.Run("a fork", func(t *testing.T) {
		w := newExecWorld(t, execDecl)
		w.headRepo = map[int]string{5: "someone/widgets"}
		w.execPullFive(execHeadFiles(execDecl, "v2"))
		w.silentFor(45 * time.Second)
		w.mustCheck()
		for _, prefix := range []string{"results:", "not-responding:", "runner-back:"} {
			w.wantComments(prefix)
		}
	})
	t.Run("a pull request that closed before its runs ended", func(t *testing.T) {
		w := newExecWorld(t, execDecl)
		w.execPullFive(execHeadFiles(execDecl, "v2"))
		w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass"))
		w.sync(map[int]string{4: bootHead}) // pull 5 is closed
		w.execRun()
		for _, prefix := range []string{"results:", "not-responding:", "runner-back:"} {
			w.wantComments(prefix)
		}
	})
}

// A head whose approval is missing is final once the approved tests and the receipt have run, and its comment names the revision, the mode and the next step.
func TestChecksWriteTheResultsCommentOfAHeadWhoseApprovalIsMissing(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPullFive(execHeadFiles(execDecl, "v2"))
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	w.finish(canonical, canonicalTests("pass", "skipped"))
	w.execRun()
	w.wantComments("results:") // the receipt has not run

	w.finish(receipt, receiptTests("detects", "inert"))
	w.execRun()

	key := resultsKeyOf(5, cjHead2, canonical.ID, receipt.ID)
	w.wantComments("results:", key)
	payload := w.commentPayload(key)
	wantPayload(t, payload, map[string]any{"pull": 5, "reference": "P2-R1", "canonicalLevel": "required", "receiptLevel": "required", "weakened": 0, "next": "approve", "approveMode": true})
	wantPayload(t, payload["receipt"].(map[string]any), map[string]any{"status": "complete"})
	wantPayload(t, payload["receipt"].(map[string]any)["counts"].(map[string]any), map[string]any{"detects": 1, "inert": 1, "newApi": 0, "fails": 0, "missing": 0, "flaky": 0,
		"skipped": 0, "excluded": 0, "markers": 0, "unparsed": 0})
}

func TestChecksNameTheApproveWithoutAModeWhenTheReceiptIsNotRequired(t *testing.T) {
	decl := execDeclaration("required", "advisory")
	w := newExecWorld(t, decl)
	w.execPullFive(execHeadFiles(decl, "v2"))
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	w.finish(canonical, canonicalTests("pass"))
	w.finish(receipt, receiptTests("detects"))
	w.execRun()

	payload := w.commentPayload(resultsKeyOf(5, cjHead2, canonical.ID, receipt.ID))
	wantPayload(t, payload, map[string]any{"next": "approve"})
	wantAbsent(t, payload, "approveMode")
}

func TestChecksNameABlockedApproveWhenTheRequiredReceiptMeetsNoKindOfChange(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPullFive(execHeadFiles(execDecl, "v2"))
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	w.finish(canonical, canonicalTests("pass"))
	w.finish(receipt, receiptTests("skipped"))
	w.execRun()

	wantPayload(t, w.commentPayload(resultsKeyOf(5, cjHead2, canonical.ID, receipt.ID)), map[string]any{"next": "blocked"})
}

// An approved test that the change edits and that fails in its approved version is weakened: the comment counts it, and so does the summary of the check.
func TestChecksCountTheWeakenedTestsInTheResultsCommentAndInTheAwaitingCheck(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPullFive(execHeadFiles(execDecl, "v2"))
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	edited := execution.Test{Package: ".", Name: "TestC00", File: "a_test.go", Kind: execution.KindChanged, State: "detects", HeadRuns: []string{"pass"}, BaseRuns: []string{"fail"}}
	added := execution.Test{Package: ".", Name: "TestNew", File: "a_test.go", Kind: execution.KindAdded, State: "inert", HeadRuns: []string{"pass"}, BaseRuns: []string{"pass"}}
	w.finish(canonical, canonicalTests("fail", "pass")) // TestC00 fails in its approved version
	w.finish(receipt, []execution.Test{edited, added})
	w.execRun()

	payload := w.commentPayload(resultsKeyOf(5, cjHead2, canonical.ID, receipt.ID))
	wantPayload(t, payload, map[string]any{"weakened": 1})
	check := w.lastCheckPayload(5)
	wantPayload(t, check, map[string]any{"reason": "approval-missing", "weakened": 1})
	wantExecution(t, check, 2, 1, 0, 1, 0, 0, 0)
}

// A test that is added has no approved version, and a changed test that passes in its approved version is not weakened.
func TestChecksDoNotCountATestThatIsAddedOrThatPassesInItsApprovedVersion(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPullFive(execHeadFiles(execDecl, "v2"))
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	changedAndPassing := execution.Test{Package: ".", Name: "TestC01", File: "a_test.go", Kind: execution.KindChanged, State: "detects", HeadRuns: []string{"pass"}, BaseRuns: []string{"fail"}}
	addedSameName := execution.Test{Package: ".", Name: "TestC00", File: "b_test.go", Kind: execution.KindAdded, State: "inert", HeadRuns: []string{"pass"}, BaseRuns: []string{"pass"}}
	w.finish(canonical, canonicalTests("fail", "pass"))
	w.finish(receipt, []execution.Test{changedAndPassing, addedSameName})
	w.execRun()

	wantPayload(t, w.commentPayload(resultsKeyOf(5, cjHead2, canonical.ID, receipt.ID)), map[string]any{"weakened": 0})
	wantAbsent(t, w.lastCheckPayload(5), "weakened")
}

func TestChecksStateTheCountsOfAnAwaitingCheckOnceEveryWantedRunIsFinal(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPullFive(execHeadFiles(execDecl, "v2"))
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	w.want(5, checks.InProgress, "approval-missing", 1)

	w.finish(canonical, canonicalTests("pass"))
	w.execRun()
	w.want(5, checks.InProgress, "approval-missing", 1) // the receipt has not run: the counts wait
	wantAbsent(t, w.lastCheckPayload(5), "execution")

	w.finish(receipt, receiptTests("detects"))
	w.execRun()

	w.want(5, checks.InProgress, "approval-missing", 2)
	wantExecution(t, w.lastCheckPayload(5), 1, 1, 0, 0, 0, 0, 0)
	w.execRun()
	w.want(5, checks.InProgress, "approval-missing", 2) // the same job again is quiet
}

// A fresh receipt job that replaces an unavailable one does not rewrite the check: the counts that went out stay.
func TestChecksKeepTheCountsOfAnAwaitingCheckWhenAFreshReceiptJobReplacesAnUnavailableOne(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPullFive(execHeadFiles(execDecl, "v2"))
	canonical := w.onlyJob(5, cjHead2, execution.PlanCanonical)
	w.finish(canonical, canonicalTests("pass"))
	w.clock = w.clock.Add(10 * time.Minute) // no executor for the wait: the receipt job is unavailable
	w.mustCheck()
	w.want(5, checks.InProgress, "approval-missing", 2)
	wantExecution(t, w.lastCheckPayload(5), 1, 1, 0, 0, 0, 0, 0)

	w.runnerAnswers() // the recovery rule replaces the receipt job with a fresh one, which has not run
	w.mustCheck()

	if jobs := w.headJobs(5, cjHead2, execution.PlanReceipt); len(jobs) != 2 {
		t.Fatalf("%d receipt jobs, want the unavailable one and a fresh one", len(jobs))
	}
	w.want(5, checks.InProgress, "approval-missing", 2)
}

func TestChecksWriteOneResultsCommentForAnApprovedHeadWhoseEarlierAndLaterSetsAreClean(t *testing.T) {
	w := newExecWorld(t, gateDecl)
	w.execPullFive(execHeadFiles(gateDecl, "v2"))
	first := w.onlyJob(5, cjHead2, execution.PlanCanonical)
	w.finish(first, canonicalTests("pass"))
	w.execRun()
	w.wantComments("results:", resultsKeyOf(5, cjHead2, first.ID, 0))

	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.execRun()
	jobs := w.headJobs(5, cjHead2, execution.PlanCanonical)
	second := jobs[len(jobs)-1]
	w.finish(second, canonicalTests("pass"))
	w.execRun()

	w.wantComments("results:", resultsKeyOf(5, cjHead2, first.ID, 0)) // the set run under the approved version has nothing to add
}

func TestChecksWriteTheLaterResultsCommentOfAnApprovedHeadThatCorrectsAnEarlierOne(t *testing.T) {
	w := newExecWorld(t, gateDecl)
	w.execPullFive(execHeadFiles(gateDecl, "v2"))
	first := w.onlyJob(5, cjHead2, execution.PlanCanonical)
	w.finish(first, canonicalTests("pass", "fail"))
	w.execRun()

	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.execRun()
	jobs := w.headJobs(5, cjHead2, execution.PlanCanonical)
	second := jobs[len(jobs)-1]
	w.finish(second, canonicalTests("pass"))
	w.execRun()

	w.wantComments("results:", resultsKeyOf(5, cjHead2, first.ID, 0), resultsKeyOf(5, cjHead2, second.ID, 0))
	wantPayload(t, w.commentPayload(resultsKeyOf(5, cjHead2, first.ID, 0)), map[string]any{"next": "approve"})
	wantPayload(t, w.commentPayload(resultsKeyOf(5, cjHead2, second.ID, 0)), map[string]any{"next": "none"})
}

// A head approved while its first run was pending has no earlier set, and its later set is always written.
func TestChecksWriteTheResultsCommentOfAnApprovedHeadThatHasNoEarlierSet(t *testing.T) {
	w := newExecWorld(t, gateDecl)
	w.execPullFive(execHeadFiles(gateDecl, "v2"))
	first := w.onlyJob(5, cjHead2, execution.PlanCanonical)

	w.consent(contract.ApproveConsent, 201, 2, 1, 5) // the first job is cancelled while it is pending
	w.execRun()
	if got := w.jobByID(first.ID); got.State != execution.StateCancelled {
		t.Fatalf("the first job is %s, want cancelled", got.State)
	}
	jobs := w.headJobs(5, cjHead2, execution.PlanCanonical)
	second := jobs[len(jobs)-1]
	w.finish(second, canonicalTests("pass"))
	w.execRun()

	w.wantComments("results:", resultsKeyOf(5, cjHead2, second.ID, 0))
}

// The weakened count is the earlier set's: the canonical run of the later set is governed by the revision, so it has no counterpart in the receipt.
func TestChecksStateTheWeakenedCountInTheEarlierSetAndNoneInTheLaterOne(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPullFive(execHeadFiles(execDecl, "v2"))
	first, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	edited := execution.Test{Package: ".", Name: "TestC00", File: "a_test.go", Kind: execution.KindChanged, State: "detects", HeadRuns: []string{"pass"}, BaseRuns: []string{"fail"}}
	w.finish(first, canonicalTests("fail"))
	w.finish(receipt, []execution.Test{edited})
	w.execRun()
	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.execRun()
	jobs := w.headJobs(5, cjHead2, execution.PlanCanonical)
	second := jobs[len(jobs)-1]
	w.finish(second, canonicalTests("pass"))
	w.execRun()

	earlier, later := resultsKeyOf(5, cjHead2, first.ID, receipt.ID), resultsKeyOf(5, cjHead2, second.ID, receipt.ID)
	w.wantComments("results:", earlier, later)
	wantPayload(t, w.commentPayload(earlier), map[string]any{"weakened": 1})
	wantPayload(t, w.commentPayload(later), map[string]any{"weakened": 0})
}

func TestChecksWriteTheResultsCommentOfAnUnblockedHeadWithWhatTheCommandEnded(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.tree(cjHead2, execHeadFiles(execDecl, "v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.mustCheck()
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	w.unblock(5, 500, cjHead2, canonical, receipt) // the runner has not answered: the command ends both for no-executor
	w.mustCheck()

	key := resultsKeyOf(5, cjHead2, canonical.ID, receipt.ID)
	w.wantComments("results:", key)
	payload := w.commentPayload(key)
	wantPayload(t, payload, map[string]any{"unblocked": "[canonical receipt]", "ended": "[canonical receipt]", "next": "approve"})
	wantPayload(t, payload["canonical"].(map[string]any), map[string]any{"status": "unavailable", "reason": "no-executor"})
}

func TestChecksOfferTheUnblockInTheResultsCommentOfARunThatDidNotRunAndHoldsTheHead(t *testing.T) {
	w := gateWorld(t)
	w.check.executorConfigured = false // the job is created unavailable
	w.execRun()
	job := w.onlyJob(4, bootHead, execution.PlanCanonical)

	key := resultsKeyOf(4, bootHead, job.ID, 0)
	w.wantComments("results:", key)
	wantPayload(t, w.commentPayload(key), map[string]any{"next": "unblock"})
	wantAbsent(t, w.commentPayload(key), "unblocked", "ended")
}

func TestChecksWriteTheNotRespondingCommentOnceForEachSetOfWaitingJobs(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	job := w.onlyJob(4, bootHead, execution.PlanCanonical)
	key := notRespondingKeyOf(4, bootHead, job.ID, 0)

	w.silentFor(30*time.Second - time.Millisecond)
	w.mustCheck()
	w.wantComments("not-responding:")                    // a moment short of 30 s
	w.want(4, checks.InProgress, "canonical-pending", 1) // the check and the comment read the one silence of the pass

	w.silentFor(30 * time.Second)
	w.mustCheck()
	w.wantComments("not-responding:", key)
	wantPayload(t, w.commentPayload(key), map[string]any{"repositoryId": bootRepoID, "pull": 4, "head": bootHead, "tier": "isolated", "canonicalJob": job.ID, "receiptJob": 0, "unblock": true})
	w.want(4, checks.InProgress, "canonical-not-responding", 2)

	w.silentFor(40 * time.Second) // the same wait, a later pass
	w.mustCheck()
	w.runnerAnswers() // the runner answers and goes quiet again: the same jobs, so the same key (a flapping runner does not fill the pull request)
	w.mustCheck()
	w.silentFor(45 * time.Second)
	w.mustCheck()
	w.wantComments("not-responding:", key)
	w.wantComments("results:")
}

func TestChecksOfferNoUnblockInTheNotRespondingCommentWhereNoGateHolds(t *testing.T) {
	w := newExecWorld(t, execDeclaration("advisory", "off"))
	w.sync(map[int]string{4: bootHead})
	w.execRun()
	job := w.onlyJob(4, bootHead, execution.PlanCanonical)

	w.silentFor(45 * time.Second)
	w.mustCheck()

	w.wantComments("not-responding:", notRespondingKeyOf(4, bootHead, job.ID, 0))
	wantPayload(t, w.commentPayload(notRespondingKeyOf(4, bootHead, job.ID, 0)), map[string]any{"unblock": false})
}

// The wait that ends unavailable adds no second not-responding comment: its key is the same jobs. It adds the results comment, which says that the tests did not run.
func TestChecksAddTheResultsCommentAndNoSecondNotRespondingCommentWhenTheWaitEndsUnavailable(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	job := w.onlyJob(4, bootHead, execution.PlanCanonical)
	w.silentFor(45 * time.Second)
	w.mustCheck()
	w.wantComments("not-responding:", notRespondingKeyOf(4, bootHead, job.ID, 0))

	w.clock = w.clock.Add(10 * time.Minute)
	w.mustCheck()

	w.wantComments("not-responding:", notRespondingKeyOf(4, bootHead, job.ID, 0))
	w.wantComments("results:", resultsKeyOf(4, bootHead, job.ID, 0))
}

func TestChecksWriteTheNotRespondingCommentOfAHeadWhoseReceiptJobWaits(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPullFive(execHeadFiles(execDecl, "v2"))
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	w.finish(canonical, canonicalTests("pass")) // only the receipt job waits for the runner

	w.silentFor(45 * time.Second)
	w.mustCheck()

	key := notRespondingKeyOf(5, cjHead2, 0, receipt.ID)
	w.wantComments("not-responding:", key)
	wantPayload(t, w.commentPayload(key), map[string]any{"canonicalJob": 0, "receiptJob": receipt.ID, "unblock": true}) // required: the waiting receipt holds the approve
}

func TestChecksWriteTheRunnerBackCommentOnceForAnUnblockedHeadWhoseRunnerReturned(t *testing.T) {
	w, canonical, receipt := noExecutorWorld(t)
	w.unblock(5, 500, cjHead2, canonical, receipt)
	w.mustCheck()
	w.wantComments("runner-back:") // the runner has not been heard since

	w.check.liveness.Saw(canonical.FinishedAt.Add(time.Second))
	w.mustCheck()

	key := runnerBackKeyOf(5, cjHead2)
	w.wantComments("runner-back:", key)
	wantPayload(t, w.commentPayload(key), map[string]any{"repositoryId": bootRepoID, "pull": 5, "head": cjHead2, "tier": "isolated"})
	w.mustCheck()
	w.mustCheck()
	w.wantComments("runner-back:", key)
}

func TestChecksWriteNoRunnerBackCommentForAHeadThatMovedOrAClosedPullRequest(t *testing.T) {
	t.Run("a pull request that closed", func(t *testing.T) {
		w, canonical, receipt := noExecutorWorld(t)
		w.unblock(5, 500, cjHead2, canonical, receipt)
		w.sync(map[int]string{4: bootHead})
		w.check.liveness.Saw(canonical.FinishedAt.Add(time.Second))
		w.mustCheck()
		w.wantComments("runner-back:")
	})
	t.Run("a head that moved", func(t *testing.T) {
		w, canonical, receipt := noExecutorWorld(t)
		w.unblock(5, 500, cjHead2, canonical, receipt)
		w.tree(cjHead3, execHeadFiles(execDecl, "v3"))
		w.sync(map[int]string{4: bootHead, 5: cjHead3})
		w.check.liveness.Saw(canonical.FinishedAt.Add(time.Second))
		w.mustCheck()
		w.wantComments("runner-back:")
	})
	t.Run("a head that was not unblocked", func(t *testing.T) {
		w, canonical, _ := noExecutorWorld(t)
		w.check.liveness.Saw(canonical.FinishedAt.Add(time.Second))
		w.mustCheck()
		w.wantComments("runner-back:")
	})
}

// A head whose results were written for jobs of one pass keeps its other pull requests apart: each pull request has its own comments.
func TestChecksWriteTheCommentsOfEachOpenPullRequestOnItsOwn(t *testing.T) {
	w := gateWorld(t)
	w.tree(cjHead2, execHeadFiles(gateDecl, "v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.execRun()
	four, five := w.onlyJob(4, bootHead, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanCanonical)
	w.finish(four, canonicalTests("pass"))
	w.finish(five, canonicalTests("pass"))
	w.execRun()

	w.wantComments("results:", resultsKeyOf(4, bootHead, four.ID, 0), resultsKeyOf(5, cjHead2, five.ID, 0))
}

// The earlier set of an approved head is written when it is final, whether or not the later set is: the run that the approval asked for may take a while, or never end.
func TestChecksWriteTheEarlierResultsCommentOfAnApprovedHeadWhileItsLaterRunIsPending(t *testing.T) {
	w := newExecWorld(t, gateDecl)
	w.execPullFive(execHeadFiles(gateDecl, "v2"))
	first := w.onlyJob(5, cjHead2, execution.PlanCanonical)
	w.finish(first, canonicalTests("pass", "fail")) // complete, and no pass of the job has seen it
	w.consent(contract.ApproveConsent, 201, 2, 1, 5)

	w.execRun() // the head is approved: the run of the approved version is pending

	jobs := w.headJobs(5, cjHead2, execution.PlanCanonical)
	if len(jobs) != 2 || jobs[1].State != execution.StatePending {
		t.Fatalf("canonical jobs %+v, want the complete first one and a pending one under the revision", jobs)
	}
	w.wantComments("results:", resultsKeyOf(5, cjHead2, first.ID, 0))
}

// A complete run whose job has no governing manifest is a caller that did not fill it, and the weakened count says so: it never reads as 0.
func TestTheWeakenedCountOfASetRefusesAJobWithNoGoverningManifest(t *testing.T) {
	w := newExecWorld(t, execDecl)
	set := domain.Set{CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelRequired, ReceiptApplies: true, ReceiptDecided: true,
		CanonicalJob: domain.Job{ID: 1, State: domain.JobComplete}, ReceiptJob: domain.Job{ID: 2, State: domain.JobComplete}}

	if n, err := w.check.weakenedOf(t.Context(), set); !errors.Is(err, domain.ErrNoGoverningDigest) || n != 0 {
		t.Errorf("weakenedOf = %d, %v; want 0 and ErrNoGoverningDigest", n, err)
	}
}

// The Set of the comments has a read that no gate makes (the canonical job of a head that no run decides). A failure of it holds nothing: the check stays what the pass
// decided it to be, and the run says that a read failed.
func TestChecksKeepTheContractCheckWhenOnlyTheReadsOfTheCommentsFail(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.linkHead(cjHead2, "spec_link") // an invalid source: no run decides the head, so the Set reads its canonical job itself
	w.sync(map[int]string{5: cjHead2})
	w.check.executions = headJobsFail{w.jobs}

	if err := w.runChecks(); err == nil {
		t.Error("the run found nothing to say of a read that failed")
	}

	w.want(5, checks.Failure, "invalid-source", 1)
}
