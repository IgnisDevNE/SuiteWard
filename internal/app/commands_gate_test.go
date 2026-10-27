package app

import (
	"errors"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The receipt gate (docs/contracts/runtime.md, "The receipt gate in the commands job") runs on an approve after its code was consumed and before ProcessConsent. It
// applies only to an approve that names the proposal's current revision, under a receipt level other than off, and reads the receipt job of the head that the
// synchronization observed.

// approve is the owner's approve of the current revision of the change proposal at the minute, with the mode when it is not empty.
func (x *cjExecWorld) approve(id int64, minute int, mode string) cjComment {
	command := "approve " + cjExecProposal
	if mode != "" {
		command += " " + mode
	}
	return x.sayWithCode(id, minute, command, 0)
}

// cjCounts is the counts of the acknowledgment of a refusal that the receipt did not meet: the states and the marker lines, with every key present.
func cjCounts(detects, inert, newAPI, fails, missing, flaky, skipped, excluded, markers int) map[string]any {
	return map[string]any{"detects": detects, "inert": inert, "newApi": newAPI, "fails": fails, "missing": missing, "flaky": flaky, "skipped": skipped, "excluded": excluded, "markers": markers}
}

// gateWant is the approval receipt that a gate should record for the job that the case set up.
type gateWant func(job execution.Job) execution.ApprovalGate

func gateOf(level domain.ReceiptLevel, state execution.RunState, summary execution.Summary, unblocked bool) gateWant {
	return func(job execution.Job) execution.ApprovalGate {
		return execution.ApprovalGate{HeadSHA: cjHead, ReceiptLevel: level, RunState: state, JobID: job.ID, Summary: summary, Unblocked: unblocked}
	}
}

func TestTheReceiptGateDecidesAnApprove(t *testing.T) {
	detects, inert := []execution.Test{cjTest("TestA", "detects")}, []execution.Test{cjTest("TestA", "inert")}
	required, before, advisory := domain.ReceiptLevelRequired, domain.ReceiptLevelBeforeApproval, domain.ReceiptLevelAdvisory
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{"reference": cjExecProposal}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	for _, tc := range []struct {
		name       string
		level      string
		applicable bool
		setup      func(x *cjExecWorld) execution.Job // the receipt job of the head; the zero job for none
		mode       string
		want       commands.Disposition
		ack        map[string]any // of a refusal; a processed approve has none from the ledger
		gate       gateWant       // nil: no approval receipt
	}{
		{"required: no job yet counts as pending", "required", true, func(x *cjExecWorld) execution.Job { return execution.Job{} }, "fix",
			commands.ReceiptRefused, with(map[string]any{"reason": "receipt-pending", "mode": "fix"}), gateOf(required, execution.RunNone, "", false)},
		{"required: a pending job", "required", true, func(x *cjExecWorld) execution.Job { return x.pending(execution.PlanReceipt, cjHead) }, "",
			commands.ReceiptRefused, with(map[string]any{"reason": "receipt-pending"}), gateOf(required, execution.RunPending, "", false)},
		{"required: a leased job is pending too", "required", true, func(x *cjExecWorld) execution.Job {
			job := x.pending(execution.PlanReceipt, cjHead)
			x.lease()
			return job
		}, "", commands.ReceiptRefused, with(map[string]any{"reason": "receipt-pending"}), gateOf(required, execution.RunPending, "", false)},
		{"required: only a job of another head, so none for this one", "required", true, func(x *cjExecWorld) execution.Job {
			x.receiptJobOf(otherHead)
			return execution.Job{}
		}, "fix", commands.ReceiptRefused, with(map[string]any{"reason": "receipt-pending", "mode": "fix"}), gateOf(required, execution.RunNone, "", false)},
		{"required: an unavailable job", "required", true, func(x *cjExecWorld) execution.Job {
			return x.unavailable(execution.PlanReceipt, cjHead, execution.ReasonNotConfigured)
		}, "fix", commands.ReceiptRefused, with(map[string]any{"reason": "receipt-unavailable", "mode": "fix"}), gateOf(required, execution.RunUnavailable, "", false)},
		{"required: a complete receipt and no mode", "required", true, func(x *cjExecWorld) execution.Job { return x.receiptJob(detects, 0, nil) }, "",
			commands.ReceiptRefused, with(map[string]any{"reason": "mode-required"}), gateOf(required, execution.RunComplete, "", false)},
		{"required: a complete receipt that meets the mode", "required", true, func(x *cjExecWorld) execution.Job { return x.receiptJob(detects, 0, nil) }, "fix",
			commands.Processed, nil, gateOf(required, execution.RunComplete, execution.SummaryMet, false)},
		{"required: a receipt that does not meet the mode", "required", true, func(x *cjExecWorld) execution.Job { return x.receiptJob(inert, 2, nil) }, "fix",
			commands.ReceiptRefused, with(map[string]any{"reason": "mode-not-met", "mode": "fix", "counts": cjCounts(0, 1, 0, 0, 0, 0, 0, 0, 2)}),
			gateOf(required, execution.RunComplete, execution.SummaryNotMet, false)},
		{"required: an empty receipt needs no mode", "required", true, func(x *cjExecWorld) execution.Job { return x.receiptJob(nil, 0, nil) }, "",
			commands.Processed, nil, gateOf(required, execution.RunComplete, "", false)},
		{"required: a receipt of nothing but excluded tests is empty", "required", true, func(x *cjExecWorld) execution.Job {
			return x.receiptJob([]execution.Test{cjTest("TestA", "excluded")}, 0, nil)
		}, "", commands.Processed, nil, gateOf(required, execution.RunComplete, "", false)},
		{"required: a changed test file that does not parse is not an empty receipt", "required", true, func(x *cjExecWorld) execution.Job {
			return x.receiptJob(nil, 0, []string{"a_test.go"})
		}, "", commands.ReceiptRefused, with(map[string]any{"reason": "mode-required"}), gateOf(required, execution.RunComplete, "", false)},
		{"required: a changed test file that does not parse blocks every mode", "required", true, func(x *cjExecWorld) execution.Job {
			return x.receiptJob(detects, 0, []string{"a_test.go"})
		}, "fix", commands.ReceiptRefused, with(map[string]any{"reason": "mode-not-met", "mode": "fix", "counts": cjCounts(1, 0, 0, 0, 0, 0, 0, 0, 0)}),
			gateOf(required, execution.RunComplete, execution.SummaryNotMet, false)},
		{"required: a receipt that does not apply needs no mode", "required", false, func(x *cjExecWorld) execution.Job { return execution.Job{} }, "",
			commands.Processed, nil, gateOf(required, execution.RunNotApplicable, "", false)},
		{"before-approval: no job yet", "before-approval", true, func(x *cjExecWorld) execution.Job { return execution.Job{} }, "",
			commands.ReceiptRefused, with(map[string]any{"reason": "receipt-pending"}), gateOf(before, execution.RunNone, "", false)},
		{"before-approval: a pending job", "before-approval", true, func(x *cjExecWorld) execution.Job { return x.pending(execution.PlanReceipt, cjHead) }, "feature",
			commands.ReceiptRefused, with(map[string]any{"reason": "receipt-pending", "mode": "feature"}), gateOf(before, execution.RunPending, "", false)},
		{"before-approval: an unavailable job does not refuse", "before-approval", true, func(x *cjExecWorld) execution.Job {
			return x.unavailable(execution.PlanReceipt, cjHead, execution.ReasonNoMergeBase)
		}, "", commands.Processed, nil, gateOf(before, execution.RunUnavailable, "", false)},
		{"before-approval: a complete receipt needs no mode", "before-approval", true, func(x *cjExecWorld) execution.Job { return x.receiptJob(inert, 0, nil) }, "",
			commands.Processed, nil, gateOf(before, execution.RunComplete, "", false)},
		{"before-approval: a mode that is given is judged for the record", "before-approval", true, func(x *cjExecWorld) execution.Job { return x.receiptJob(inert, 0, nil) }, "fix",
			commands.Processed, nil, gateOf(before, execution.RunComplete, execution.SummaryNotMet, false)},
		{"advisory: a pending job does not refuse, and the state is recorded", "advisory", true, func(x *cjExecWorld) execution.Job { return x.pending(execution.PlanReceipt, cjHead) }, "",
			commands.Processed, nil, gateOf(advisory, execution.RunPending, "", false)},
		{"advisory: no job yet", "advisory", true, func(x *cjExecWorld) execution.Job { return execution.Job{} }, "",
			commands.Processed, nil, gateOf(advisory, execution.RunNone, "", false)},
		{"off: nothing is gated and nothing is recorded", "off", true, func(x *cjExecWorld) execution.Job { return execution.Job{} }, "",
			commands.Processed, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newCjExecWorld(t, "off", tc.level, tc.applicable)
			job := tc.setup(x)
			x.sync(x.approve(500, 10, tc.mode))

			x.mustRun()

			entry, _ := x.entry(500)
			if entry.Disposition != tc.want || entry.Mode != tc.mode {
				t.Fatalf("entry is %q with the mode %q; want %q with %q\nlogs: %s", entry.Disposition, entry.Mode, tc.want, tc.mode, x.logs)
			}
			processed := tc.want == commands.Processed
			if processed != x.consented(500) {
				t.Errorf("consent recorded: %v; want %v", !processed, processed)
			}
			if processed && entry.Operation != cjOperation(500) || !processed && entry.Operation != "" {
				t.Errorf("entry has the operation %q; want it exactly when processed", entry.Operation)
			}
			if !processed {
				x.requireAckPayload(cjID2, 500, tc.want, tc.ack)
			}
			var want *execution.ApprovalGate
			if tc.gate != nil {
				g := tc.gate(job)
				want = &g
			}
			x.requireGate(500, want)
			x.requireStepsConsumed(500)
			// A processed approve is the owner's latest command for the check; a refusal of the gate is not.
			if _, found, err := x.ledger.LatestOwnerCommand(t.Context(), cjID2, cjPull, cjOwnerID, 0); err != nil || found == (tc.want == commands.ReceiptRefused) {
				t.Errorf("LatestOwnerCommand found %v, error %v; want it found exactly when the approve was processed", found, err)
			}
			x.requireNothingLeaked(cjCode(cjAt(10), 0))
		})
	}
}

