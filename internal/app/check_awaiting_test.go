package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The Contract check of a head that awaits the owner's approval (docs/contracts/runtime.md, "The canonical gate", the paragraphs "Approval missing" and "The approve command of an
// approval-missing check"): its approve command names a mode when the revision comment does, which is under a required receipt, and under canonical: required it carries the
// counts of a complete canonical run. The ledger starts a generation when the head, state, reason, reference, unblocked mark or counted job changes, so the check is written
// again, with the counts, in the pass that finds the run complete.

const (
	modeApprove  = "```\n/suiteward approve P2-R1 <fix|feature|refactor> <code>\n```"
	plainApprove = "```\n/suiteward approve P2-R1 <code>\n```"
)

// summaryOfPayload is the summary that the publisher renders from the payload of a check message.
func summaryOfPayload(t *testing.T, payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var p checkPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	_, summary, err := p.output("check:1:5:1")
	if err != nil {
		t.Fatal(err)
	}
	return summary
}

func TestChecksNameTheModeOfTheApproveOnlyUnderARequiredReceipt(t *testing.T) {
	for _, tc := range []struct {
		name string
		decl string
		mode bool
	}{
		{"receipt required", execDecl, true},
		{"receipt required, canonical off", execDeclaration("off", "required"), true},
		{"receipt before approval", execDeclaration("required", "before-approval"), false},
		{"receipt advisory", execDeclaration("required", "advisory"), false},
		{"receipt off", execDeclaration("required", "off"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newExecWorld(t, tc.decl)
			w.execPull(execHeadFiles(tc.decl, "v2"))

			w.want(5, checks.InProgress, "approval-missing", 1)

			payload := w.lastCheckPayload(5)
			if got := payload["approveMode"] == true; got != tc.mode {
				t.Errorf("payload %v: approveMode = %v, want %v", payload, got, tc.mode)
			}
			summary := summaryOfPayload(t, payload)
			if tc.mode && (!strings.Contains(summary, modeApprove) || strings.Contains(summary, plainApprove)) {
				t.Errorf("the summary does not offer the approve with a mode, or also offers the one a required receipt refuses:\n%s", summary)
			}
			if !tc.mode && (!strings.Contains(summary, plainApprove) || strings.Contains(summary, "<fix|feature|refactor>")) {
				t.Errorf("the summary offers a mode that no receipt asks for:\n%s", summary)
			}
		})
	}
}

// A head that awaits approval has no counts while its run is pending: its first message says so, and nothing is counted yet.
func TestChecksStateNoCountsOfACanonicalRunThatIsStillPendingForAHeadAwaitingApproval(t *testing.T) {
	w := newExecWorld(t, gateDecl)
	w.execPullFive(execHeadFiles(gateDecl, "v2"))

	record := w.want(5, checks.InProgress, "approval-missing", 1)

	if _, found := w.lastCheckPayload(5)["execution"]; found || record.CountedJob != 0 {
		t.Errorf("payload %v, counted job %d: the run is pending, so nothing is counted", w.lastCheckPayload(5), record.CountedJob)
	}
}

// The live path: the first message goes out while the run is pending, and the pass that finds the run complete writes the check again, with the counts. The reason does not
// change (the approval is still what is missing, and a failing test does not hold the check), so what advances the generation is the job whose counts the check states.
func TestChecksWriteTheCheckAgainWithTheCountsWhenTheCanonicalRunOfAHeadAwaitingApprovalCompletes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tests []string
		block bool
	}{
		{"nothing blocks", []string{"pass", "skipped", "excluded"}, false},
		{"a test fails", []string{"pass", "fail"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newExecWorld(t, gateDecl)
			w.execPullFive(execHeadFiles(gateDecl, "v2"))
			w.want(5, checks.InProgress, "approval-missing", 1)
			job := w.onlyJob(5, cjHead2, execution.PlanCanonical)
			w.finish(job, canonicalTests(tc.tests...))

			w.execRun()

			record := w.want(5, checks.InProgress, "approval-missing", 2) // the approval is still what is missing: the run does not decide the check
			if record.CountedJob != job.ID {
				t.Errorf("the check counts job %d, want job %d", record.CountedJob, job.ID)
			}
			payload := w.lastCheckPayload(5)
			if tc.block {
				wantExecution(t, payload, 2, 1, 0, 1, 0, 0, 0)
			} else {
				wantExecution(t, payload, 2, 1, 1, 0, 0, 0, 1) // an excluded test is not one that ran
			}
			summary := summaryOfPayload(t, payload)
			for _, want := range []string{"Canonical run on this head:", "This check reports on the protected contract and on the run of its approved tests.", plainApprove} {
				if !strings.Contains(summary, want) {
					t.Errorf("summary lacks %q:\n%s", want, summary)
				}
			}
			if blocking := strings.Contains(summary, "If this change edits that test, read the change before you approve"); blocking != tc.block {
				t.Errorf("the sentence about a blocking test is present = %v, want %v:\n%s", blocking, tc.block, summary)
			}

			w.execRun() // the same job again is quiet

			w.want(5, checks.InProgress, "approval-missing", 2)
		})
	}
}

