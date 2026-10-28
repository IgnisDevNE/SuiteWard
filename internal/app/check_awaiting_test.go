package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

// The Contract check of a head that awaits the owner's approval (docs/contracts/runtime.md, "Check reason approval-missing" and "The canonical gate"): its approve
// command names a mode when the revision comment does, which is under a required receipt, and under canonical: required it carries the counts of a complete canonical
// run. The check is written once per state (the ledger starts a generation when the head, state, reason, reference or unblocked mark changes), so the counts of a run that
// completed after the first message go out with the next message the ledger writes.

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

// A head that awaits approval has no counts while its run is pending, and the check is not written again when the run completes: the approval is still what is missing.
func TestChecksStateNoCountsOfACanonicalRunThatIsStillPendingForAHeadAwaitingApproval(t *testing.T) {
	w := newExecWorld(t, gateDecl)
	w.execPullFive(execHeadFiles(gateDecl, "v2"))

	w.want(5, checks.InProgress, "approval-missing", 1)

	if _, found := w.lastCheckPayload(5)["execution"]; found {
		t.Errorf("payload %v has counts, but the run is pending", w.lastCheckPayload(5))
	}
}

func TestChecksStateTheCountsOfACompleteCanonicalRunWhenTheyWriteTheCheckOfAHeadAwaitingApproval(t *testing.T) {
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
			w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests(tc.tests...))
			lost := w.loseCheck(5) // the message of the first state is lost, so the ledger writes the same state again

			w.execRun()

			w.want(5, checks.InProgress, "approval-missing", lost.Generation+1) // the approval is still what is missing: the run does not decide the check
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
		})
	}
}

// Only canonical: required states the counts: under advisory the run is evidence of nothing the check reports.
func TestChecksStateNoCountsForAHeadAwaitingApprovalUnlessTheCanonicalRunIsRequired(t *testing.T) {
	decl := execDeclaration("advisory", "off")
	w := newExecWorld(t, decl)
	w.execPullFive(execHeadFiles(decl, "v2"))
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass", "fail"))
	w.loseCheck(5)

	w.execRun()

	if _, found := w.lastCheckPayload(5)["execution"]; found {
		t.Errorf("payload %v has counts under an advisory canonical run", w.lastCheckPayload(5))
	}
}

// A message that the ledger writes again while the run waits for GitHub still carries what the check of the head says: the counts of the complete run and the mode.
func TestChecksSendAgainTheCountsAndTheModeOfAMissingApprovalTheyRestore(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPullFive(execHeadFiles(execDecl, "v2"))
	w.want(5, checks.InProgress, "approval-missing", 1)
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass", "fail"))
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