// An unavailable receipt that the owner unblocked passes the gate under required with or without a mode, and the record says so.
func TestAnUnblockedUnavailableReceiptPassesTheRequiredGate(t *testing.T) {
	for _, mode := range []string{"", "refactor"} {
		t.Run("mode "+mode, func(t *testing.T) {
			x := newCjExecWorld(t, "off", "required", true)
			job := x.unavailable(execution.PlanReceipt, cjHead, execution.ReasonNoMergeBase)
			x.sync(x.sayWithCode(500, 10, unblockCommand, 0), x.approve(501, 12, mode))

			x.mustRun()

			x.requireEntry(500, commands.Unblocked, map[string]any{"plans": []string{"receipt"}})
			if entry, _ := x.entry(501); entry.Disposition != commands.Processed || entry.Mode != mode {
				t.Fatalf("the approve is %q with the mode %q; want processed with %q\nlogs: %s", entry.Disposition, entry.Mode, mode, x.logs)
			}
			x.requireGate(501, &execution.ApprovalGate{HeadSHA: cjHead, ReceiptLevel: domain.ReceiptLevelRequired, RunState: execution.RunUnavailable, JobID: job.ID, Unblocked: true})
		})
	}
}

// An unblock names the jobs it lifted and nothing else: a job created later for the same head needs its own unblock.
func TestAnUnblockGivenForAnotherJobDoesNotPassTheGate(t *testing.T) {
	x := newCjExecWorld(t, "off", "required", true)
	old := x.unavailable(execution.PlanReceipt, cjHead, execution.ReasonNotConfigured)
	x.sync(x.sayWithCode(500, 10, unblockCommand, 0))
	x.mustRun()
	x.requireUnblocks(500, old)
	// The checks job replaces the job that was created for executor-not-configured: a new job that the unblock does not cover.
	fresh := x.unavailable(execution.PlanReceipt, cjHead, execution.ReasonNoMergeBase)
	x.sync(x.approve(501, 12, "fix"))

	x.mustRun()

	if entry, _ := x.entry(501); entry.Disposition != commands.ReceiptRefused {
		t.Fatalf("the approve is %q; want it refused: the unblock named the old job", entry.Disposition)
	}
	x.requireAckPayload(cjID2, 501, commands.ReceiptRefused, map[string]any{"reference": cjExecProposal, "reason": "receipt-unavailable", "mode": "fix"})
	x.requireGate(501, &execution.ApprovalGate{HeadSHA: cjHead, ReceiptLevel: domain.ReceiptLevelRequired, RunState: execution.RunUnavailable, JobID: fresh.ID})
}