// Only canonical: required states the counts: under advisory the run is evidence of nothing the check reports, so its completion moves nothing.
func TestChecksStateNoCountsForAHeadAwaitingApprovalUnlessTheCanonicalRunIsRequired(t *testing.T) {
	decl := execDeclaration("advisory", "off")
	w := newExecWorld(t, decl)
	w.execPullFive(execHeadFiles(decl, "v2"))
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass", "fail"))

	w.execRun()

	record := w.want(5, checks.InProgress, "approval-missing", 1)
	if _, found := w.lastCheckPayload(5)["execution"]; found || record.CountedJob != 0 {
		t.Errorf("payload %v, counted job %d: an advisory canonical run is not counted", w.lastCheckPayload(5), record.CountedJob)
	}
}

// The reasons of every other check keep their generations: a check whose run fails has no counted job.
func TestChecksCountNoJobForAReasonThatIsNotAnApprovalThatIsMissing(t *testing.T) {
	w := gateWorld(t)
	w.execRun()
	w.finish(w.onlyJob(4, bootHead, execution.PlanCanonical), canonicalTests("pass", "fail"))

	w.execRun()

	if record := w.want(4, checks.Failure, "canonical-failing", 2); record.CountedJob != 0 {
		t.Errorf("a canonical-failing check counts job %d", record.CountedJob)
	}
}

// A message that the ledger writes again while the run waits for GitHub still carries what the check of the head says: the counts of the complete run and the mode.
func TestChecksSendAgainTheCountsAndTheModeOfAMissingApprovalTheyRestore(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPullFive(execHeadFiles(execDecl, "v2"))
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass", "fail"))
	w.finish(w.onlyJob(5, cjHead2, execution.PlanReceipt), receiptTests("detects")) // the counts wait for the receipt too
	w.execRun()
	w.want(5, checks.InProgress, "approval-missing", 2)
	w.reader.repos[bootRepo].treeErr[cjHead2] = errors.New("github: 502") // the run waits: it cannot read the head, so it keeps the check it has
	lost := w.loseCheck(5)

	w.runRestored(false) // a wait for GitHub is no failure of the run

	w.want(5, checks.InProgress, "approval-missing", lost.Generation+1)
	payload := w.lastCheckPayload(5)
	wantExecution(t, payload, 2, 1, 0, 1, 0, 0, 0)
	if payload["approveMode"] != true {
		t.Errorf("payload %v: the restored check lost the mode of the approve", payload)
	}
}

// A counted job that cannot be read this time is the stored one, not none: a store that fails for a moment must not flip the check and write two messages.
func TestChecksKeepTheCountedJobWhenItsCountsCannotBeReadThisTime(t *testing.T) {
	w := newExecWorld(t, gateDecl)
	w.execPullFive(execHeadFiles(gateDecl, "v2"))
	job := w.onlyJob(5, cjHead2, execution.PlanCanonical)
	w.finish(job, canonicalTests("pass", "fail"))
	w.execRun()
	w.want(5, checks.InProgress, "approval-missing", 2)
	w.check.executions = &testsFailOnce{Store: w.jobs} // the first read of the tests fails

	w.runRestored(true)

	if record := w.want(5, checks.InProgress, "approval-missing", 2); record.CountedJob != job.ID {
		t.Errorf("the check counts job %d after a failed read, want job %d", record.CountedJob, job.ID)
	}
	w.execRun() // the read works again

	w.want(5, checks.InProgress, "approval-missing", 2)
}