// Only an approve that names the proposal's current revision is gated; a revoke never is, and a revision or a proposal that does not exist is answered by
// ProcessConsent as before.
func TestTheReceiptGateActsOnlyOnAnApproveOfTheCurrentRevision(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		want    commands.Disposition
	}{
		{"another revision of the proposal", "approve P2-R2 fix", commands.Processed},
		{"a proposal that does not exist", "approve P9-R1 fix", commands.UnknownProposal},
		{"a revoke", "revoke " + cjExecProposal, commands.Processed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newCjExecWorld(t, "off", "required", true)
			x.sync(x.sayWithCode(500, 10, tc.command, 0))

			x.mustRun()

			if entry, _ := x.entry(500); entry.Disposition != tc.want {
				t.Fatalf("entry is %q; want %q\nlogs: %s", entry.Disposition, tc.want, x.logs)
			}
			x.requireGate(500, nil)
			x.requireStepsConsumed(500)
		})
	}
}

// A retry after a crash that finds the consent already recorded skips the gate and records the entry as processed with no approval receipt: the state at the
// approval was not recorded, and a state read now would not be that one.
func TestAReplayAfterACrashSkipsTheGateAndWritesNoApprovalReceipt(t *testing.T) {
	x := newCjExecWorld(t, "off", "required", true)
	job := x.receiptJob([]execution.Test{cjTest("TestA", "detects")}, 0, nil)
	x.sync(x.approve(500, 10, "fix"))
	x.ledger.recordErr = errors.New("the process crashed before the ledger entry")

	if err := x.run(); err == nil {
		t.Fatal("the run succeeded although the ledger entry could not be written")
	}
	if !x.consented(500) {
		t.Fatal("ProcessConsent did not run before the crash")
	}
	// A receipt that would now refuse the approve: the job of the head is gone.
	if _, err := x.store.Cancel(t.Context(), job.ID, execution.ReasonObsolete, cjOrigin.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	x.ledger.recordErr = nil

	x.mustRun()

	if entry, _ := x.entry(500); entry.Disposition != commands.Processed || entry.Operation != cjOperation(500) {
		t.Fatalf("entry is %q with the operation %q; want processed: the gate is not read again\nlogs: %s", entry.Disposition, entry.Operation, x.logs)
	}
	x.requireGate(500, nil)
}