// With the policy unknown the stored check of the head is kept as it is, its counted job included.
func TestChecksKeepTheCountedJobWhileTheExecutionPolicyCannotBeRead(t *testing.T) {
	w := newExecWorld(t, gateDecl)
	w.execPullFive(execHeadFiles(gateDecl, "v2"))
	job := w.onlyJob(5, cjHead2, execution.PlanCanonical)
	w.finish(job, canonicalTests("pass"))
	w.execRun()
	w.want(5, checks.InProgress, "approval-missing", 2)
	w.check.contents = unreadableContents{}

	w.runRestored(true)

	if record := w.want(5, checks.InProgress, "approval-missing", 2); record.CountedJob != job.ID {
		t.Errorf("the check counts job %d while the policy cannot be read, want job %d", record.CountedJob, job.ID)
	}
}

// Only a complete job that the manifest of the canonical governs is counted: a complete job of another version (the canonical moved, and a transient pass reads the job of the
// old one) says nothing of the approved tests of this one.
func TestChecksCountOnlyACompleteJobGovernedByTheManifestOfTheCanonical(t *testing.T) {
	w := newExecWorld(t, gateDecl)
	w.execPullFive(execHeadFiles(gateDecl, "v2"))
	job := w.onlyJob(5, cjHead2, execution.PlanCanonical)
	w.finish(job, canonicalTests("pass", "fail"))
	x := &executionRun{known: true, policy: executionPolicy{canonical: domain.CanonicalLevelRequired}}
	s := &pullState{p: owner.ProjectPolicy{RepositoryID: bootRepoID}, pull: githubsync.PullState{Number: 5, HeadSHA: cjHead2}}

	for _, tc := range []struct {
		name     string
		manifest artifact.Digest
		counted  bool
	}{
		{"the manifest of the canonical", w.canonicalManifest(), true},
		{"the manifest of another version", artifact.Hash([]byte("another version")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.suite = suiteView{manifest: tc.manifest}
			d := &pullDecision{result: checks.Result{Reason: reasonApprovalMissing}}
			// the head has no wanted run but the canonical one, which is final
			executed := executionOutcome{set: domain.Set{CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelOff, ReceiptDecided: true,
				CanonicalJob: domain.Job{ID: job.ID, State: domain.JobComplete}}}

			if err := w.check.approvalCounts(t.Context(), s, x, d, &executed); err != nil {
				t.Fatal(err)
			}

			if counted := executed.counts != nil; counted != tc.counted || (d.result.CountedJob == job.ID) != tc.counted {
				t.Errorf("counts %+v, counted job %d: counted = %v, want %v (job %d)", executed.counts, d.result.CountedJob, counted, tc.counted, job.ID)
			}
		})
	}
}

// The counted job is set where it is read: any reason but an approval that is missing counts none, and a pass that cannot read the run carries the stored job only when the
// stored check of the same head is an approval that is missing too, so a job id never moves onto another reason or another head.
func TestChecksCarryTheStoredCountedJobOnlyForTheSameHeadOfAnApprovalThatIsMissing(t *testing.T) {
	w := newExecWorld(t, gateDecl)
	unknown := &executionRun{} // the policy was not read, so this pass cannot count anything
	for _, tc := range []struct {
		name     string
		sameHead bool
		stored   string // the reason of the stored check
		decided  string // the reason of this pass
		want     int64
	}{
		{"the same approval that is missing", true, reasonApprovalMissing, reasonApprovalMissing, 5},
		{"another head", false, reasonApprovalMissing, reasonApprovalMissing, 0},
		{"a stored check of another reason", true, reasonReady, reasonApprovalMissing, 0},
		{"a decided reason that is not an approval that is missing", true, reasonApprovalMissing, reasonReady, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &pullState{sameHead: tc.sameHead, record: checks.Record{Result: checks.Result{Reason: tc.stored, CountedJob: 5}}}
			d := &pullDecision{result: checks.Result{Reason: tc.decided, CountedJob: 9}} // a value that only this pass may set
			var executed executionOutcome

			if err := w.check.approvalCounts(t.Context(), s, unknown, d, &executed); err != nil {
				t.Fatal(err)
			}

			if d.result.CountedJob != tc.want || executed.counts != nil {
				t.Errorf("counted job %d, counts %+v; want job %d and no counts", d.result.CountedJob, executed.counts, tc.want)
			}
		})
	}
}
